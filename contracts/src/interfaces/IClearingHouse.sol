// SPDX-License-Identifier: MIT
pragma solidity ^0.8.25;

/// @title IClearingHouse
/// @notice Interface for the L1 clearing registry.
interface IClearingHouse {

    // ─── types ───────────────────────────────────────────────────────────────

    /// @notice Lifecycle of an on-chain clearing intent.
    /// Pending  → Proven  → Cleared
    ///         ↘ Failed  (at any stage, set by owner for manual recovery)
    enum IntentStatus { Pending, Proven, Cleared, Failed }

    /// @notice On-chain record binding a withdrawal to its prover.
    /// @dev Sized to fit two storage slots:
    ///   slot 0: withdrawalHash (32 bytes)
    ///   slot 1: sourceChainId(8) + registeredAt(8) + provenAt(8) + status(1) + prover(20) = 45 → 2 slots
    struct ClearingIntent {
        bytes32      withdrawalHash;  // keccak256 of abi.encode(WithdrawalTx)
        uint64       sourceChainId;   // e.g. 84532 = Base Sepolia
        address      recipient;       // final recipient on L1
        address      prover;          // node operator who registered this intent
        IntentStatus status;
        uint64       registeredAt;    // block.timestamp at registration
        uint64       provenAt;        // block.timestamp when markProven called
    }

    // ─── events ──────────────────────────────────────────────────────────────

    event IntentRegistered(
        bytes32 indexed withdrawalHash,
        uint64  indexed sourceChainId,
        address         recipient,
        address indexed prover
    );

    event IntentProven(bytes32 indexed withdrawalHash);
    event IntentCleared(bytes32 indexed withdrawalHash);
    event IntentFailed(bytes32 indexed withdrawalHash, string reason);

    // ─── functions ───────────────────────────────────────────────────────────

    function registerIntent(
        bytes32 withdrawalHash,
        uint64  sourceChainId,
        address recipient
    ) external;

    function markProven(bytes32 withdrawalHash) external;

    function clearIntent(bytes32 withdrawalHash) external;

    function getIntent(bytes32 withdrawalHash)
        external view returns (ClearingIntent memory);

    function isCleared(bytes32 withdrawalHash)
        external view returns (bool);
}
