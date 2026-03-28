package usecase

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/erigontech/erigon-l1-bridge/internal/entity"
)

// ClearingUseCase orchestrates the full two-step OP Stack withdrawal lifecycle:
//
//	1. IngestEvent    — called by listener; saves event, creates intent when safe
//	2. ProcessPending — called by orchestrator on tick; drives each intent forward
//	                    through the state machine:
//	                      Safe → Proving → Proven → Finalizable → Finalizing → Finalized
type ClearingUseCase struct {
	events    EventRepository
	clearing  ClearingRepository
	paymaster PaymasterService
	adapters  map[entity.ChainID]BridgeAdapter
	finality  map[entity.ChainID]FinalityChecker
}

func NewClearingUseCase(
	events EventRepository,
	clearing ClearingRepository,
	paymaster PaymasterService,
) *ClearingUseCase {
	return &ClearingUseCase{
		events:    events,
		clearing:  clearing,
		paymaster: paymaster,
		adapters:  make(map[entity.ChainID]BridgeAdapter),
		finality:  make(map[entity.ChainID]FinalityChecker),
	}
}

func (uc *ClearingUseCase) RegisterAdapter(a BridgeAdapter) {
	uc.adapters[a.Chain()] = a
}

func (uc *ClearingUseCase) RegisterFinalityChecker(f FinalityChecker) {
	uc.finality[f.Chain()] = f
}

// IngestEvent is called by the listener when a new bridge event is observed.
// It persists the event and creates a clearing intent if the source block
// has reached safe confirmation depth.
func (uc *ClearingUseCase) IngestEvent(ctx context.Context, event entity.BridgeEvent) error {
	event.Status = entity.StatusPending
	if err := uc.events.Save(ctx, event); err != nil {
		return fmt.Errorf("clearing: save event: %w", err)
	}

	checker, ok := uc.finality[event.SourceChain]
	if !ok {
		return fmt.Errorf("clearing: no finality checker for chain %d", event.SourceChain)
	}
	safe, err := checker.IsSafe(ctx, event.SourceBlock)
	if err != nil {
		return fmt.Errorf("clearing: finality check: %w", err)
	}
	if !safe {
		// Not safe yet — listener will deliver new blocks; we re-check on tick.
		return nil
	}

	return uc.createIntent(ctx, event)
}

// RecheckPendingEvents promotes any saved events that have now reached safe depth
// but hadn't yet when IngestEvent was called. Called by the orchestrator tick.
func (uc *ClearingUseCase) RecheckPendingEvents(ctx context.Context) error {
	events, err := uc.events.GetByStatus(ctx, entity.StatusPending)
	if err != nil {
		return fmt.Errorf("clearing: get pending events: %w", err)
	}
	for _, event := range events {
		checker, ok := uc.finality[event.SourceChain]
		if !ok {
			continue
		}
		safe, err := checker.IsSafe(ctx, event.SourceBlock)
		if err != nil {
			log.Printf("clearing: recheck finality for event %s: %v", event.ID, err)
			continue
		}
		if !safe {
			continue
		}
		if err := uc.createIntent(ctx, event); err != nil {
			log.Printf("clearing: create intent for event %s: %v", event.ID, err)
		}
	}
	return nil
}

// ProcessPending drives all active clearing intents forward through the state machine.
// It is safe to call concurrently — each intent is processed independently.
func (uc *ClearingUseCase) ProcessPending(ctx context.Context) error {
	// First promote any events that have now reached safe depth.
	if err := uc.RecheckPendingEvents(ctx); err != nil {
		log.Printf("clearing: recheck pending events: %v", err)
	}

	intents, err := uc.clearing.GetPending(ctx)
	if err != nil {
		return fmt.Errorf("clearing: get pending intents: %w", err)
	}

	var processed, proving, proven, finalizing, failed int
	for _, intent := range intents {
		next, err := uc.advance(ctx, intent)
		if err != nil {
			log.Printf("clearing: advance intent %s (%s→?): %v", intent.ID, statusName(intent.Event.Status), err)
			_ = uc.markFailed(ctx, intent, err.Error())
			failed++
			continue
		}
		switch next {
		case entity.StatusProving:
			proving++
		case entity.StatusProven:
			proven++
		case entity.StatusFinalizing:
			finalizing++
		case entity.StatusFinalized:
			processed++
		}
	}

	if len(intents) > 0 {
		log.Printf("clearing: tick — intents=%d proving=%d proven=%d finalizing=%d finalized=%d failed=%d",
			len(intents), proving, proven, finalizing, processed, failed)
	}
	return nil
}

// advance moves a single intent to its next state.
// Returns the status it transitioned TO (or the current status if no transition).
func (uc *ClearingUseCase) advance(ctx context.Context, intent entity.ClearingIntent) (entity.BridgeStatus, error) {
	adapter, ok := uc.adapters[intent.Event.SourceChain]
	if !ok {
		return intent.Event.Status, fmt.Errorf("no adapter for chain %d", intent.Event.SourceChain)
	}

	switch intent.Event.Status {

	// ── Step 1: submit the withdrawal proof to L1 ─────────────────────────────
	case entity.StatusSafe:
		txHash, err := adapter.ProveWithdrawal(ctx, intent.Event)
		if err != nil {
			return intent.Event.Status, fmt.Errorf("prove withdrawal: %w", err)
		}
		now := time.Now()
		intent.ProveSubmittedAt = &now
		intent.ProveTxHash = txHash
		intent.Event.Status = entity.StatusProving
		if err := uc.clearing.Save(ctx, intent); err != nil {
			return intent.Event.Status, fmt.Errorf("save proving state: %w", err)
		}
		log.Printf("clearing: intent %s → proving (prove_tx=%s)", intent.ID, shortHash(txHash))
		return entity.StatusProving, nil

	// ── Step 2: wait for the prove tx to land and the proof to be accepted ────
	case entity.StatusProving:
		proven, err := adapter.IsProven(ctx, intent.Event)
		if err != nil {
			return intent.Event.Status, fmt.Errorf("check proven: %w", err)
		}
		if !proven {
			return intent.Event.Status, nil // still waiting
		}
		if err := uc.clearing.UpdateStatus(ctx, intent.ID, entity.StatusProven); err != nil {
			return intent.Event.Status, fmt.Errorf("update to proven: %w", err)
		}
		log.Printf("clearing: intent %s → proven (challenge window started)", intent.ID)
		return entity.StatusProven, nil

	// ── Step 3: wait out the 7-day challenge window ───────────────────────────
	case entity.StatusProven:
		if intent.ProveSubmittedAt == nil {
			// Defensive: mark failed — can't compute window without timestamp.
			return intent.Event.Status, fmt.Errorf("ProveSubmittedAt is nil for proven intent")
		}
		finalizable, err := adapter.IsFinalized(ctx, intent.Event)
		if err != nil {
			return intent.Event.Status, fmt.Errorf("check finalizable: %w", err)
		}
		if !finalizable {
			return intent.Event.Status, nil // still within challenge window
		}
		if err := uc.clearing.UpdateStatus(ctx, intent.ID, entity.StatusFinalizable); err != nil {
			return intent.Event.Status, fmt.Errorf("update to finalizable: %w", err)
		}
		log.Printf("clearing: intent %s → finalizable", intent.ID)
		return entity.StatusFinalizable, nil

	// ── Step 4: submit the finalize call ─────────────────────────────────────
	case entity.StatusFinalizable:
		txHash, err := adapter.FinalizeWithdrawal(ctx, intent.Event)
		if err != nil {
			return intent.Event.Status, fmt.Errorf("finalize withdrawal: %w", err)
		}
		intent.FinalizeTxHash = txHash
		intent.Event.Status = entity.StatusFinalizing
		if err := uc.clearing.Save(ctx, intent); err != nil {
			return intent.Event.Status, fmt.Errorf("save finalizing state: %w", err)
		}
		log.Printf("clearing: intent %s → finalizing (finalize_tx=%s)", intent.ID, shortHash(txHash))
		return entity.StatusFinalizing, nil

	// ── Step 5: confirm finalization ──────────────────────────────────────────
	case entity.StatusFinalizing:
		done, err := adapter.IsSettled(ctx, intent.Event)
		if err != nil {
			return intent.Event.Status, fmt.Errorf("check settled: %w", err)
		}
		if !done {
			return intent.Event.Status, nil
		}
		if err := uc.clearing.UpdateStatus(ctx, intent.ID, entity.StatusFinalized); err != nil {
			return intent.Event.Status, fmt.Errorf("update to finalized: %w", err)
		}
		log.Printf("clearing: intent %s → finalized ✓ (finalize_tx=%s)", intent.ID, shortHash(intent.FinalizeTxHash))
		return entity.StatusFinalized, nil

	default:
		// StatusFinalized and StatusFailed are terminal — GetPending won't return them.
		return intent.Event.Status, nil
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func (uc *ClearingUseCase) createIntent(ctx context.Context, event entity.BridgeEvent) error {
	event.Status = entity.StatusSafe
	if err := uc.events.UpdateStatus(ctx, event.ID, entity.StatusSafe); err != nil {
		return fmt.Errorf("clearing: update event status: %w", err)
	}
	intent := entity.ClearingIntent{
		ID:        fmt.Sprintf("intent-%s", event.ID),
		Event:     event,
		Deadline:  time.Now().Add(14 * 24 * time.Hour), // 14 days — 2× challenge window
		CreatedAt: time.Now(),
	}
	if err := uc.clearing.Save(ctx, intent); err != nil {
		return fmt.Errorf("clearing: save intent: %w", err)
	}
	log.Printf("clearing: intent created for event %s (chain=%d block=%d)", event.ID, event.SourceChain, event.SourceBlock)
	return nil
}

func (uc *ClearingUseCase) markFailed(ctx context.Context, intent entity.ClearingIntent, reason string) error {
	intent.Event.Status = entity.StatusFailed
	intent.FailureReason = reason
	return uc.clearing.Save(ctx, intent)
}

func statusName(s entity.BridgeStatus) string {
	names := map[entity.BridgeStatus]string{
		entity.StatusPending:     "pending",
		entity.StatusSafe:        "safe",
		entity.StatusProving:     "proving",
		entity.StatusProven:      "proven",
		entity.StatusFinalizable: "finalizable",
		entity.StatusFinalizing:  "finalizing",
		entity.StatusFinalized:   "finalized",
		entity.StatusFailed:      "failed",
	}
	if n, ok := names[s]; ok {
		return n
	}
	return fmt.Sprintf("unknown(%d)", s)
}

func shortHash(h string) string {
	if len(h) > 10 {
		return h[:10] + "…"
	}
	return h
}
