// SPDX-License-Identifier: MIT
pragma solidity ^0.8.25;

import {IClearingHouse} from "./interfaces/IClearingHouse.sol";

/// @title ClearingPaymaster
/// @notice Sponsors gas for clearing operations performed by authorised node operators.
///
/// @dev EIP-7702 context (Pectra):
///      EIP-7702 allows an EOA to delegate its code to a contract for a single
///      transaction via a signed authorisation tuple:
///        (chain_id, address(this), nonce, y_parity, r, s)
///      The Go paymaster service (internal/paymaster/service.go) builds and signs
///      this tuple. When the node operator submits a clearing transaction, the
///      EVM applies this contract's code to the operator's EOA, allowing the
///      transaction to be gas-sponsored by this contract's ETH balance.
///
///      On pre-Pectra networks (current Sepolia), this contract functions as a
///      conventional gas subsidy: the operator calls sponsorCall() directly and
///      the paymaster executes the inner call while tracking gas spend.
///
/// Economic model:
///   - The paymaster is funded by the protocol (or node operator pool).
///   - Only authorised clearers can request sponsorship.
///   - A per-clearer gas allowance prevents a single operator draining the balance.
///   - clearIntent() calls are always sponsored; registerIntent() and markProven()
///     are sponsored only if the intent exists or the call IS the registration.
contract ClearingPaymaster {

    // ─── state ───────────────────────────────────────────────────────────────

    IClearingHouse public immutable CLEARING_HOUSE;
    address        public owner;

    /// @notice Authorised node operators.
    mapping(address => bool) public authorizedClearers;

    /// @notice Cumulative gas units sponsored per clearer.
    mapping(address => uint256) public totalSponsored;

    /// @notice Lifetime gas allowance per clearer (in gas units).
    /// 50M gas ≈ ~500 full clearing transactions at 100k gas each.
    uint256 public maxSponsoredPerClearer = 50_000_000;

    /// @notice Minimum ETH balance required before sponsoring any call.
    uint256 public constant MIN_BALANCE = 0.005 ether;

    // ─── events ──────────────────────────────────────────────────────────────

    event ClearerAuthorized(address indexed clearer);
    event ClearerRevoked(address indexed clearer);
    event GasSponsored(
        address indexed clearer,
        bytes32 indexed withdrawalHash,
        uint256         gasUsed
    );
    event Funded(address indexed funder, uint256 amount);
    event Withdrawn(address indexed to, uint256 amount);
    event LimitUpdated(uint256 newLimit);

    // ─── errors ──────────────────────────────────────────────────────────────

    error NotAuthorized(address clearer);
    error InsufficientBalance(uint256 have, uint256 need);
    error RateLimitExceeded(address clearer, uint256 used, uint256 limit);
    error NotOwner();
    error ZeroAddress();
    error TransferFailed();
    error InnerCallFailed(bytes reason);

    // ─── constructor ─────────────────────────────────────────────────────────

    constructor(address clearingHouse) {
        if (clearingHouse == address(0)) revert ZeroAddress();
        CLEARING_HOUSE = IClearingHouse(clearingHouse);
        owner = msg.sender;
    }

    receive() external payable {
        emit Funded(msg.sender, msg.value);
    }

    // ─── modifiers ───────────────────────────────────────────────────────────

    modifier onlyOwner() {
        if (msg.sender != owner) revert NotOwner();
        _;
    }

    // ─── admin ───────────────────────────────────────────────────────────────

    function authorizeClearer(address clearer) external onlyOwner {
        if (clearer == address(0)) revert ZeroAddress();
        authorizedClearers[clearer] = true;
        emit ClearerAuthorized(clearer);
    }

    function revokeClearer(address clearer) external onlyOwner {
        authorizedClearers[clearer] = false;
        emit ClearerRevoked(clearer);
    }

    function setMaxSponsoredPerClearer(uint256 limit) external onlyOwner {
        maxSponsoredPerClearer = limit;
        emit LimitUpdated(limit);
    }

    function withdraw(address payable to, uint256 amount) external onlyOwner {
        if (address(this).balance < amount)
            revert InsufficientBalance(address(this).balance, amount);
        (bool ok,) = to.call{value: amount}("");
        if (!ok) revert TransferFailed();
        emit Withdrawn(to, amount);
    }

    function transferOwnership(address newOwner) external onlyOwner {
        if (newOwner == address(0)) revert ZeroAddress();
        owner = newOwner;
    }

    // ─── sponsorship ─────────────────────────────────────────────────────────

    /// @notice Sponsor a clearing call on behalf of an authorised node operator.
    ///
    /// @dev Gate logic:
    ///   - Caller must be an authorised clearer.
    ///   - Paymaster must have >= MIN_BALANCE.
    ///   - Either the intent exists in the clearing house OR the call is a
    ///     registerIntent() (which creates it — allowed before the record exists).
    ///   - Cumulative gas for this clearer must stay under maxSponsoredPerClearer.
    ///
    /// @param withdrawalHash  Identifies the intent — used for gating and event log.
    /// @param target          Contract to call (L1ClearingHouse address).
    /// @param data            Calldata: registerIntent / markProven / clearIntent.
    function sponsorCall(
        bytes32      withdrawalHash,
        address      target,
        bytes calldata data
    ) external returns (bytes memory result) {
        if (!authorizedClearers[msg.sender]) revert NotAuthorized(msg.sender);
        if (address(this).balance < MIN_BALANCE)
            revert InsufficientBalance(address(this).balance, MIN_BALANCE);

        // Allow registerIntent before the record exists; everything else requires it.
        if (!_isRegisterCall(data)) {
            IClearingHouse.ClearingIntent memory intent =
                CLEARING_HOUSE.getIntent(withdrawalHash);
            require(intent.registeredAt > 0, "paymaster: intent not registered");
        }

        uint256 gasBefore = gasleft();

        bool success;
        (success, result) = target.call(data);
        if (!success) revert InnerCallFailed(result);

        uint256 gasUsed = gasBefore - gasleft();

        uint256 newTotal = totalSponsored[msg.sender] + gasUsed;
        if (newTotal > maxSponsoredPerClearer)
            revert RateLimitExceeded(msg.sender, totalSponsored[msg.sender], maxSponsoredPerClearer);
        totalSponsored[msg.sender] = newTotal;

        emit GasSponsored(msg.sender, withdrawalHash, gasUsed);
    }

    // ─── views ───────────────────────────────────────────────────────────────

    function balance() external view returns (uint256) {
        return address(this).balance;
    }

    function remainingAllowance(address clearer) external view returns (uint256) {
        uint256 used = totalSponsored[clearer];
        if (used >= maxSponsoredPerClearer) return 0;
        return maxSponsoredPerClearer - used;
    }

    // ─── internal ────────────────────────────────────────────────────────────

    /// @dev Returns true if the calldata targets registerIntent().
    function _isRegisterCall(bytes calldata data) internal pure returns (bool) {
        if (data.length < 4) return false;
        return bytes4(data[:4]) == IClearingHouse.registerIntent.selector;
    }
}
