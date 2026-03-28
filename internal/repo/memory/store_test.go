package memory_test

import (
	"context"
	"math/big"
	"fmt"
	"testing"
	"time"

	"github.com/erigontech/erigon-l1-bridge/internal/entity"
	"github.com/erigontech/erigon-l1-bridge/internal/repo/memory"
)

func TestEventStoreRoundtrip(t *testing.T) {
	store := memory.NewEventStore()
	ctx := context.Background()

	event := entity.BridgeEvent{
		ID:          "evt-001",
		SourceChain: entity.ChainIDBase,
		TargetChain: entity.ChainIDEthereum,
		Direction:   entity.L2ToL1,
		Amount:      big.NewInt(1e18),
		SourceBlock: 100,
		Timestamp:   time.Now(),
		Status:      entity.StatusPending,
	}

	if err := store.Save(ctx, event); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := store.GetByID(ctx, "evt-001")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != event.ID {
		t.Errorf("ID: got %s, want %s", got.ID, event.ID)
	}
}

func TestEventStoreGetByStatus(t *testing.T) {
	store := memory.NewEventStore()
	ctx := context.Background()

	for i, status := range []entity.BridgeStatus{
		entity.StatusPending, entity.StatusSafe, entity.StatusProven,
	} {
		store.Save(ctx, entity.BridgeEvent{
			ID:     fmt.Sprintf("evt-%d", i),
			Status: status,
			Amount: big.NewInt(0),
		})
	}

	pending, _ := store.GetByStatus(ctx, entity.StatusPending)
	if len(pending) != 1 {
		t.Errorf("expected 1 pending, got %d", len(pending))
	}
}

func TestEventStoreUpdateStatus(t *testing.T) {
	store := memory.NewEventStore()
	ctx := context.Background()

	store.Save(ctx, entity.BridgeEvent{ID: "evt-001", Status: entity.StatusPending, Amount: big.NewInt(0)})

	if err := store.UpdateStatus(ctx, "evt-001", entity.StatusSafe); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := store.GetByID(ctx, "evt-001")
	if got.Status != entity.StatusSafe {
		t.Errorf("status: got %d, want StatusSafe", got.Status)
	}
}

func TestEventStoreUpdateStatusNotFound(t *testing.T) {
	store := memory.NewEventStore()
	ctx := context.Background()
	if err := store.UpdateStatus(ctx, "nonexistent", entity.StatusSafe); err == nil {
		t.Error("expected error for nonexistent event")
	}
}

func TestClearingStoreGetPendingExcludesTerminal(t *testing.T) {
	store := memory.NewClearingStore()
	ctx := context.Background()

	statuses := []entity.BridgeStatus{
		entity.StatusSafe,
		entity.StatusProving,
		entity.StatusProven,
		entity.StatusFinalizable,
		entity.StatusFinalizing,
		entity.StatusFinalized, // terminal — should NOT appear in GetPending
		entity.StatusFailed,    // terminal — should NOT appear in GetPending
	}

	for i, s := range statuses {
		store.Save(ctx, entity.ClearingIntent{
			ID:    fmt.Sprintf("intent-%d", i),
			Event: entity.BridgeEvent{Status: s, Amount: big.NewInt(0)},
		})
	}

	pending, err := store.GetPending(ctx)
	if err != nil {
		t.Fatalf("get pending: %v", err)
	}
	// 7 total, 2 terminal = 5 pending
	if len(pending) != 5 {
		t.Errorf("expected 5 pending (excluding finalized+failed), got %d", len(pending))
	}
}

func TestClearingStoreUpdateStatus(t *testing.T) {
	store := memory.NewClearingStore()
	ctx := context.Background()

	store.Save(ctx, entity.ClearingIntent{
		ID:    "intent-001",
		Event: entity.BridgeEvent{Status: entity.StatusSafe, Amount: big.NewInt(0)},
	})

	transitions := []entity.BridgeStatus{
		entity.StatusProving,
		entity.StatusProven,
		entity.StatusFinalizable,
		entity.StatusFinalizing,
		entity.StatusFinalized,
	}

	for _, next := range transitions {
		if err := store.UpdateStatus(ctx, "intent-001", next); err != nil {
			t.Fatalf("update to %d: %v", next, err)
		}
		got, _ := store.GetByID(ctx, "intent-001")
		if got.Event.Status != next {
			t.Errorf("after update: got status %d, want %d", got.Event.Status, next)
		}
	}

	// After finalizing it should not appear in GetPending.
	pending, _ := store.GetPending(ctx)
	if len(pending) != 0 {
		t.Errorf("expected 0 pending after finalization, got %d", len(pending))
	}
}
