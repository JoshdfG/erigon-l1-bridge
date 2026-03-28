package usecase

import (
	"context"
	"math/big"

	"github.com/erigontech/erigon-l1-bridge/internal/entity"
)

// ChainClient abstracts a connection to an EVM chain.
// One implementation per chain lives in internal/chain/<name>/.
type ChainClient interface {
	ChainID() entity.ChainID
	BlockNumber(ctx context.Context) (uint64, error)
	SubscribeLogs(ctx context.Context, handler func(entity.BridgeEvent)) error
	SendTransaction(ctx context.Context, raw []byte) (string, error)
	GasPrice(ctx context.Context) (*big.Int, error)
}

// BridgeAdapter abstracts the proof and finality model of a specific L2.
//
// The full lifecycle for an OP Stack withdrawal is:
//
//	ProveWithdrawal    → submit proof to L1 portal
//	IsProven           → confirm proof landed and was accepted
//	IsFinalized        → check challenge window has elapsed
//	FinalizeWithdrawal → submit finalize call to L1 portal
//	IsSettled          → confirm finalize tx landed
//
// Each method is idempotent — safe to call multiple times during orchestrator ticks.
type BridgeAdapter interface {
	Chain() entity.ChainID

	// ProveWithdrawal submits the withdrawal proof to the L1 portal.
	// Returns the L1 tx hash. Called once when the intent enters StatusSafe.
	ProveWithdrawal(ctx context.Context, event entity.BridgeEvent) (string, error)

	// IsProven returns true once the prove tx has been included and the portal
	// has accepted the proof. Polled while intent is StatusProving.
	IsProven(ctx context.Context, event entity.BridgeEvent) (bool, error)

	// IsFinalized returns true once the 7-day challenge window has elapsed and
	// the dispute game resolved as DEFENDER_WINS. Polled while StatusProven.
	IsFinalized(ctx context.Context, event entity.BridgeEvent) (bool, error)

	// FinalizeWithdrawal submits the finalize call to the L1 portal.
	// Returns the L1 tx hash. Called once when the intent enters StatusFinalizable.
	FinalizeWithdrawal(ctx context.Context, event entity.BridgeEvent) (string, error)

	// IsSettled returns true once the finalize tx has been included on L1.
	// Polled while intent is StatusFinalizing.
	IsSettled(ctx context.Context, event entity.BridgeEvent) (bool, error)
}

// FinalityChecker decides when a chain event is safe to act on.
type FinalityChecker interface {
	Chain() entity.ChainID
	IsSafe(ctx context.Context, blockNumber uint64) (bool, error)
	IsFinalized(ctx context.Context, blockNumber uint64) (bool, error)
}

// PaymasterService handles EIP-7702 gas delegation.
type PaymasterService interface {
	SponsorIntent(ctx context.Context, intent entity.ClearingIntent) ([]byte, error)
	Balance(ctx context.Context, chain entity.ChainID) (*big.Int, error)
}

// ClearingRepository persists clearing intents across orchestrator ticks.
type ClearingRepository interface {
	Save(ctx context.Context, intent entity.ClearingIntent) error
	GetByID(ctx context.Context, id string) (*entity.ClearingIntent, error)
	GetPending(ctx context.Context) ([]entity.ClearingIntent, error)
	UpdateStatus(ctx context.Context, id string, status entity.BridgeStatus) error
}

// EventRepository persists observed bridge events.
type EventRepository interface {
	Save(ctx context.Context, event entity.BridgeEvent) error
	GetByID(ctx context.Context, id string) (*entity.BridgeEvent, error)
	GetByStatus(ctx context.Context, status entity.BridgeStatus) ([]entity.BridgeEvent, error)
	UpdateStatus(ctx context.Context, id string, status entity.BridgeStatus) error
}
