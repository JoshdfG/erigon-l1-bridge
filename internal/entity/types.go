package entity

import (
	"math/big"
	"time"
)

type ChainID uint64

const (
	ChainIDEthereum ChainID = 1
	ChainIDBase     ChainID = 8453
	ChainIDOptimism ChainID = 10
	ChainIDArbitrum ChainID = 42161
)

type BridgeDirection uint8

const (
	L2ToL1 BridgeDirection = iota
	L1ToL2
	L2ToL2
)

// BridgeStatus is the full lifecycle of a cross-chain withdrawal.
// The state machine is:
//
//	Pending → Proven → Finalizable → Finalized
//	        ↘ Failed (at any stage)
//
// Proven means proveWithdrawalTransaction has been submitted on L1.
// Finalizable means the 7-day challenge window has elapsed.
// Finalized means finalizeWithdrawalTransaction confirmed on L1.
type BridgeStatus uint8

const (
	StatusPending     BridgeStatus = iota // observed on source chain, not yet safe
	StatusSafe                            // safe block confirmations reached, intent created
	StatusProving                         // proveWithdrawalTransaction submitted, awaiting inclusion
	StatusProven                          // proof confirmed on L1, challenge window running
	StatusFinalizable                     // challenge window elapsed, ready to finalize
	StatusFinalizing                      // finalizeWithdrawalTransaction submitted
	StatusFinalized                       // fully settled
	StatusFailed                          // unrecoverable error — requires manual review
)

type BridgeEvent struct {
	ID          string
	SourceChain ChainID
	TargetChain ChainID
	Direction   BridgeDirection
	Sender      string
	Recipient   string
	Token       string
	Amount      *big.Int
	Data        []byte
	SourceTx    string
	SourceBlock uint64
	Timestamp   time.Time
	Status      BridgeStatus

	// WithdrawalTx fields — populated from the MessagePassed event.
	// Required to reconstruct the exact WithdrawalTransaction struct for
	// OptimismPortal2.proveWithdrawalTransaction / finalizeWithdrawalTransaction.
	// Zero values indicate the event came from StandardBridge (not direct MessagePassed).
	Nonce          *big.Int // versioned nonce from L2ToL1MessagePasser
	MsgGasLimit    *big.Int // gasLimit field in the withdrawal tx
	WithdrawalHash [32]byte // keccak256 of abi.encode(WithdrawalTx) — from MessagePassed
}

type ClearingIntent struct {
	ID          string
	Event       BridgeEvent
	Paymaster   string
	GasEstimate *big.Int
	Deadline    time.Time
	CreatedAt   time.Time

	// Proof tracking
	ProveSubmittedAt *time.Time // when proveWithdrawalTransaction was submitted
	ProveTxHash      string     // L1 tx hash of the prove call
	FinalizeTxHash   string     // L1 tx hash of the finalize call
	FailureReason    string     // set on StatusFailed
}

type FinalityConfig struct {
	ChainID            ChainID
	SafeConfirmations  uint64
	FinalizedDelay     uint64
	ProofWindowSeconds uint64
}
