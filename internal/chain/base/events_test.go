package base

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/erigontech/erigon-l1-bridge/internal/entity"
)

// Helper: build a minimal types.Log with the given topic[0] and data.
func makeLog(topic0 common.Hash, extraTopics []common.Hash, data []byte, txHash common.Hash, idx uint) types.Log {
	topics := append([]common.Hash{topic0}, extraTopics...)
	return types.Log{
		Topics:      topics,
		Data:        data,
		TxHash:      txHash,
		BlockNumber: 12345,
		Index:       idx,
	}
}

// padAddress pads a hex address to 32 bytes (as it appears in ABI-encoded topics/data).
func padAddress(hex string) []byte {
	addr := common.HexToAddress(hex)
	padded := make([]byte, 32)
	copy(padded[12:], addr.Bytes())
	return padded
}

// padUint256 pads a *big.Int to 32 bytes big-endian.
func padUint256(n *big.Int) []byte {
	b := make([]byte, 32)
	nb := n.Bytes()
	copy(b[32-len(nb):], nb)
	return b
}

func TestTopicHashesAreCorrect(t *testing.T) {
	// These expected values are computed independently by keccak256(sig).
	// If these assertions fail, the event filter will miss every event on-chain.
	cases := []struct {
		name     string
		topic    common.Hash
		expected string
	}{
		{
			name:     "ETHBridgeInitiated",
			topic:    topicETHBridgeInitiated,
			expected: "0x2849b43074093a05396b6f2a937dee8565b15a48a7b3d4bffb732a5017380af5",
		},
		{
			name:     "ERC20BridgeInitiated",
			topic:    topicERC20BridgeInitiated,
			expected: "0x7ff126db8024424bbfd9826e8ab82ff59136289ea440b04b39a0df1b03b9cabf",
		},
		{
			name:     "MessagePassed",
			topic:    topicMessagePassed,
			expected: "0x02a52367d10742d8032712c1bb8e0144ff1ec5ffda1ed7d70bb05a2744955054",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.topic.Hex()
			if got != tc.expected {
				t.Errorf("topic hash mismatch\n  got  %s\n  want %s", got, tc.expected)
			}
		})
	}
}

func TestParseETHBridgeInitiated(t *testing.T) {
	from := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	to := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	amount := big.NewInt(1_000_000_000_000_000_000) // 1 ETH in wei

	// Build ABI-encoded data: uint256 amount (32 bytes) + bytes offset (32) + bytes length (32)
	data := make([]byte, 96)
	copy(data[0:32], padUint256(amount))
	// offset for bytes = 64 (points past the first two 32-byte slots)
	copy(data[32:64], padUint256(big.NewInt(64)))
	// length of extraData = 0

	l := makeLog(
		topicETHBridgeInitiated,
		[]common.Hash{
			common.BytesToHash(common.HexToAddress(from).Bytes()),
			common.BytesToHash(common.HexToAddress(to).Bytes()),
		},
		data,
		common.HexToHash("0xdeadbeef"),
		0,
	)

	event, ok, err := parseLog(l)
	if err != nil {
		t.Fatalf("parseLog error: %v", err)
	}
	if !ok {
		t.Fatal("expected event to be recognised")
	}

	if event.SourceChain != entity.ChainIDBase {
		t.Errorf("source chain: got %d, want %d", event.SourceChain, entity.ChainIDBase)
	}
	if event.TargetChain != entity.ChainIDEthereum {
		t.Errorf("target chain: got %d, want %d", event.TargetChain, entity.ChainIDEthereum)
	}
	if event.Direction != entity.L2ToL1 {
		t.Errorf("direction: got %d, want L2ToL1", event.Direction)
	}
	if event.Token != "" {
		t.Errorf("token: expected empty string for native ETH, got %q", event.Token)
	}
	if event.Amount.Cmp(amount) != 0 {
		t.Errorf("amount: got %s, want %s", event.Amount.String(), amount.String())
	}
	if event.SourceBlock != 12345 {
		t.Errorf("source block: got %d, want 12345", event.SourceBlock)
	}
	if event.Status != entity.StatusPending {
		t.Errorf("status: got %d, want StatusPending", event.Status)
	}
}

func TestParseERC20BridgeInitiated(t *testing.T) {
	localToken := "0x1111111111111111111111111111111111111111"
	remoteToken := "0x2222222222222222222222222222222222222222"
	from := "0x3333333333333333333333333333333333333333"
	to := "0x4444444444444444444444444444444444444444"
	amount := big.NewInt(500_000_000) // 500 USDC (6 decimals)

	// data: address to (32 bytes, right-padded) + uint256 amount (32 bytes)
	data := make([]byte, 64)
	copy(data[12:32], common.HexToAddress(to).Bytes())
	copy(data[32:64], padUint256(amount))

	l := makeLog(
		topicERC20BridgeInitiated,
		[]common.Hash{
			common.BytesToHash(common.HexToAddress(localToken).Bytes()),
			common.BytesToHash(common.HexToAddress(remoteToken).Bytes()),
			common.BytesToHash(common.HexToAddress(from).Bytes()),
		},
		data,
		common.HexToHash("0xc0ffee00"),
		1,
	)

	event, ok, err := parseLog(l)
	if err != nil {
		t.Fatalf("parseLog error: %v", err)
	}
	if !ok {
		t.Fatal("expected event to be recognised")
	}

	if event.Token == "" {
		t.Error("token: expected non-empty for ERC20 bridge")
	}
	if event.Amount.Cmp(amount) != 0 {
		t.Errorf("amount: got %s, want %s", event.Amount, amount)
	}
}

func TestParseUnknownTopic(t *testing.T) {
	l := makeLog(
		common.HexToHash("0xdeaddeaddeaddeaddeaddeaddeaddeaddeaddeaddeaddeaddeaddeaddeaddead"),
		nil,
		nil,
		common.HexToHash("0x1234"),
		0,
	)
	_, ok, err := parseLog(l)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected unknown topic to return ok=false")
	}
}

func TestParseEmptyLog(t *testing.T) {
	l := types.Log{}
	_, ok, err := parseLog(l)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected empty log to return ok=false")
	}
}

func TestEventIDUniqueness(t *testing.T) {
	txA := common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	txB := common.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	l1 := makeLog(topicETHBridgeInitiated, nil, nil, txA, 0)
	l2 := makeLog(topicETHBridgeInitiated, nil, nil, txA, 1) // same tx, different index
	l3 := makeLog(topicETHBridgeInitiated, nil, nil, txB, 0) // different tx, same index

	if eventID(l1) == eventID(l2) {
		t.Error("same tx, different log index should produce different IDs")
	}
	if eventID(l1) == eventID(l3) {
		t.Error("different tx should produce different IDs")
	}
}

func TestParseMessagePassedFullDecode(t *testing.T) {
	// Versioned nonce: upper 2 bytes = version 1, lower byte = nonce 42.
	// Build via SetBytes to avoid int64 overflow (versioned nonces are uint256).
	nonceBytes := make([]byte, 32)
	nonceBytes[1] = 0x01 // version
	nonceBytes[31] = 42  // nonce
	nonce := new(big.Int).SetBytes(nonceBytes)

	sender := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	target := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	value := big.NewInt(5e17) // 0.5 ETH in wei
	gasLimit := big.NewInt(100_000)
	callData := []byte{0xde, 0xad, 0xbe, 0xef}

	withdrawalHash := common.HexToHash("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")

	// ABI layout: value(32) | gasLimit(32) | offset(32)=160 | len(32)=4 | data(32,padded) | hash(32)
	data := make([]byte, 192)
	copy(data[0:32], padUint256(value))
	copy(data[32:64], padUint256(gasLimit))
	copy(data[64:96], padUint256(big.NewInt(160)))
	copy(data[96:128], padUint256(big.NewInt(int64(len(callData)))))
	copy(data[128:132], callData)
	copy(data[160:192], withdrawalHash.Bytes())

	l := makeLog(
		topicMessagePassed,
		[]common.Hash{
			common.BytesToHash(nonceBytes),                          // topics[1] = nonce
			common.BytesToHash(common.HexToAddress(sender).Bytes()), // topics[2] = sender
			common.BytesToHash(common.HexToAddress(target).Bytes()), // topics[3] = target
		},
		data,
		common.HexToHash("0xabcdef01"),
		3,
	)

	event, ok, err := parseLog(l)
	if err != nil {
		t.Fatalf("parseLog: %v", err)
	}
	if !ok {
		t.Fatal("expected event recognised")
	}
	if event.Nonce == nil {
		t.Fatal("Nonce is nil")
	}
	if event.Nonce.Cmp(nonce) != 0 {
		t.Errorf("Nonce: got %s, want %s", event.Nonce, nonce)
	}
	if event.MsgGasLimit == nil {
		t.Fatal("MsgGasLimit is nil")
	}
	if event.MsgGasLimit.Cmp(gasLimit) != 0 {
		t.Errorf("MsgGasLimit: got %s, want %s", event.MsgGasLimit, gasLimit)
	}
	if event.Amount.Cmp(value) != 0 {
		t.Errorf("Amount: got %s, want %s", event.Amount, value)
	}
	if len(event.Data) != len(callData) {
		t.Errorf("Data length: got %d, want %d", len(event.Data), len(callData))
	}
	if event.WithdrawalHash != [32]byte(withdrawalHash) {
		t.Errorf("WithdrawalHash: got %x, want %x", event.WithdrawalHash, withdrawalHash)
	}
}
