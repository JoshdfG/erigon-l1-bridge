# L1 Clearing Bridge Contracts

## Structure

```
contracts/
  src/
    L1ClearingHouse.sol      ← clearing registry (core contract)
    ClearingPaymaster.sol    ← EIP-7702 gas sponsor
    interfaces/
      IClearingHouse.sol
      IOptimismPortal2.sol
  test/
    L1ClearingHouse.t.sol    ← full test suite (unit + fuzz)
  script/
    Deploy.s.sol             ← deploy to Ethereum Sepolia
```

## Step 1 Install forge-std

```bash
cd contracts
forge install foundry-rs/forge-std --no-git
```

## Step 2 Run tests

```bash
forge test -vv
```

Expected: 28 tests pass including 3 fuzz tests (1000 runs each).

```
[PASS] test_constructor_setsPortal
[PASS] test_constructor_setsOwner
[PASS] test_constructor_supportedChains
[PASS] test_constructor_revertsZeroPortal
[PASS] test_registerIntent_succeeds
[PASS] test_registerIntent_emitsEvent
[PASS] test_registerIntent_revertsAlreadyRegistered
[PASS] test_registerIntent_revertsUnsupportedChain
[PASS] test_registerIntent_revertsZeroRecipient
[PASS] test_markProven_succeeds
[PASS] test_markProven_emitsEvent
[PASS] test_markProven_revertsNotProver
[PASS] test_markProven_revertsPortalNotProven
[PASS] test_markProven_revertsWrongStatus_alreadyProven
[PASS] test_markProven_revertsNotRegistered
[PASS] test_clearIntent_succeeds
[PASS] test_clearIntent_isPermissionless
[PASS] test_clearIntent_emitsEvent
[PASS] test_clearIntent_revertsIfNotProven
[PASS] test_clearIntent_revertsIfAlreadyCleared
[PASS] test_clearIntent_revertsIfProofGoneFromPortal
[PASS] test_clearIntent_revertsNotRegistered
[PASS] test_isCleared_falseBeforeCleared
[PASS] test_isCleared_falseForUnknown
[PASS] test_markFailed_byOwner
[PASS] test_markFailed_revertsIfNotOwner
[PASS] test_addSupportedChain
[PASS] test_removeSupportedChain
[PASS] test_chainManagement_revertsIfNotOwner
[PASS] test_transferOwnership
[PASS] test_transferOwnership_revertsZeroAddress
[PASS] test_transferOwnership_revertsIfNotOwner
[PASS] test_paymaster_sponsorsRegisterIntent
[PASS] test_paymaster_sponsorsMarkProven
[PASS] test_paymaster_revertsIfNotAuthorized
[PASS] test_paymaster_revertsInsufficientBalance
[PASS] test_paymaster_remainingAllowance
[PASS] test_paymaster_balance
[PASS] test_paymaster_authorizeClearer_revertsZeroAddress
[PASS] test_paymaster_withdraw
[PASS] test_paymaster_withdraw_revertsInsufficientBalance
[PASS] testFuzz_fullLifecycle (1000 runs)
[PASS] testFuzz_twoIndependentIntents (1000 runs)
[PASS] testFuzz_noDoubleRegistration (1000 runs)
```

## Step 3 Get testnet ETH

- Sepolia ETH (for L1 deploy): https://sepoliafaucet.com
- Base Sepolia ETH (for bridge testing): https://docs.base.org/docs/tools/network-faucets

You need ~0.1 Sepolia ETH for deployment + paymaster funding.

## Step 4 Deploy to Ethereum Sepolia

Add to `.env`:

```bash
DEPLOYER_PRIVATE_KEY=0x...
L1_RPC_URL=https://eth-sepolia.g.alchemy.com/v2/YOUR_KEY
ETHERSCAN_API_KEY=...          # optional, for verification
```

Deploy:

```bash
forge script script/Deploy.s.sol \
  --rpc-url $L1_RPC_URL \
  --broadcast \
  --verify \
  --etherscan-api-key $ETHERSCAN_API_KEY
```

## Step 5 Wire the Go bridge

Copy the printed addresses into your project root `.env`:

```bash
CLEARING_CONTRACT_ADDRESS=0x...
PAYMASTER_ADDRESS=0x...
BRIDGE_SIGNER_PRIVATE_KEY=0x...  # same as deployer for now
BASE_PORTAL_ADDRESS=0x49f53e41452C74589E85cA1677426Ba426459e85
BASE_RPC_URL=https://base-sepolia.g.alchemy.com/v2/YOUR_KEY
BASE_WS_URL=wss://base-sepolia.g.alchemy.com/v2/YOUR_KEY
L1_RPC_URL=https://eth-sepolia.g.alchemy.com/v2/YOUR_KEY
```

Then:

```bash
make run
```

The Go orchestrator will watch Base Sepolia for withdrawals and call
`registerIntent` → `markProven` → `clearIntent` on the deployed contracts.
