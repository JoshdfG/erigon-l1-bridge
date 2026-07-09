// SPDX-License-Identifier: MIT
pragma solidity ^0.8.25;

import "forge-std/Test.sol";
import {L1ClearingHouse} from "../src/L1ClearingHouse.sol";
import {ClearingPaymaster} from "../src/ClearingPaymaster.sol";
import {IClearingHouse} from "../src/interfaces/IClearingHouse.sol";
import {IOptimismPortal2} from "../src/interfaces/IOptimismPortal2.sol";

// ─── Mock OptimismPortal2 ─────────────────────────────────────────────────────

/// @dev Controls what provenWithdrawals() returns so we can test the
///      clearing house's portal cross-check without a live L1 node.
contract MockPortal is IOptimismPortal2 {
    mapping(bytes32 => mapping(address => uint64)) private _timestamps;

    function setProven(bytes32 hash, address prover) external {
        _timestamps[hash][prover] = uint64(block.timestamp);
    }

    function clearProven(bytes32 hash, address prover) external {
        _timestamps[hash][prover] = 0;
    }

    function provenWithdrawals(bytes32 hash, address prover) external view override returns (address, uint64) {
        return (address(0), _timestamps[hash][prover]);
    }

    function proveWithdrawalTransaction(
        WithdrawalTransaction calldata,
        uint256,
        OutputRootProof calldata,
        bytes[] calldata
    ) external override {}

    function finalizeWithdrawalTransaction(WithdrawalTransaction calldata) external override {}
}

// ─── L1ClearingHouseTest ──────────────────────────────────────────────────────

contract L1ClearingHouseTest is Test {
    L1ClearingHouse public house;
    MockPortal public portal;
    ClearingPaymaster public paymaster;

    address owner = address(this);
    address prover = makeAddr("prover");
    address alice = makeAddr("alice");
    address bob = makeAddr("bob");

    uint64 constant BASE_SEPOLIA = 84532;
    uint64 constant OP_MAINNET = 10;
    bytes32 constant WITHDRAWAL_HASH = keccak256("test-withdrawal-1");

    // ─── setup ───────────────────────────────────────────────────────────────

    function setUp() public {
        portal = new MockPortal();

        uint64[] memory chains = new uint64[](1);
        chains[0] = BASE_SEPOLIA;

        house = new L1ClearingHouse(address(portal), chains);
        paymaster = new ClearingPaymaster(address(house));

        vm.deal(address(paymaster), 1 ether);
    }

    // ─── constructor ─────────────────────────────────────────────────────────

    function test_constructor_setsPortal() public view {
        assertEq(address(house.PORTAL()), address(portal));
    }

    function test_constructor_setsOwner() public view {
        assertEq(house.owner(), owner);
    }

    function test_constructor_supportedChains() public view {
        assertTrue(house.supportedChains(BASE_SEPOLIA));
        assertFalse(house.supportedChains(OP_MAINNET));
    }

    function test_constructor_revertsZeroPortal() public {
        uint64[] memory chains = new uint64[](0);
        vm.expectRevert(L1ClearingHouse.ZeroAddress.selector);
        new L1ClearingHouse(address(0), chains);
    }

    // ─── registerIntent ───────────────────────────────────────────────────────

    function test_registerIntent_succeeds() public {
        vm.prank(prover);
        house.registerIntent(WITHDRAWAL_HASH, BASE_SEPOLIA, alice);

        IClearingHouse.ClearingIntent memory intent = house.getIntent(WITHDRAWAL_HASH);

        assertEq(intent.withdrawalHash, WITHDRAWAL_HASH);
        assertEq(intent.prover, prover);
        assertEq(intent.recipient, alice);
        assertEq(intent.sourceChainId, BASE_SEPOLIA);
        assertEq(uint8(intent.status), uint8(IClearingHouse.IntentStatus.Pending));
        assertGt(intent.registeredAt, 0);
        assertEq(intent.provenAt, 0);
    }

    function test_registerIntent_emitsEvent() public {
        vm.expectEmit(true, true, true, true);
        emit IClearingHouse.IntentRegistered(WITHDRAWAL_HASH, BASE_SEPOLIA, alice, prover);

        vm.prank(prover);
        house.registerIntent(WITHDRAWAL_HASH, BASE_SEPOLIA, alice);
    }

    function test_registerIntent_revertsAlreadyRegistered() public {
        vm.prank(prover);
        house.registerIntent(WITHDRAWAL_HASH, BASE_SEPOLIA, alice);

        vm.prank(bob);
        vm.expectRevert(abi.encodeWithSelector(L1ClearingHouse.AlreadyRegistered.selector, WITHDRAWAL_HASH));
        house.registerIntent(WITHDRAWAL_HASH, BASE_SEPOLIA, alice);
    }

    function test_registerIntent_revertsUnsupportedChain() public {
        vm.expectRevert(abi.encodeWithSelector(L1ClearingHouse.UnsupportedChain.selector, OP_MAINNET));
        house.registerIntent(WITHDRAWAL_HASH, OP_MAINNET, alice);
    }

    function test_registerIntent_revertsZeroRecipient() public {
        vm.expectRevert(L1ClearingHouse.ZeroAddress.selector);
        house.registerIntent(WITHDRAWAL_HASH, BASE_SEPOLIA, address(0));
    }

    // ─── markProven ───────────────────────────────────────────────────────────

    function test_markProven_succeeds() public {
        _register();
        portal.setProven(WITHDRAWAL_HASH, prover);

        vm.prank(prover);
        house.markProven(WITHDRAWAL_HASH);

        IClearingHouse.ClearingIntent memory intent = house.getIntent(WITHDRAWAL_HASH);
        assertEq(uint8(intent.status), uint8(IClearingHouse.IntentStatus.Proven));
        assertGt(intent.provenAt, 0);
    }

    function test_markProven_emitsEvent() public {
        _register();
        portal.setProven(WITHDRAWAL_HASH, prover);

        vm.expectEmit(true, false, false, false);
        emit IClearingHouse.IntentProven(WITHDRAWAL_HASH);

        vm.prank(prover);
        house.markProven(WITHDRAWAL_HASH);
    }

    function test_markProven_revertsNotProver() public {
        _register();
        portal.setProven(WITHDRAWAL_HASH, prover);

        vm.prank(alice);
        vm.expectRevert(abi.encodeWithSelector(L1ClearingHouse.NotProver.selector, WITHDRAWAL_HASH, alice));
        house.markProven(WITHDRAWAL_HASH);
    }

    function test_markProven_revertsPortalNotProven() public {
        _register();
        // portal NOT set — provenWithdrawals returns timestamp = 0

        vm.prank(prover);
        vm.expectRevert(abi.encodeWithSelector(L1ClearingHouse.NotYetProvenOnPortal.selector, WITHDRAWAL_HASH));
        house.markProven(WITHDRAWAL_HASH);
    }

    function test_markProven_revertsWrongStatus_alreadyProven() public {
        _registerAndProve();

        vm.prank(prover);
        vm.expectRevert(
            abi.encodeWithSelector(
                L1ClearingHouse.WrongStatus.selector,
                WITHDRAWAL_HASH,
                IClearingHouse.IntentStatus.Proven,
                IClearingHouse.IntentStatus.Pending
            )
        );
        house.markProven(WITHDRAWAL_HASH);
    }

    function test_markProven_revertsNotRegistered() public {
        vm.prank(prover);
        vm.expectRevert(abi.encodeWithSelector(L1ClearingHouse.NotRegistered.selector, WITHDRAWAL_HASH));
        house.markProven(WITHDRAWAL_HASH);
    }

    // ─── clearIntent ──────────────────────────────────────────────────────────

    function test_clearIntent_succeeds() public {
        _registerAndProve();
        house.clearIntent(WITHDRAWAL_HASH);

        assertTrue(house.isCleared(WITHDRAWAL_HASH));
        IClearingHouse.ClearingIntent memory intent = house.getIntent(WITHDRAWAL_HASH);
        assertEq(uint8(intent.status), uint8(IClearingHouse.IntentStatus.Cleared));
    }

    function test_clearIntent_isPermissionless() public {
        _registerAndProve();

        vm.prank(makeAddr("random-stranger"));
        house.clearIntent(WITHDRAWAL_HASH);

        assertTrue(house.isCleared(WITHDRAWAL_HASH));
    }

    function test_clearIntent_emitsEvent() public {
        _registerAndProve();

        vm.expectEmit(true, false, false, false);
        emit IClearingHouse.IntentCleared(WITHDRAWAL_HASH);

        house.clearIntent(WITHDRAWAL_HASH);
    }

    function test_clearIntent_revertsIfNotProven() public {
        _register();

        vm.expectRevert(
            abi.encodeWithSelector(
                L1ClearingHouse.WrongStatus.selector,
                WITHDRAWAL_HASH,
                IClearingHouse.IntentStatus.Pending,
                IClearingHouse.IntentStatus.Proven
            )
        );
        house.clearIntent(WITHDRAWAL_HASH);
    }

    function test_clearIntent_revertsIfAlreadyCleared() public {
        _registerAndProve();
        house.clearIntent(WITHDRAWAL_HASH);

        vm.expectRevert(
            abi.encodeWithSelector(
                L1ClearingHouse.WrongStatus.selector,
                WITHDRAWAL_HASH,
                IClearingHouse.IntentStatus.Cleared,
                IClearingHouse.IntentStatus.Proven
            )
        );
        house.clearIntent(WITHDRAWAL_HASH);
    }

    function test_clearIntent_revertsIfProofGoneFromPortal() public {
        _registerAndProve();
        // Simulate an edge-case reorg removing the proof from the portal.
        portal.clearProven(WITHDRAWAL_HASH, prover);

        vm.expectRevert(abi.encodeWithSelector(L1ClearingHouse.NotYetProvenOnPortal.selector, WITHDRAWAL_HASH));
        house.clearIntent(WITHDRAWAL_HASH);
    }

    function test_clearIntent_revertsNotRegistered() public {
        vm.expectRevert(abi.encodeWithSelector(L1ClearingHouse.NotRegistered.selector, WITHDRAWAL_HASH));
        house.clearIntent(WITHDRAWAL_HASH);
    }

    // ─── isCleared ───────────────────────────────────────────────────────────

    function test_isCleared_falseBeforeCleared() public {
        _registerAndProve();
        assertFalse(house.isCleared(WITHDRAWAL_HASH));
    }

    function test_isCleared_falseForUnknown() public view {
        assertFalse(house.isCleared(bytes32(uint256(999))));
    }

    // ─── markFailed ──────────────────────────────────────────────────────────

    function test_markFailed_byOwner() public {
        _register();
        house.markFailed(WITHDRAWAL_HASH, "L2 reorg");

        IClearingHouse.ClearingIntent memory intent = house.getIntent(WITHDRAWAL_HASH);
        assertEq(uint8(intent.status), uint8(IClearingHouse.IntentStatus.Failed));
    }

    function test_markFailed_revertsIfNotOwner() public {
        _register();
        vm.prank(alice);
        vm.expectRevert(L1ClearingHouse.NotOwner.selector);
        house.markFailed(WITHDRAWAL_HASH, "nope");
    }

    // ─── chain management ─────────────────────────────────────────────────────

    function test_addSupportedChain() public {
        house.addSupportedChain(OP_MAINNET);
        assertTrue(house.supportedChains(OP_MAINNET));
    }

    function test_removeSupportedChain() public {
        house.removeSupportedChain(BASE_SEPOLIA);
        assertFalse(house.supportedChains(BASE_SEPOLIA));
    }

    function test_chainManagement_revertsIfNotOwner() public {
        vm.prank(alice);
        vm.expectRevert(L1ClearingHouse.NotOwner.selector);
        house.addSupportedChain(OP_MAINNET);
    }

    // ─── ownership ───────────────────────────────────────────────────────────

    function test_transferOwnership() public {
        house.transferOwnership(alice);
        assertEq(house.owner(), alice);
    }

    function test_transferOwnership_revertsZeroAddress() public {
        vm.expectRevert(L1ClearingHouse.ZeroAddress.selector);
        house.transferOwnership(address(0));
    }

    function test_transferOwnership_revertsIfNotOwner() public {
        vm.prank(alice);
        vm.expectRevert(L1ClearingHouse.NotOwner.selector);
        house.transferOwnership(bob);
    }

    // ─── paymaster ────────────────────────────────────────────────────────────

    function test_paymaster_sponsorsRegisterIntent() public {
        paymaster.authorizeClearer(prover);

        bytes memory data = abi.encodeCall(IClearingHouse.registerIntent, (WITHDRAWAL_HASH, BASE_SEPOLIA, alice));

        vm.prank(prover);
        paymaster.sponsorCall(WITHDRAWAL_HASH, address(house), data);

        // The house sees msg.sender = paymaster (it proxied the call).
        IClearingHouse.ClearingIntent memory intent = house.getIntent(WITHDRAWAL_HASH);
        assertEq(intent.prover, address(paymaster));
    }

    function test_paymaster_sponsorsMarkProven() public {
        // Register directly (prover = paymaster from the test above pattern)
        paymaster.authorizeClearer(address(this));

        // Register via paymaster so prover = paymaster address
        bytes memory regData = abi.encodeCall(IClearingHouse.registerIntent, (WITHDRAWAL_HASH, BASE_SEPOLIA, alice));
        paymaster.sponsorCall(WITHDRAWAL_HASH, address(house), regData);

        // Portal must see proof for address(paymaster) since that was msg.sender
        portal.setProven(WITHDRAWAL_HASH, address(paymaster));

        bytes memory proveData = abi.encodeCall(IClearingHouse.markProven, (WITHDRAWAL_HASH));
        paymaster.sponsorCall(WITHDRAWAL_HASH, address(house), proveData);

        IClearingHouse.ClearingIntent memory intent = house.getIntent(WITHDRAWAL_HASH);
        assertEq(uint8(intent.status), uint8(IClearingHouse.IntentStatus.Proven));
    }

    function test_paymaster_revertsIfNotAuthorized() public {
        bytes memory data = abi.encodeCall(IClearingHouse.registerIntent, (WITHDRAWAL_HASH, BASE_SEPOLIA, alice));

        vm.prank(alice);
        vm.expectRevert(abi.encodeWithSelector(ClearingPaymaster.NotAuthorized.selector, alice));
        paymaster.sponsorCall(WITHDRAWAL_HASH, address(house), data);
    }

    function test_paymaster_revertsInsufficientBalance() public {
        paymaster.authorizeClearer(prover);
        paymaster.authorizeClearer(address(this));

        paymaster.withdraw(payable(alice), 1 ether);

        bytes memory data = abi.encodeCall(IClearingHouse.registerIntent, (WITHDRAWAL_HASH, BASE_SEPOLIA, alice));

        vm.prank(prover);
        vm.expectRevert(
            abi.encodeWithSelector(ClearingPaymaster.InsufficientBalance.selector, uint256(0), paymaster.MIN_BALANCE())
        );
        paymaster.sponsorCall(WITHDRAWAL_HASH, address(house), data);
    }

    function test_paymaster_remainingAllowance() public view {
        assertEq(paymaster.remainingAllowance(prover), paymaster.maxSponsoredPerClearer());
    }

    function test_paymaster_balance() public view {
        assertEq(paymaster.balance(), 1 ether);
    }

    function test_paymaster_authorizeClearer_revertsZeroAddress() public {
        vm.expectRevert(ClearingPaymaster.ZeroAddress.selector);
        paymaster.authorizeClearer(address(0));
    }

    function test_paymaster_withdraw() public {
        uint256 before = alice.balance;
        paymaster.withdraw(payable(alice), 0.5 ether);
        assertEq(alice.balance, before + 0.5 ether);
    }

    function test_paymaster_withdraw_revertsInsufficientBalance() public {
        vm.expectRevert(abi.encodeWithSelector(ClearingPaymaster.InsufficientBalance.selector, 1 ether, 2 ether));
        paymaster.withdraw(payable(owner), 2 ether);
    }

    // ─── fuzz: full lifecycle ─────────────────────────────────────────────────

    /// @notice Fuzz the full three-step lifecycle with arbitrary inputs.
    /// @dev    Ensures no combination of valid hash + recipient causes a revert
    ///         when walking the happy path.
    function testFuzz_fullLifecycle(bytes32 hash, address recipient) public {
        vm.assume(recipient != address(0));
        vm.assume(hash != bytes32(0));

        // Step 1: register
        vm.prank(prover);
        house.registerIntent(hash, BASE_SEPOLIA, recipient);

        IClearingHouse.ClearingIntent memory i1 = house.getIntent(hash);
        assertEq(uint8(i1.status), uint8(IClearingHouse.IntentStatus.Pending));
        assertEq(i1.prover, prover);

        // Step 2: prove
        portal.setProven(hash, prover);
        vm.prank(prover);
        house.markProven(hash);

        IClearingHouse.ClearingIntent memory i2 = house.getIntent(hash);
        assertEq(uint8(i2.status), uint8(IClearingHouse.IntentStatus.Proven));
        assertGt(i2.provenAt, 0);

        // Step 3: clear (permissionless)
        house.clearIntent(hash);
        assertTrue(house.isCleared(hash));
    }

    /// @notice No two different hashes can interfere with each other.
    function testFuzz_twoIndependentIntents(bytes32 hashA, bytes32 hashB, address recipientA, address recipientB)
        public
    {
        vm.assume(hashA != hashB);
        vm.assume(recipientA != address(0));
        vm.assume(recipientB != address(0));

        vm.startPrank(prover);
        house.registerIntent(hashA, BASE_SEPOLIA, recipientA);
        house.registerIntent(hashB, BASE_SEPOLIA, recipientB);
        vm.stopPrank();

        // Prove only A
        portal.setProven(hashA, prover);
        vm.prank(prover);
        house.markProven(hashA);

        // B is still Pending
        assertEq(uint8(house.getIntent(hashB).status), uint8(IClearingHouse.IntentStatus.Pending));

        // Clear A
        house.clearIntent(hashA);
        assertTrue(house.isCleared(hashA));
        assertFalse(house.isCleared(hashB));
    }

    /// @notice registering the same hash twice always reverts.
    function testFuzz_noDoubleRegistration(bytes32 hash, address recipient) public {
        vm.assume(recipient != address(0));
        vm.assume(hash != bytes32(0));

        vm.prank(prover);
        house.registerIntent(hash, BASE_SEPOLIA, recipient);

        vm.prank(bob);
        vm.expectRevert(abi.encodeWithSelector(L1ClearingHouse.AlreadyRegistered.selector, hash));
        house.registerIntent(hash, BASE_SEPOLIA, recipient);
    }

    // ─── helpers ─────────────────────────────────────────────────────────────

    function _register() internal {
        vm.prank(prover);
        house.registerIntent(WITHDRAWAL_HASH, BASE_SEPOLIA, alice);
    }

    function _registerAndProve() internal {
        _register();
        portal.setProven(WITHDRAWAL_HASH, prover);
        vm.prank(prover);
        house.markProven(WITHDRAWAL_HASH);
    }
}
