// SPDX-License-Identifier: MIT
pragma solidity ^0.8.25;

/// @title IOptimismPortal2
/// @notice Minimal interface for the OptimismPortal2 proxy.
/// @dev Full ABI: https://github.com/ethereum-optimism/optimism/blob/develop/packages/contracts-bedrock/src/L1/OptimismPortal2.sol
interface IOptimismPortal2 {

    struct WithdrawalTransaction {
        uint256 nonce;
        address sender;
        address target;
        uint256 value;
        uint256 gasLimit;
        bytes   data;
    }

    struct OutputRootProof {
        bytes32 version;
        bytes32 stateRoot;
        bytes32 messagePasserStorageRoot;
        bytes32 latestBlockhash;
    }

    /// @notice Submit a withdrawal proof to the portal.
    function proveWithdrawalTransaction(
        WithdrawalTransaction calldata _tx,
        uint256                        _disputeGameIndex,
        OutputRootProof calldata       _outputRootProof,
        bytes[] calldata               _withdrawalProof
    ) external;

    /// @notice Finalize a proven withdrawal after the challenge window.
    function finalizeWithdrawalTransaction(
        WithdrawalTransaction calldata _tx
    ) external;

    /// @notice Returns proof metadata for a (withdrawalHash, prover) pair.
    /// @dev    A non-zero timestamp means the proof was accepted.
    function provenWithdrawals(bytes32 withdrawalHash, address prover)
        external view
        returns (address disputeGameProxy, uint64 timestamp);
}
