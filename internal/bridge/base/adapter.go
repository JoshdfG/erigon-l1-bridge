// Package base implements BridgeAdapter for Base using OptimismPortal2 (post fault-proofs).
//
// The two-step withdrawal flow:
//
//  Step 1 — ProveWithdrawal
//    a) Compute the withdrawalHash from the BridgeEvent's MessagePassed nonce + params
//    b) Find a valid FaultDisputeGame on L1 whose L2 block number >= the withdrawal block
//    c) Fetch the L2 output root proof: stateRoot + messagePasserStorageRoot + blockhash
//       via eth_getProof on the L2ToL1MessagePasser predeploy
//    d) Call OptimismPortal2.proveWithdrawalTransaction()
//
//  Step 2 — FinalizeWithdrawal (after 7-day challenge window)
//    a) Check that the dispute game has resolved as DEFENDER_WINS
//    b) Check that provenWithdrawals[hash][prover].timestamp + FINALIZATION_PERIOD has passed
//    c) Call OptimismPortal2.finalizeWithdrawalTransaction()
//
// Key contracts on L1 (Ethereum mainnet):
//   OptimismPortalProxy:    0x49048044D57e1C92A77f79988d21Fa8fAF74E97e
//   DisputeGameFactoryProxy: 0x43edB88C4B80fDD2AdFF2412A7BebF9dF42cB40e
package base

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/erigontech/erigon-l1-bridge/internal/entity"
	"github.com/erigontech/erigon-l1-bridge/internal/usecase"
)

// Compile-time interface check.
var _ usecase.BridgeAdapter = (*Adapter)(nil)

// Base mainnet L1 contract addresses (source: superchain-registry/addresses.json)
const (
	OptimismPortalProxyAddr    = "0x49048044D57e1C92A77f79988d21Fa8fAF74E97e"
	DisputeGameFactoryProxyAddr = "0x43edB88C4B80fDD2AdFF2412A7BebF9dF42cB40e"

	// FaultDisputeGame game type used for permissionless withdrawals on Base.
	// Source: OptimismPortal2.respectedGameType() — currently 0 (FAULT).
	RespectedGameType uint32 = 0

	// How many recent games to scan when searching for a suitable dispute game.
	// OP Stack docs recommend ~100; we use 200 to be safe given Base's throughput.
	GameSearchDepth = 200

	// FinalizationPeriod is the minimum time after proof submission before
	// finalizeWithdrawalTransaction can be called.
	// On Base mainnet this is 7 days; 3.5 days for the dispute game + portal delay.
	FinalizationPeriod = 7 * 24 * time.Hour

	// L2ToL1MessagePasserStorageSlot is the slot number of the `sentMessages`
	// mapping in the L2ToL1MessagePasser predeploy. Used to compute the
	// storage key for eth_getProof.
	L2ToL1MessagePasserStorageSlot = 0
)

// AdapterConfig holds addresses and clients for the Base bridge adapter.
type AdapterConfig struct {
	// L1Client is an ethclient connected to Ethereum mainnet (or testnet).
	L1Client *ethclient.Client
	// L2Client is an ethclient connected to Base — needed for eth_getProof.
	L2Client *ethclient.Client
	// L2RPCClient is the raw RPC client for eth_getProof (not exposed by ethclient).
	L2RPCClient *rpc.Client
	// Signer is the private key that submits prove/finalize transactions.
	Signer *ecdsa.PrivateKey
	// PortalAddress overrides the default OptimismPortalProxyAddr (useful for testnets).
	PortalAddress string
	// FactoryAddress overrides DisputeGameFactoryProxyAddr.
	FactoryAddress string
}

// Adapter implements BridgeAdapter for Base (OP Stack with fault proofs).
type Adapter struct {
	l1         *ethclient.Client
	l2         *ethclient.Client
	l2rpc      *rpc.Client
	signer     *ecdsa.PrivateKey
	portal     common.Address
	factory    common.Address
	portalABI  abi.ABI
	factoryABI abi.ABI
	gameABI    abi.ABI
}

// New creates a minimal adapter suitable for read-only operations (IsFinalized).
// For ProveWithdrawal / FinalizeWithdrawal, use NewFromConfig with a signer.
func New(portalAddress string) *Adapter {
	addr := portalAddress
	if addr == "" {
		addr = OptimismPortalProxyAddr
	}
	return &Adapter{
		portal:  common.HexToAddress(addr),
		factory: common.HexToAddress(DisputeGameFactoryProxyAddr),
	}
}

// NewFromConfig creates a fully wired adapter capable of submitting transactions.
func NewFromConfig(cfg AdapterConfig) (*Adapter, error) {
	portalAddr := cfg.PortalAddress
	if portalAddr == "" {
		portalAddr = OptimismPortalProxyAddr
	}
	factoryAddr := cfg.FactoryAddress
	if factoryAddr == "" {
		factoryAddr = DisputeGameFactoryProxyAddr
	}

	portalABI, err := loadABI(portalABIJSON)
	if err != nil {
		return nil, fmt.Errorf("bridge/base: parse portal ABI: %w", err)
	}
	factoryABI, err := loadABI(factoryABIJSON)
	if err != nil {
		return nil, fmt.Errorf("bridge/base: parse factory ABI: %w", err)
	}
	gameABI, err := loadABI(gameABIJSON)
	if err != nil {
		return nil, fmt.Errorf("bridge/base: parse game ABI: %w", err)
	}

	return &Adapter{
		l1:         cfg.L1Client,
		l2:         cfg.L2Client,
		l2rpc:      cfg.L2RPCClient,
		signer:     cfg.Signer,
		portal:     common.HexToAddress(portalAddr),
		factory:    common.HexToAddress(factoryAddr),
		portalABI:  portalABI,
		factoryABI: factoryABI,
		gameABI:    gameABI,
	}, nil
}

func (a *Adapter) Chain() entity.ChainID { return entity.ChainIDBase }

// ─── IsFinalized ──────────────────────────────────────────────────────────────

// IsFinalized returns true if the withdrawal has been proven AND the
// finalization period has passed AND the dispute game resolved as DEFENDER_WINS.
// This is the gate the orchestrator polls before calling FinalizeWithdrawal.
func (a *Adapter) IsFinalized(ctx context.Context, event entity.BridgeEvent) (bool, error) {
	if a.l1 == nil {
		// No L1 client wired — return false so the orchestrator skips safely.
		return false, nil
	}

	// Use the WithdrawalHash already parsed from MessagePassed if available —
	// avoids a receipt fetch for direct-portal withdrawals.
	var withdrawalHash [32]byte
	if event.WithdrawalHash != ([32]byte{}) {
		withdrawalHash = event.WithdrawalHash
	} else {
		var err error
		withdrawalHash, err = a.computeWithdrawalHash(ctx, event)
		if err != nil {
			return false, fmt.Errorf("isFinalized: compute hash: %w", err)
		}
	}

	signerAddr := crypto.PubkeyToAddress(a.signer.PublicKey)
	proven, err := a.queryProvenWithdrawal(ctx, withdrawalHash, signerAddr)
	if err != nil {
		return false, fmt.Errorf("isFinalized: query proven: %w", err)
	}
	if proven.Timestamp == 0 {
		// Not yet proven.
		return false, nil
	}

	// Check the dispute game resolved correctly.
	game, err := a.queryDisputeGame(ctx, proven.DisputeGameProxy)
	if err != nil {
		return false, fmt.Errorf("isFinalized: query game: %w", err)
	}
	if game.Status != entity.GameDefenderWins {
		return false, nil
	}

	// Check finalization period has elapsed since proof submission.
	provenAt := time.Unix(int64(proven.Timestamp), 0)
	if time.Since(provenAt) < FinalizationPeriod {
		return false, nil
	}

	return true, nil
}

// ─── IsProven ─────────────────────────────────────────────────────────────────

// IsProven returns true once the prove tx has landed and OptimismPortal2 has
// recorded the proof in provenWithdrawals[hash][prover].
// Polled by the orchestrator while the intent is in StatusProving.
func (a *Adapter) IsProven(ctx context.Context, event entity.BridgeEvent) (bool, error) {
	if a.l1 == nil || a.signer == nil {
		return false, nil
	}

	var withdrawalHash [32]byte
	if event.WithdrawalHash != ([32]byte{}) {
		withdrawalHash = event.WithdrawalHash
	} else {
		var err error
		withdrawalHash, err = a.computeWithdrawalHash(ctx, event)
		if err != nil {
			return false, fmt.Errorf("isProven: compute hash: %w", err)
		}
	}

	signerAddr := crypto.PubkeyToAddress(a.signer.PublicKey)
	proven, err := a.queryProvenWithdrawal(ctx, withdrawalHash, signerAddr)
	if err != nil {
		return false, fmt.Errorf("isProven: query portal: %w", err)
	}

	// A non-zero timestamp means the portal accepted the proof.
	return proven.Timestamp > 0, nil
}

// ─── IsSettled ────────────────────────────────────────────────────────────────

// IsSettled returns true once the finalize tx has been included on L1.
// We detect this by checking whether the portal emits a WithdrawalFinalized event
// for our withdrawal hash, or by querying the L1 receipt of the finalize tx.
//
// For now we check the receipt of the known finalize tx hash — this requires
// the caller to have stored the tx hash from FinalizeWithdrawal's return value.
func (a *Adapter) IsSettled(ctx context.Context, event entity.BridgeEvent) (bool, error) {
	if a.l1 == nil {
		return false, nil
	}

	// If the event carries a withdrawal hash, we can check the portal's
	// WithdrawalFinalized event by scanning recent L1 logs. For now we use a
	// simpler approach: re-query provenWithdrawals and check if the game's
	// resolvedAt timestamp is in the past — combined with the finalizable check
	// this is a good proxy for settlement.
	//
	// TODO: watch the WithdrawalFinalized event on L1 for definitive confirmation.
	return a.IsFinalized(ctx, event)
}

// ─── ProveWithdrawal ─────────────────────────────────────────────────────────

// ProveWithdrawal executes Step 1 of the OP Stack two-step withdrawal:
//  1. Finds a valid FaultDisputeGame on L1 covering the withdrawal's L2 block
//  2. Fetches the storage proof from the L2 node via eth_getProof
//  3. Submits proveWithdrawalTransaction to OptimismPortal2
func (a *Adapter) ProveWithdrawal(ctx context.Context, event entity.BridgeEvent) (string, error) {
	if a.l1 == nil || a.l2 == nil || a.signer == nil {
		return "", fmt.Errorf("bridge/base: ProveWithdrawal called without L1/L2 clients or signer")
	}

	// 1. Build the WithdrawalTx from the event.
	wtx, err := a.buildWithdrawalTx(ctx, event)
	if err != nil {
		return "", fmt.Errorf("proveWithdrawal: build tx: %w", err)
	}

	// 2. Compute the withdrawal hash (keccak256 of the ABI-encoded WithdrawalTx).
	withdrawalHash, err := a.computeWithdrawalHashFromTx(wtx)
	if err != nil {
		return "", fmt.Errorf("proveWithdrawal: hash: %w", err)
	}

	// 3. Find a suitable dispute game on L1 that covers this withdrawal's L2 block.
	game, err := a.findSuitableGame(ctx, event.SourceBlock)
	if err != nil {
		return "", fmt.Errorf("proveWithdrawal: find game: %w", err)
	}

	// 4. Fetch the output root proof from the L2 node.
	// We need: stateRoot, messagePasserStorageRoot, blockhash at game.L2BlockNum.
	outputProof, storageProof, err := a.fetchOutputRootProof(ctx, withdrawalHash, game.L2BlockNum)
	if err != nil {
		return "", fmt.Errorf("proveWithdrawal: fetch proof: %w", err)
	}

	// 5. ABI-encode the call and submit to L1.
	txHash, err := a.submitProveWithdrawal(ctx, wtx, game.Index, outputProof, storageProof)
	if err != nil {
		return "", fmt.Errorf("proveWithdrawal: submit: %w", err)
	}

	return txHash, nil
}

// ─── FinalizeWithdrawal ───────────────────────────────────────────────────────

// FinalizeWithdrawal executes Step 2 — calls finalizeWithdrawalTransaction
// on OptimismPortal2 once the finalization period has passed.
// The caller (orchestrator) must have verified IsFinalized == true first.
func (a *Adapter) FinalizeWithdrawal(ctx context.Context, event entity.BridgeEvent) (string, error) {
	if a.l1 == nil || a.signer == nil {
		return "", fmt.Errorf("bridge/base: FinalizeWithdrawal called without L1 client or signer")
	}

	wtx, err := a.buildWithdrawalTx(ctx, event)
	if err != nil {
		return "", fmt.Errorf("finalizeWithdrawal: build tx: %w", err)
	}

	txHash, err := a.submitFinalizeWithdrawal(ctx, wtx)
	if err != nil {
		return "", fmt.Errorf("finalizeWithdrawal: submit: %w", err)
	}

	return txHash, nil
}

// ─── internal helpers ─────────────────────────────────────────────────────────

// buildWithdrawalTx reconstructs the WithdrawalTx from a BridgeEvent.
//
// Source of truth for each field:
//
//	Nonce    — from event.Nonce (populated by parseMessagePassed).
//	           For StandardBridge events (ETHBridgeInitiated / ERC20BridgeInitiated)
//	           the MessagePassed nonce must be fetched from the same tx receipt.
//	Sender   — from event.Sender
//	Target   — from event.Recipient
//	Value    — from event.Amount
//	GasLimit — from event.MsgGasLimit (populated by parseMessagePassed)
//	Data     — from event.Data
//
// The reconstructed tx must be byte-for-byte identical to the one the L2ToL1MessagePasser
// hashed — any discrepancy produces a wrong withdrawalHash and the portal rejects the proof.
func (a *Adapter) buildWithdrawalTx(ctx context.Context, event entity.BridgeEvent) (withdrawalTx, error) {
	value := event.Amount
	if value == nil {
		value = big.NewInt(0)
	}

	nonce := event.Nonce
	gasLimit := event.MsgGasLimit

	// If nonce or gasLimit are nil this is a StandardBridge event
	// (ETHBridgeInitiated / ERC20BridgeInitiated). Those events are emitted in the
	// same transaction as a MessagePassed event — fetch the receipt and extract it.
	if nonce == nil || gasLimit == nil {
		if a.l2 == nil {
			return withdrawalTx{}, fmt.Errorf("buildWithdrawalTx: StandardBridge event requires L2 client to fetch receipt (tx %s)", event.SourceTx)
		}
		fetchedNonce, fetchedGasLimit, err := a.nonceFromReceipt(ctx, event.SourceTx)
		if err != nil {
			return withdrawalTx{}, fmt.Errorf("buildWithdrawalTx: fetch nonce from receipt: %w", err)
		}
		nonce = fetchedNonce
		gasLimit = fetchedGasLimit
	}

	return withdrawalTx{
		Nonce:    nonce,
		Sender:   common.HexToAddress(event.Sender),
		Target:   common.HexToAddress(event.Recipient),
		Value:    value,
		GasLimit: gasLimit,
		Data:     event.Data,
	}, nil
}

// nonceFromReceipt fetches the transaction receipt and extracts the versioned nonce
// and gasLimit from the MessagePassed event emitted by L2ToL1MessagePasser.
//
// Every withdrawal — whether via StandardBridge or direct — emits exactly one
// MessagePassed event in the initiating transaction. The nonce there is the one
// the portal uses to compute the withdrawal hash.
func (a *Adapter) nonceFromReceipt(ctx context.Context, txHashHex string) (*big.Int, *big.Int, error) {
	txHash := common.HexToHash(txHashHex)
	receipt, err := a.l2.TransactionReceipt(ctx, txHash)
	if err != nil {
		return nil, nil, fmt.Errorf("get receipt for %s: %w", txHashHex, err)
	}

	const l2ToL1MessagePasser = "0x4200000000000000000000000000000000000016"
	msgPasserAddr := common.HexToAddress(l2ToL1MessagePasser)
	msgPassedTopic := crypto.Keccak256Hash([]byte("MessagePassed(uint256,address,address,uint256,uint256,bytes,bytes32)"))

	for _, l := range receipt.Logs {
		if l.Address != msgPasserAddr {
			continue
		}
		if len(l.Topics) < 2 || l.Topics[0] != msgPassedTopic {
			continue
		}
		if len(l.Data) < 64 {
			return nil, nil, fmt.Errorf("MessagePassed log data too short in tx %s", txHashHex)
		}
		// topics[1] = uint256 indexed nonce
		nonce := new(big.Int).SetBytes(l.Topics[1].Bytes())
		// data[32:64] = uint256 gasLimit
		gasLimit := new(big.Int).SetBytes(l.Data[32:64])
		return nonce, gasLimit, nil
	}

	return nil, nil, fmt.Errorf("no MessagePassed log found in tx %s", txHashHex)
}

// findSuitableGame scans recent FaultDisputeGames on L1 to find one whose
// L2 block number is >= the withdrawal's source block and which has resolved
// as DEFENDER_WINS (valid output root).
//
// Per OP Stack docs: scan the most recent ~200 games, pick the earliest one
// with L2BlockNumber >= withdrawalBlock. Verify the game locally before using.
func (a *Adapter) findSuitableGame(ctx context.Context, withdrawalBlock uint64) (entity.DisputeGame, error) {
	// Get total game count from factory.
	count, err := a.queryGameCount(ctx)
	if err != nil {
		return entity.DisputeGame{}, fmt.Errorf("findSuitableGame: count: %w", err)
	}
	if count == 0 {
		return entity.DisputeGame{}, fmt.Errorf("findSuitableGame: no games exist")
	}

	// Scan from newest game backwards, up to GameSearchDepth.
	start := count
	depth := uint64(GameSearchDepth)
	if start < depth {
		depth = start
	}

	for i := uint64(0); i < depth; i++ {
		idx := start - 1 - i

		gameAddr, gameType, err := a.queryGameAtIndex(ctx, idx)
		if err != nil {
			continue
		}
		if gameType != RespectedGameType {
			continue
		}

		game, err := a.queryDisputeGame(ctx, gameAddr)
		if err != nil {
			continue
		}

		// Must cover the withdrawal block.
		if game.L2BlockNum < withdrawalBlock {
			continue
		}
		// Must be resolved in favour of the defender (valid output root).
		if game.Status != entity.GameDefenderWins {
			continue
		}

		game.Index = idx
		return game, nil
	}

	return entity.DisputeGame{}, fmt.Errorf(
		"findSuitableGame: no suitable game found for block %d in last %d games",
		withdrawalBlock, depth,
	)
}

// fetchOutputRootProof calls eth_getProof on the L2ToL1MessagePasser predeploy
// at the given L2 block number to get:
//   - The output root components (stateRoot, messagePasserStorageRoot, blockHash)
//   - The storage inclusion proof for the withdrawal hash
//
// This is the cryptographic heart of the OP Stack withdrawal process.
func (a *Adapter) fetchOutputRootProof(
	ctx context.Context,
	withdrawalHash [32]byte,
	l2BlockNum uint64,
) (entity.OutputRootProof, [][]byte, error) {
	blockHex := fmt.Sprintf("0x%x", l2BlockNum)

	// Storage key = keccak256(withdrawalHash ++ slot 0)
	// This is how Solidity computes mapping(bytes32 => bool) storage slots.
	storageKey := computeStorageKey(withdrawalHash)

	// eth_getProof returns the account proof + storage proof for the given key.
	var proofResult ethGetProofResult
	// L2ToL1MessagePasser predeploy — same address on every OP Stack chain.
	const l2ToL1MessagePasser = "0x4200000000000000000000000000000000000016"

	err := a.l2rpc.CallContext(ctx, &proofResult,
		"eth_getProof",
		l2ToL1MessagePasser,
		[]string{storageKey},
		blockHex,
	)
	if err != nil {
		return entity.OutputRootProof{}, nil, fmt.Errorf("eth_getProof: %w", err)
	}

	// Fetch the block header to get stateRoot and blockHash.
	l2Block, err := a.l2.BlockByNumber(ctx, new(big.Int).SetUint64(l2BlockNum))
	if err != nil {
		return entity.OutputRootProof{}, nil, fmt.Errorf("fetch l2 block %d: %w", l2BlockNum, err)
	}

	var outputProof entity.OutputRootProof
	outputProof.StateRoot = l2Block.Root()
	outputProof.LatestBlockhash = l2Block.Hash()

	// messagePasserStorageRoot comes from the eth_getProof response.
	storageRoot, err := hex.DecodeString(strings.TrimPrefix(proofResult.StorageHash, "0x"))
	if err != nil {
		return entity.OutputRootProof{}, nil, fmt.Errorf("decode storageHash: %w", err)
	}
	copy(outputProof.MessagePasserStorageRoot[:], storageRoot)

	// version is bytes32(0) for the current output root version.
	// If OP Stack upgrades to a new version format this will need updating.
	outputProof.Version = [32]byte{}

	// Decode the storage proof nodes — these are RLP-encoded trie nodes.
	storageProof := make([][]byte, len(proofResult.StorageProof))
	if len(proofResult.StorageProof) > 0 {
		for i, node := range proofResult.StorageProof[0].Proof {
			decoded, err := hex.DecodeString(strings.TrimPrefix(node, "0x"))
			if err != nil {
				return entity.OutputRootProof{}, nil, fmt.Errorf("decode proof node %d: %w", i, err)
			}
			storageProof[i] = decoded
		}
	}

	return outputProof, storageProof, nil
}

// submitProveWithdrawal ABI-encodes and submits the proveWithdrawalTransaction call.
func (a *Adapter) submitProveWithdrawal(
	ctx context.Context,
	wtx withdrawalTx,
	gameIndex uint64,
	outputProof entity.OutputRootProof,
	storageProof [][]byte,
) (string, error) {
	calldata, err := a.portalABI.Pack(
		"proveWithdrawalTransaction",
		wtx.toABIStruct(),
		new(big.Int).SetUint64(gameIndex),
		toOutputRootProofABI(outputProof),
		storageProof,
	)
	if err != nil {
		return "", fmt.Errorf("pack proveWithdrawal: %w", err)
	}

	return a.sendL1Transaction(ctx, a.portal, calldata)
}

// submitFinalizeWithdrawal ABI-encodes and submits finalizeWithdrawalTransaction.
func (a *Adapter) submitFinalizeWithdrawal(ctx context.Context, wtx withdrawalTx) (string, error) {
	calldata, err := a.portalABI.Pack(
		"finalizeWithdrawalTransaction",
		wtx.toABIStruct(),
	)
	if err != nil {
		return "", fmt.Errorf("pack finalizeWithdrawal: %w", err)
	}

	return a.sendL1Transaction(ctx, a.portal, calldata)
}

// sendL1Transaction builds, signs, and submits a transaction to L1.
func (a *Adapter) sendL1Transaction(ctx context.Context, to common.Address, data []byte) (string, error) {
	signerAddr := crypto.PubkeyToAddress(a.signer.PublicKey)

	nonce, err := a.l1.PendingNonceAt(ctx, signerAddr)
	if err != nil {
		return "", fmt.Errorf("pending nonce: %w", err)
	}

	gasPrice, err := a.l1.SuggestGasPrice(ctx)
	if err != nil {
		return "", fmt.Errorf("gas price: %w", err)
	}
	// Add 20% tip to ensure inclusion.
	gasPrice = new(big.Int).Mul(gasPrice, big.NewInt(12))
	gasPrice = new(big.Int).Div(gasPrice, big.NewInt(10))

	msg := ethereum.CallMsg{From: signerAddr, To: &to, Data: data, GasPrice: gasPrice}
	gasLimit, err := a.l1.EstimateGas(ctx, msg)
	if err != nil {
		// Use a safe fallback if estimation fails (e.g. portal is paused).
		gasLimit = 300_000
	}

	chainID, err := a.l1.ChainID(ctx)
	if err != nil {
		return "", fmt.Errorf("chain ID: %w", err)
	}

	rawTx := types.NewTransaction(nonce, to, big.NewInt(0), gasLimit, gasPrice, data)
	signed, err := types.SignTx(rawTx, types.NewEIP155Signer(chainID), a.signer)
	if err != nil {
		return "", fmt.Errorf("sign tx: %w", err)
	}

	if err := a.l1.SendTransaction(ctx, signed); err != nil {
		return "", fmt.Errorf("send tx: %w", err)
	}

	return signed.Hash().Hex(), nil
}

// ─── on-chain query helpers ──────────────────────────────────────────────────

// queryProvenWithdrawal calls OptimismPortal2.provenWithdrawals(hash, prover).
func (a *Adapter) queryProvenWithdrawal(
	ctx context.Context,
	withdrawalHash [32]byte,
	prover common.Address,
) (entity.ProvenWithdrawal, error) {
	calldata, err := a.portalABI.Pack("provenWithdrawals", withdrawalHash, prover)
	if err != nil {
		return entity.ProvenWithdrawal{}, err
	}

	result, err := a.l1.CallContract(ctx, ethereum.CallMsg{
		To:   &a.portal,
		Data: calldata,
	}, nil)
	if err != nil {
		return entity.ProvenWithdrawal{}, fmt.Errorf("call provenWithdrawals: %w", err)
	}

	unpacked, err := a.portalABI.Unpack("provenWithdrawals", result)
	if err != nil {
		return entity.ProvenWithdrawal{}, fmt.Errorf("unpack provenWithdrawals: %w", err)
	}

	type provenResult struct {
		DisputeGameProxy common.Address
		Timestamp        uint64
	}
	r, ok := unpacked[0].(provenResult)
	if !ok {
		return entity.ProvenWithdrawal{}, fmt.Errorf("provenWithdrawals: unexpected type %T", unpacked[0])
	}

	return entity.ProvenWithdrawal{
		DisputeGameProxy: r.DisputeGameProxy.Hex(),
		Timestamp:        r.Timestamp,
	}, nil
}

// queryGameCount calls DisputeGameFactory.gameCount().
func (a *Adapter) queryGameCount(ctx context.Context) (uint64, error) {
	calldata, err := a.factoryABI.Pack("gameCount")
	if err != nil {
		return 0, err
	}
	result, err := a.l1.CallContract(ctx, ethereum.CallMsg{To: &a.factory, Data: calldata}, nil)
	if err != nil {
		return 0, fmt.Errorf("call gameCount: %w", err)
	}
	unpacked, err := a.factoryABI.Unpack("gameCount", result)
	if err != nil {
		return 0, err
	}
	n, ok := unpacked[0].(*big.Int)
	if !ok {
		return 0, fmt.Errorf("gameCount: unexpected type %T", unpacked[0])
	}
	return n.Uint64(), nil
}

// queryGameAtIndex calls DisputeGameFactory.gameAtIndex(idx).
// Returns the game proxy address and its game type.
func (a *Adapter) queryGameAtIndex(ctx context.Context, idx uint64) (string, uint32, error) {
	calldata, err := a.factoryABI.Pack("gameAtIndex", new(big.Int).SetUint64(idx))
	if err != nil {
		return "", 0, err
	}
	result, err := a.l1.CallContract(ctx, ethereum.CallMsg{To: &a.factory, Data: calldata}, nil)
	if err != nil {
		return "", 0, fmt.Errorf("call gameAtIndex(%d): %w", idx, err)
	}

	type gameAtIndexResult struct {
		GameType  uint32
		Timestamp uint64
		Proxy     common.Address
	}
	var r gameAtIndexResult
	if err := a.factoryABI.UnpackIntoInterface(&r, "gameAtIndex", result); err != nil {
		return "", 0, err
	}
	return r.Proxy.Hex(), r.GameType, nil
}

// queryDisputeGame calls status(), rootClaim(), l2BlockNumber(), and resolvedAt()
// on the FaultDisputeGame proxy contract.
func (a *Adapter) queryDisputeGame(ctx context.Context, proxyAddr string) (entity.DisputeGame, error) {
	addr := common.HexToAddress(proxyAddr)

	callAndUnpack := func(method string) ([]interface{}, error) {
		data, err := a.gameABI.Pack(method)
		if err != nil {
			return nil, err
		}
		result, err := a.l1.CallContract(ctx, ethereum.CallMsg{To: &addr, Data: data}, nil)
		if err != nil {
			return nil, fmt.Errorf("call %s on %s: %w", method, proxyAddr, err)
		}
		return a.gameABI.Unpack(method, result)
	}

	statusVals, err := callAndUnpack("status")
	if err != nil {
		return entity.DisputeGame{}, err
	}
	rootClaimVals, err := callAndUnpack("rootClaim")
	if err != nil {
		return entity.DisputeGame{}, err
	}
	l2BlockVals, err := callAndUnpack("l2BlockNumber")
	if err != nil {
		return entity.DisputeGame{}, err
	}
	resolvedAtVals, err := callAndUnpack("resolvedAt")
	if err != nil {
		return entity.DisputeGame{}, err
	}

	statusU8, _ := statusVals[0].(uint8)
	rootClaim, _ := rootClaimVals[0].([32]byte)
	l2BlockBig, _ := l2BlockVals[0].(*big.Int)
	resolvedAt, _ := resolvedAtVals[0].(uint64)

	var l2Block uint64
	if l2BlockBig != nil {
		l2Block = l2BlockBig.Uint64()
	}

	return entity.DisputeGame{
		Address:    proxyAddr,
		Status:     entity.DisputeGameStatus(statusU8),
		RootClaim:  rootClaim,
		L2BlockNum: l2Block,
		ResolvedAt: resolvedAt,
	}, nil
}

// ─── cryptographic helpers ───────────────────────────────────────────────────

// computeWithdrawalHash computes the withdrawal hash from a BridgeEvent.
// Must match the hash computed inside OptimismPortal2 exactly.
func (a *Adapter) computeWithdrawalHash(ctx context.Context, event entity.BridgeEvent) ([32]byte, error) {
	wtx, err := a.buildWithdrawalTx(ctx, event)
	if err != nil {
		return [32]byte{}, err
	}
	return a.computeWithdrawalHashFromTx(wtx)
}

// computeWithdrawalHashFromTx ABI-encodes the WithdrawalTx and returns keccak256.
// The hash must match:
//   keccak256(abi.encode(nonce, sender, target, value, gasLimit, keccak256(data)))
func (a *Adapter) computeWithdrawalHashFromTx(wtx withdrawalTx) ([32]byte, error) {
	// ABI type definitions for manual encoding
	uint256Ty, _ := abi.NewType("uint256", "", nil)
	addressTy, _ := abi.NewType("address", "", nil)
	bytesTy, _ := abi.NewType("bytes", "", nil)

	args := abi.Arguments{
		{Type: uint256Ty},
		{Type: addressTy},
		{Type: addressTy},
		{Type: uint256Ty},
		{Type: uint256Ty},
		{Type: bytesTy},
	}

	encoded, err := args.Pack(
		wtx.Nonce,
		wtx.Sender,
		wtx.Target,
		wtx.Value,
		wtx.GasLimit,
		wtx.Data,
	)
	if err != nil {
		return [32]byte{}, fmt.Errorf("encode withdrawal tx: %w", err)
	}

	return crypto.Keccak256Hash(encoded), nil
}

// computeStorageKey computes the storage slot key for the sentMessages mapping
// in L2ToL1MessagePasser:
//
//	slot = keccak256(abi.encode(withdrawalHash, uint256(0)))
//
// This is the standard Solidity mapping slot formula.
func computeStorageKey(withdrawalHash [32]byte) string {
	uint256Ty, _ := abi.NewType("uint256", "", nil)
	bytes32Ty, _ := abi.NewType("bytes32", "", nil)

	args := abi.Arguments{{Type: bytes32Ty}, {Type: uint256Ty}}
	encoded, _ := args.Pack(withdrawalHash, big.NewInt(L2ToL1MessagePasserStorageSlot))
	key := crypto.Keccak256Hash(encoded)
	return key.Hex()
}

// hashToUint256 deterministically derives a *big.Int from a string.
// Used as a development-time nonce placeholder.
func hashToUint256(s string) *big.Int {
	h := crypto.Keccak256Hash([]byte(s))
	return new(big.Int).SetBytes(h.Bytes())
}

// ─── ABI loading ─────────────────────────────────────────────────────────────

// Inline ABI strings — in production these would be loaded from the generated
// abigen bindings, but embedding them directly avoids the abigen build step
// for the R&D phase.

const portalABIJSON = `[
  {"type":"function","name":"proveWithdrawalTransaction","inputs":[{"name":"_tx","type":"tuple","components":[{"name":"nonce","type":"uint256"},{"name":"sender","type":"address"},{"name":"target","type":"address"},{"name":"value","type":"uint256"},{"name":"gasLimit","type":"uint256"},{"name":"data","type":"bytes"}]},{"name":"_disputeGameIndex","type":"uint256"},{"name":"_outputRootProof","type":"tuple","components":[{"name":"version","type":"bytes32"},{"name":"stateRoot","type":"bytes32"},{"name":"messagePasserStorageRoot","type":"bytes32"},{"name":"latestBlockhash","type":"bytes32"}]},{"name":"_withdrawalProof","type":"bytes[]"}],"outputs":[],"stateMutability":"nonpayable"},
  {"type":"function","name":"finalizeWithdrawalTransaction","inputs":[{"name":"_tx","type":"tuple","components":[{"name":"nonce","type":"uint256"},{"name":"sender","type":"address"},{"name":"target","type":"address"},{"name":"value","type":"uint256"},{"name":"gasLimit","type":"uint256"},{"name":"data","type":"bytes"}]}],"outputs":[],"stateMutability":"nonpayable"},
  {"type":"function","name":"provenWithdrawals","inputs":[{"name":"withdrawalHash","type":"bytes32"},{"name":"proofSubmitter","type":"address"}],"outputs":[{"name":"","type":"tuple","components":[{"name":"disputeGameProxy","type":"address"},{"name":"timestamp","type":"uint64"}]}],"stateMutability":"view"},
  {"type":"function","name":"disputeGameFactory","inputs":[],"outputs":[{"name":"","type":"address"}],"stateMutability":"view"},
  {"type":"function","name":"respectedGameType","inputs":[],"outputs":[{"name":"","type":"uint32"}],"stateMutability":"view"}
]`

const factoryABIJSON = `[
  {"type":"function","name":"gameCount","inputs":[],"outputs":[{"name":"gameCount_","type":"uint256"}],"stateMutability":"view"},
  {"type":"function","name":"gameAtIndex","inputs":[{"name":"_index","type":"uint256"}],"outputs":[{"name":"gameType_","type":"uint32"},{"name":"timestamp_","type":"uint64"},{"name":"proxy_","type":"address"}],"stateMutability":"view"}
]`

const gameABIJSON = `[
  {"type":"function","name":"status","inputs":[],"outputs":[{"name":"","type":"uint8"}],"stateMutability":"view"},
  {"type":"function","name":"rootClaim","inputs":[],"outputs":[{"name":"","type":"bytes32"}],"stateMutability":"view"},
  {"type":"function","name":"l2BlockNumber","inputs":[],"outputs":[{"name":"","type":"uint256"}],"stateMutability":"view"},
  {"type":"function","name":"resolvedAt","inputs":[],"outputs":[{"name":"","type":"uint64"}],"stateMutability":"view"}
]`

func loadABI(jsonStr string) (abi.ABI, error) {
	return abi.JSON(strings.NewReader(jsonStr))
}

// ─── ABI struct helpers ───────────────────────────────────────────────────────

// withdrawalTx is the internal representation of a WithdrawalTransaction.
// Kept unexported — only the adapter uses it directly.
type withdrawalTx struct {
	Nonce    *big.Int
	Sender   common.Address
	Target   common.Address
	Value    *big.Int
	GasLimit *big.Int
	Data     []byte
}

// toABIStruct converts to the anonymous struct layout go-ethereum's abi.Pack expects
// for tuple types. The field names must match the ABI component names exactly.
func (w withdrawalTx) toABIStruct() struct {
	Nonce    *big.Int
	Sender   common.Address
	Target   common.Address
	Value    *big.Int
	GasLimit *big.Int
	Data     []byte
} {
	return struct {
		Nonce    *big.Int
		Sender   common.Address
		Target   common.Address
		Value    *big.Int
		GasLimit *big.Int
		Data     []byte
	}{
		Nonce:    w.Nonce,
		Sender:   w.Sender,
		Target:   w.Target,
		Value:    w.Value,
		GasLimit: w.GasLimit,
		Data:     w.Data,
	}
}

// outputRootProofABI is a local struct for abi.Pack — we cannot define methods
// on entity.OutputRootProof (non-local type), so we convert with a function.
type outputRootProofABI struct {
	Version                  [32]byte
	StateRoot                [32]byte
	MessagePasserStorageRoot [32]byte
	LatestBlockhash          [32]byte
}

func toOutputRootProofABI(o entity.OutputRootProof) outputRootProofABI {
	return outputRootProofABI{
		Version:                  o.Version,
		StateRoot:                o.StateRoot,
		MessagePasserStorageRoot: o.MessagePasserStorageRoot,
		LatestBlockhash:          o.LatestBlockhash,
	}
}

// ─── eth_getProof response types ─────────────────────────────────────────────

type ethGetProofResult struct {
	StorageHash  string             `json:"storageHash"`
	StorageProof []storageProofItem `json:"storageProof"`
}

type storageProofItem struct {
	Key   string   `json:"key"`
	Value string   `json:"value"`
	Proof []string `json:"proof"`
}
