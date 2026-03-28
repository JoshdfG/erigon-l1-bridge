package base

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/erigontech/erigon-l1-bridge/internal/entity"
)

func TestComputeStorageKey(t *testing.T) {
	// The storage key must match what Solidity computes for:
	//   keccak256(abi.encode(withdrawalHash, uint256(0)))
	// We verify the function produces a 32-byte hex string in the right format.
	var hash [32]byte
	copy(hash[:], crypto.Keccak256([]byte("test withdrawal")))

	key := computeStorageKey(hash)
	if len(key) != 66 { // "0x" + 64 hex chars
		t.Errorf("storage key wrong length: %d, got %q", len(key), key)
	}
	if key[:2] != "0x" {
		t.Errorf("storage key missing 0x prefix: %q", key)
	}
}

func TestComputeWithdrawalHashDeterministic(t *testing.T) {
	// Same inputs must always produce the same hash.
	// Set Nonce + MsgGasLimit so buildWithdrawalTx uses the direct path (no L2 client needed).
	a := &Adapter{}

	event := entity.BridgeEvent{
		ID:          "evt-abc",
		Sender:      "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Recipient:   "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Amount:      big.NewInt(1e18),
		Data:        []byte{},
		Nonce:       big.NewInt(1),
		MsgGasLimit: big.NewInt(200_000),
	}

	h1, err := a.computeWithdrawalHash(context.Background(), event)
	if err != nil {
		t.Fatalf("hash 1: %v", err)
	}
	h2, err := a.computeWithdrawalHash(context.Background(), event)
	if err != nil {
		t.Fatalf("hash 2: %v", err)
	}

	if h1 != h2 {
		t.Errorf("hash not deterministic:\n  h1=%x\n  h2=%x", h1, h2)
	}
}

func TestBuildWithdrawalTxAddresses(t *testing.T) {
	a := &Adapter{}

	sender := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	recipient := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	event := entity.BridgeEvent{
		ID:          "evt-001",
		Sender:      sender,
		Recipient:   recipient,
		Amount:      big.NewInt(500),
		Nonce:       big.NewInt(7),
		MsgGasLimit: big.NewInt(150_000),
	}

	wtx, err := a.buildWithdrawalTx(context.Background(), event)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if wtx.Sender != common.HexToAddress(sender) {
		t.Errorf("sender mismatch: got %s, want %s", wtx.Sender.Hex(), sender)
	}
	if wtx.Target != common.HexToAddress(recipient) {
		t.Errorf("target mismatch: got %s, want %s", wtx.Target.Hex(), recipient)
	}
	if wtx.Value.Cmp(big.NewInt(500)) != 0 {
		t.Errorf("value mismatch: got %s, want 500", wtx.Value)
	}
}

func TestWithdrawalTxABIStructRoundtrip(t *testing.T) {
	wtx := withdrawalTx{
		Nonce:    big.NewInt(42),
		Sender:   common.HexToAddress("0xaaaa"),
		Target:   common.HexToAddress("0xbbbb"),
		Value:    big.NewInt(1e18),
		GasLimit: big.NewInt(200_000),
		Data:     []byte{0x01, 0x02, 0x03},
	}

	s := wtx.toABIStruct()
	if s.Nonce.Cmp(wtx.Nonce) != 0 {
		t.Errorf("nonce mismatch")
	}
	if s.Sender != wtx.Sender {
		t.Errorf("sender mismatch")
	}
	if s.GasLimit.Cmp(wtx.GasLimit) != 0 {
		t.Errorf("gasLimit mismatch")
	}
}

func TestHashToUint256Deterministic(t *testing.T) {
	n1 := hashToUint256("event-123")
	n2 := hashToUint256("event-123")
	n3 := hashToUint256("event-456")

	if n1.Cmp(n2) != 0 {
		t.Error("same input should produce same uint256")
	}
	if n1.Cmp(n3) == 0 {
		t.Error("different inputs should produce different uint256")
	}
	if n1.Sign() <= 0 {
		t.Error("uint256 should be positive")
	}
}

func TestABILoading(t *testing.T) {
	// Verify all three inline ABI strings parse without error.
	for name, json := range map[string]string{
		"portal":  portalABIJSON,
		"factory": factoryABIJSON,
		"game":    gameABIJSON,
	} {
		_, err := loadABI(json)
		if err != nil {
			t.Errorf("failed to load %s ABI: %v", name, err)
		}
	}
}

func TestPortalABIMethodsPresent(t *testing.T) {
	a, err := loadABI(portalABIJSON)
	if err != nil {
		t.Fatalf("load ABI: %v", err)
	}

	required := []string{"proveWithdrawalTransaction", "finalizeWithdrawalTransaction", "provenWithdrawals"}
	for _, m := range required {
		if _, ok := a.Methods[m]; !ok {
			t.Errorf("portal ABI missing method: %s", m)
		}
	}
}

func TestGameABIMethodsPresent(t *testing.T) {
	a, err := loadABI(gameABIJSON)
	if err != nil {
		t.Fatalf("load ABI: %v", err)
	}

	required := []string{"status", "rootClaim", "l2BlockNumber", "resolvedAt"}
	for _, m := range required {
		if _, ok := a.Methods[m]; !ok {
			t.Errorf("game ABI missing method: %s", m)
		}
	}
}

func TestGameDefenderWinsConstant(t *testing.T) {
	// GameDefenderWins must be 2 — this is hardcoded in FaultDisputeGame.sol.
	if entity.GameDefenderWins != 2 {
		t.Errorf("GameDefenderWins must be 2, got %d", entity.GameDefenderWins)
	}
}

func TestFinalizationPeriodPositive(t *testing.T) {
	if FinalizationPeriod <= 0 {
		t.Error("FinalizationPeriod must be positive")
	}
}
