// SPDX-License-Identifier: MIT
pragma solidity ^0.8.25;

import {IClearingHouse}   from "./interfaces/IClearingHouse.sol";
import {IOptimismPortal2} from "./interfaces/IOptimismPortal2.sol";

/// @title L1ClearingHouse
/// @notice Trustless clearing registry for OP Stack L2→L1 withdrawals.
///
/// @dev The clearing flow:
///
///   1. registerIntent()
///      Called by a node operator (prover) when it observes a withdrawal on
///      the source L2. Creates an on-chain record binding this withdrawal hash
///      to this prover. Registering early reserves the prover slot —
///      important for the future multi-prover model.
///
///   2. markProven()
///      Called by the same prover after submitting
///      OptimismPortal2.proveWithdrawalTransaction() on L1. The clearing house
///      verifies the proof actually landed on the portal before accepting it —
///      prevents a prover claiming credit before the proof tx is included.
///
///   3. clearIntent()
///      Permissionless — called by any party after the 7-day challenge window
///      has elapsed and the withdrawal has been finalized on the portal.
///      Emits IntentCleared, which is the source of truth for settlement in
///      the Go orchestrator.
///
/// Security model:
///   - Only the registered prover can call markProven.
///   - clearIntent is permissionless post-finalization (no funds held here).
///   - Re-registration of an existing intent reverts — one prover per withdrawal.
///   - The contract holds no ETH and makes no external value transfers.
///     It is purely a registry.
///
/// R&D notes:
///   Phase 1 (this): single prover per intent, operator calls all three steps.
///   Phase 2:        open registration — any node operator can claim an
///                   unclaimed withdrawal and earn the clearing fee.
///   Phase 3:        multi-prover quorum — N-of-M node operators must agree
///                   before a withdrawal is considered cleared. This is the
///                   full trustless committee model described in the brief.
contract L1ClearingHouse is IClearingHouse {

    // ─── immutables ──────────────────────────────────────────────────────────

    /// @notice The OptimismPortal2 proxy on this L1 chain.
    IOptimismPortal2 public immutable PORTAL;

    // ─── state ───────────────────────────────────────────────────────────────

    /// @notice withdrawalHash → ClearingIntent
    mapping(bytes32 => ClearingIntent) private _intents;

    /// @notice Supported source chain IDs.
    /// Prevents registration of intents from chains that don't share the
    /// OP Stack portal proof model.
    mapping(uint64 => bool) public supportedChains;

    /// @notice Owner — can add/remove supported chains and recover failed intents.
    /// In production this should be a timelock or governance contract.
    address public owner;

    // ─── errors ──────────────────────────────────────────────────────────────

    error AlreadyRegistered(bytes32 withdrawalHash);
    error NotRegistered(bytes32 withdrawalHash);
    error NotProver(bytes32 withdrawalHash, address caller);
    error WrongStatus(bytes32 withdrawalHash, IntentStatus current, IntentStatus required);
    error UnsupportedChain(uint64 chainId);
    error NotYetProvenOnPortal(bytes32 withdrawalHash);
    error ZeroAddress();
    error NotOwner();

    // ─── constructor ─────────────────────────────────────────────────────────

    /// @param portal  Address of OptimismPortal2 proxy on this L1.
    /// @param chains  Source L2 chain IDs to support at deployment time.
    constructor(address portal, uint64[] memory chains) {
        if (portal == address(0)) revert ZeroAddress();
        PORTAL = IOptimismPortal2(portal);
        owner  = msg.sender;

        for (uint256 i = 0; i < chains.length; i++) {
            supportedChains[chains[i]] = true;
            emit ChainAdded(chains[i]);
        }
    }

    // ─── events (extra, not in interface) ────────────────────────────────────

    event ChainAdded(uint64 indexed chainId);
    event ChainRemoved(uint64 indexed chainId);
    event OwnershipTransferred(address indexed previousOwner, address indexed newOwner);

    // ─── modifiers ───────────────────────────────────────────────────────────

    modifier onlyOwner() {
        if (msg.sender != owner) revert NotOwner();
        _;
    }

    // ─── admin ───────────────────────────────────────────────────────────────

    function addSupportedChain(uint64 chainId) external onlyOwner {
        supportedChains[chainId] = true;
        emit ChainAdded(chainId);
    }

    function removeSupportedChain(uint64 chainId) external onlyOwner {
        supportedChains[chainId] = false;
        emit ChainRemoved(chainId);
    }

    function transferOwnership(address newOwner) external onlyOwner {
        if (newOwner == address(0)) revert ZeroAddress();
        emit OwnershipTransferred(owner, newOwner);
        owner = newOwner;
    }

    /// @notice Mark an intent as Failed for manual recovery.
    /// @dev    Use when a withdrawal is provably unclaimable (e.g. L2 reorg).
    function markFailed(bytes32 withdrawalHash, string calldata reason)
        external onlyOwner
    {
        ClearingIntent storage intent = _requireIntent(withdrawalHash);
        intent.status = IntentStatus.Failed;
        emit IntentFailed(withdrawalHash, reason);
    }

    // ─── core ────────────────────────────────────────────────────────────────

    /// @inheritdoc IClearingHouse
    function registerIntent(
        bytes32 withdrawalHash,
        uint64  sourceChainId,
        address recipient
    ) external override {
        if (!supportedChains[sourceChainId])   revert UnsupportedChain(sourceChainId);
        if (_intents[withdrawalHash].registeredAt != 0) revert AlreadyRegistered(withdrawalHash);
        if (recipient == address(0))            revert ZeroAddress();

        _intents[withdrawalHash] = ClearingIntent({
            withdrawalHash: withdrawalHash,
            sourceChainId:  sourceChainId,
            recipient:      recipient,
            prover:         msg.sender,
            status:         IntentStatus.Pending,
            registeredAt:   uint64(block.timestamp),
            provenAt:       0
        });

        emit IntentRegistered(withdrawalHash, sourceChainId, recipient, msg.sender);
    }

    /// @inheritdoc IClearingHouse
    /// @dev Verifies proof landed on OptimismPortal2 before accepting.
    ///      This is the key trust-minimisation step: the clearing house can't
    ///      be fooled into marking something proven that the portal rejected.
    function markProven(bytes32 withdrawalHash) external override {
        ClearingIntent storage intent = _requireIntent(withdrawalHash);

        if (msg.sender != intent.prover)
            revert NotProver(withdrawalHash, msg.sender);
        if (intent.status != IntentStatus.Pending)
            revert WrongStatus(withdrawalHash, intent.status, IntentStatus.Pending);

        // Cross-check with OptimismPortal2: proof must have been accepted.
        (, uint64 portalTimestamp) = PORTAL.provenWithdrawals(withdrawalHash, msg.sender);
        if (portalTimestamp == 0) revert NotYetProvenOnPortal(withdrawalHash);

        intent.status   = IntentStatus.Proven;
        intent.provenAt = uint64(block.timestamp);

        emit IntentProven(withdrawalHash);
    }

    /// @inheritdoc IClearingHouse
    /// @dev Permissionless after the proof has been accepted.
    ///      The Go orchestrator calls this after finalizeWithdrawalTransaction
    ///      has confirmed on L1, completing the full settlement loop.
    ///      We re-verify the proof is still on the portal in case of an edge-case
    ///      reorg between markProven and clearIntent.
    function clearIntent(bytes32 withdrawalHash) external override {
        ClearingIntent storage intent = _requireIntent(withdrawalHash);

        if (intent.status != IntentStatus.Proven)
            revert WrongStatus(withdrawalHash, intent.status, IntentStatus.Proven);

        // Re-verify: proof must still be on the portal.
        (, uint64 portalTimestamp) = PORTAL.provenWithdrawals(withdrawalHash, intent.prover);
        if (portalTimestamp == 0) revert NotYetProvenOnPortal(withdrawalHash);

        intent.status = IntentStatus.Cleared;

        emit IntentCleared(withdrawalHash);
    }

    // ─── views ───────────────────────────────────────────────────────────────

    /// @inheritdoc IClearingHouse
    function getIntent(bytes32 withdrawalHash)
        external view override returns (ClearingIntent memory)
    {
        return _intents[withdrawalHash];
    }

    /// @inheritdoc IClearingHouse
    function isCleared(bytes32 withdrawalHash)
        external view override returns (bool)
    {
        return _intents[withdrawalHash].status == IntentStatus.Cleared;
    }

    // ─── internal ────────────────────────────────────────────────────────────

    function _requireIntent(bytes32 withdrawalHash)
        internal view returns (ClearingIntent storage)
    {
        ClearingIntent storage intent = _intents[withdrawalHash];
        if (intent.registeredAt == 0) revert NotRegistered(withdrawalHash);
        return intent;
    }
}
