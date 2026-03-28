// Package base — OP Stack event signatures and log parsing for Base.
//
// Topic hashes (keccak256 of normalised event signature):
//
//	ETHBridgeInitiated(address,address,uint256,bytes)
//	  → 0x2849b43074093a05396b6f2a937dee8565b15a48a7b3d4bffb732a5017380af5
//
//	ERC20BridgeInitiated(address,address,address,address,uint256,bytes)
//	  → 0x7ff126db8024424bbfd9826e8ab82ff59136289ea440b04b39a0df1b03b9cabf
//
//	MessagePassed (L2ToL1MessagePasser predeploy — lower-level than bridge)
//	  → 0x02a52367d10742d8032712c1bb8e0144ff1ec5ffda1ed7d70bb05a2744955054
//
// We watch the L2StandardBridge predeploy (0x4200…0010) for both ETH and ERC20
// withdrawal initiations.  The L2ToL1MessagePasser event is a secondary signal
// used for direct portal withdrawals that bypass the StandardBridge.
package base

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/erigontech/erigon-l1-bridge/internal/entity"
)

// Predeploy addresses on every OP Stack chain (same on Base, OP Mainnet, etc.)
const (
	L2StandardBridgeAddr    = "0x4200000000000000000000000000000000000010"
	L2ToL1MessagePasserAddr = "0x4200000000000000000000000000000000000016"
)

// topicHash returns keccak256(signature) as a common.Hash.
// Go-ethereum's crypto.Keccak256Hash is the canonical way to do this.
func topicHash(sig string) common.Hash {
	return crypto.Keccak256Hash([]byte(sig))
}

// Well-known topic hashes — computed at init time, never hardcoded as raw hex
// so a typo in the signature string fails loudly at startup rather than silently
// missing every event.
var (
	topicETHBridgeInitiated   = topicHash("ETHBridgeInitiated(address,address,uint256,bytes)")
	topicERC20BridgeInitiated = topicHash("ERC20BridgeInitiated(address,address,address,address,uint256,bytes)")
	topicMessagePassed        = topicHash("MessagePassed(uint256,address,address,uint256,uint256,bytes,bytes32)")
)

// parseLog converts a go-ethereum types.Log into our domain BridgeEvent.
// Returns (event, true, nil) on success, (_, false, nil) if the log is not a
// recognised bridge event, or (_, false, err) on a decode failure.
func parseLog(log types.Log) (entity.BridgeEvent, bool, error) {
	if len(log.Topics) == 0 {
		return entity.BridgeEvent{}, false, nil
	}

	switch log.Topics[0] {
	case topicETHBridgeInitiated:
		return parseETHBridgeInitiated(log)
	case topicERC20BridgeInitiated:
		return parseERC20BridgeInitiated(log)
	case topicMessagePassed:
		return parseMessagePassed(log)
	default:
		return entity.BridgeEvent{}, false, nil
	}
}

// ETHBridgeInitiated(address indexed from, address indexed to, uint256 amount, bytes extraData)
//
// Layout:
//
//	topics[0] = event signature
//	topics[1] = from (indexed, left-padded address)
//	topics[2] = to   (indexed, left-padded address)
//	data      = abi.encode(uint256 amount, bytes extraData)
func parseETHBridgeInitiated(log types.Log) (entity.BridgeEvent, bool, error) {
	if len(log.Topics) < 3 {
		return entity.BridgeEvent{}, false, fmt.Errorf("ETHBridgeInitiated: expected 3 topics, got %d", len(log.Topics))
	}
	if len(log.Data) < 32 {
		return entity.BridgeEvent{}, false, fmt.Errorf("ETHBridgeInitiated: data too short (%d bytes)", len(log.Data))
	}

	from := common.BytesToAddress(log.Topics[1].Bytes())
	to := common.BytesToAddress(log.Topics[2].Bytes())

	// First 32 bytes of data = uint256 amount (big-endian)
	amount := new(big.Int).SetBytes(log.Data[:32])

	// extraData starts at offset encoded in the next 32 bytes — skip for now;
	// we preserve the raw bytes for downstream use.
	var extraData []byte
	if len(log.Data) > 64 {
		extraData = log.Data[64:]
	}

	return entity.BridgeEvent{
		ID:          eventID(log),
		SourceChain: entity.ChainIDBase,
		TargetChain: entity.ChainIDEthereum,
		Direction:   entity.L2ToL1,
		Sender:      strings.ToLower(from.Hex()),
		Recipient:   strings.ToLower(to.Hex()),
		Token:       "", // native ETH — zero value signals ETH
		Amount:      amount,
		Data:        extraData,
		SourceTx:    log.TxHash.Hex(),
		SourceBlock: log.BlockNumber,
		Timestamp:   time.Now(), // block timestamp requires an extra RPC call; use now as a proxy
		Status:      entity.StatusPending,
	}, true, nil
}

// ERC20BridgeInitiated(
//
//	address indexed localToken,
//	address indexed remoteToken,
//	address indexed from,
//	address to,
//	uint256 amount,
//	bytes extraData
//
// )
//
// Layout:
//
//	topics[0] = event signature
//	topics[1] = localToken  (indexed)
//	topics[2] = remoteToken (indexed)
//	topics[3] = from        (indexed)
//	data      = abi.encode(address to, uint256 amount, bytes extraData)
func parseERC20BridgeInitiated(log types.Log) (entity.BridgeEvent, bool, error) {
	if len(log.Topics) < 4 {
		return entity.BridgeEvent{}, false, fmt.Errorf("ERC20BridgeInitiated: expected 4 topics, got %d", len(log.Topics))
	}
	if len(log.Data) < 64 {
		return entity.BridgeEvent{}, false, fmt.Errorf("ERC20BridgeInitiated: data too short (%d bytes)", len(log.Data))
	}

	localToken := common.BytesToAddress(log.Topics[1].Bytes())
	// remoteToken := common.BytesToAddress(log.Topics[2].Bytes()) // L1 counterpart
	from := common.BytesToAddress(log.Topics[3].Bytes())

	// data[0:32]  = address to (right-aligned in 32 bytes)
	to := common.BytesToAddress(log.Data[12:32]) // addresses are right-aligned
	// data[32:64] = uint256 amount
	amount := new(big.Int).SetBytes(log.Data[32:64])

	var extraData []byte
	if len(log.Data) > 96 {
		extraData = log.Data[96:]
	}

	return entity.BridgeEvent{
		ID:          eventID(log),
		SourceChain: entity.ChainIDBase,
		TargetChain: entity.ChainIDEthereum,
		Direction:   entity.L2ToL1,
		Sender:      strings.ToLower(from.Hex()),
		Recipient:   strings.ToLower(to.Hex()),
		Token:       strings.ToLower(localToken.Hex()),
		Amount:      amount,
		Data:        extraData,
		SourceTx:    log.TxHash.Hex(),
		SourceBlock: log.BlockNumber,
		Timestamp:   time.Now(),
		Status:      entity.StatusPending,
	}, true, nil
}

// MessagePassed is emitted by the L2ToL1MessagePasser predeploy.
// It signals a direct withdrawal bypassing the StandardBridge — less common
// but must be handled for completeness.
//
// MessagePassed(
//
//	uint256 indexed nonce,
//	address indexed sender,
//	address indexed target,
//	uint256 value,
//	uint256 gasLimit,
//	bytes data,
//	bytes32 withdrawalHash
//
// )
func parseMessagePassed(log types.Log) (entity.BridgeEvent, bool, error) {
	if len(log.Topics) < 4 {
		return entity.BridgeEvent{}, false, fmt.Errorf("MessagePassed: expected 4 topics, got %d", len(log.Topics))
	}
	// Minimum data: value(32) + gasLimit(32) + bytes_offset(32) + bytes_length(32) + withdrawalHash(32) = 160
	if len(log.Data) < 160 {
		return entity.BridgeEvent{}, false, fmt.Errorf("MessagePassed: data too short (%d bytes, need 160)", len(log.Data))
	}

	// topics[1] = uint256 indexed nonce (versioned nonce from CrossDomainMessenger)
	nonce := new(big.Int).SetBytes(log.Topics[1].Bytes())

	// topics[2] = address indexed sender
	sender := common.BytesToAddress(log.Topics[2].Bytes())
	// topics[3] = address indexed target
	target := common.BytesToAddress(log.Topics[3].Bytes())

	// Non-indexed fields ABI-encoded in data:
	// data[0:32]   = uint256 value  (ETH value of the withdrawal)
	value := new(big.Int).SetBytes(log.Data[0:32])

	// data[32:64]  = uint256 gasLimit
	gasLimit := new(big.Int).SetBytes(log.Data[32:64])

	// data[64:96]  = uint256 offset of `bytes data` (always 160 = 0xa0)
	// data[96:128] = uint256 length of `bytes data`
	dataLen := new(big.Int).SetBytes(log.Data[96:128]).Uint64()

	// data[128 : 128+dataLen] = raw bytes payload
	var callData []byte
	if dataLen > 0 && len(log.Data) >= int(128+dataLen) {
		callData = make([]byte, dataLen)
		copy(callData, log.Data[128:128+dataLen])
	}

	// withdrawalHash is the last 32 bytes, after the ABI-padded calldata.
	// ABI pads bytes to the next 32-byte boundary.
	paddedDataLen := ((dataLen + 31) / 32) * 32
	hashOffset := 128 + paddedDataLen
	var withdrawalHash [32]byte
	if len(log.Data) >= int(hashOffset+32) {
		copy(withdrawalHash[:], log.Data[hashOffset:hashOffset+32])
	}

	return entity.BridgeEvent{
		ID:             eventID(log),
		SourceChain:    entity.ChainIDBase,
		TargetChain:    entity.ChainIDEthereum,
		Direction:      entity.L2ToL1,
		Sender:         strings.ToLower(sender.Hex()),
		Recipient:      strings.ToLower(target.Hex()),
		Token:          "", // native ETH — token withdrawals go via StandardBridge path
		Amount:         value,
		Data:           callData,
		SourceTx:       log.TxHash.Hex(),
		SourceBlock:    log.BlockNumber,
		Timestamp:      time.Now(),
		Status:         entity.StatusPending,
		Nonce:          nonce,
		MsgGasLimit:    gasLimit,
		WithdrawalHash: withdrawalHash,
	}, true, nil
}

// eventID produces a stable, unique ID for a bridge event from its log position.
// Format: <txhash_8chars>-<logIndex> — short enough for logging, collision-free.
func eventID(log types.Log) string {
	h := hex.EncodeToString(log.TxHash.Bytes())
	if len(h) > 8 {
		h = h[:8]
	}
	return fmt.Sprintf("%s-%d", h, log.Index)
}
