package entity

// WithdrawalTx is the WithdrawalTransaction struct passed to OptimismPortal2.
// It must exactly match the Solidity Types.WithdrawalTransaction layout.
// See: optimism/packages/contracts-bedrock/src/libraries/Types.sol
type WithdrawalTx struct {
	Nonce    interface{} // *big.Int — using interface{} avoids import cycle; cast at use site
	Sender   string      // address hex
	Target   string      // address hex
	Value    interface{} // *big.Int
	GasLimit interface{} // *big.Int
	Data     []byte
}

// OutputRootProof is the proof of the L2 output root submitted to the portal.
// Fields must match Types.OutputRootProof exactly.
type OutputRootProof struct {
	Version                  [32]byte
	StateRoot                [32]byte
	MessagePasserStorageRoot [32]byte
	LatestBlockhash          [32]byte
}

// ProvenWithdrawal is the return value of OptimismPortal2.provenWithdrawals().
type ProvenWithdrawal struct {
	DisputeGameProxy string // address
	Timestamp        uint64
}

// DisputeGameStatus mirrors the GameStatus enum in FaultDisputeGame.sol:
//
//	0 = IN_PROGRESS
//	1 = CHALLENGER_WINS
//	2 = DEFENDER_WINS  (valid output root — withdrawal is provable)
type DisputeGameStatus uint8

const (
	GameInProgress    DisputeGameStatus = 0
	GameChallengerWins DisputeGameStatus = 1
	GameDefenderWins  DisputeGameStatus = 2
)

// DisputeGame holds the fields we query from a FaultDisputeGame proxy.
type DisputeGame struct {
	Index       uint64
	Address     string            // proxy address on L1
	GameType    uint32
	Timestamp   uint64
	RootClaim   [32]byte          // output root this game commits to
	L2BlockNum  uint64
	Status      DisputeGameStatus
	ResolvedAt  uint64
}

// WithdrawalProofData bundles everything needed to call proveWithdrawalTransaction.
type WithdrawalProofData struct {
	Tx                WithdrawalTx
	DisputeGameIndex  uint64
	OutputRootProof   OutputRootProof
	WithdrawalProof   [][]byte // Merkle proof nodes from eth_getProof
	WithdrawalHash    [32]byte // keccak256 of the encoded WithdrawalTx
}
