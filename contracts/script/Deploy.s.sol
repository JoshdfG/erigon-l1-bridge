// SPDX-License-Identifier: MIT
pragma solidity ^0.8.25;

import "forge-std/Script.sol";
import {L1ClearingHouse}   from "../src/L1ClearingHouse.sol";
import {ClearingPaymaster} from "../src/ClearingPaymaster.sol";

/// @notice Deploy L1ClearingHouse + ClearingPaymaster to Ethereum Sepolia.
///
/// Required env vars (set in .env):
///   DEPLOYER_PRIVATE_KEY     — funded Sepolia wallet (needs ~0.1 ETH)
///   L1_RPC_URL               — Ethereum Sepolia RPC
///   ETHERSCAN_API_KEY        — for contract verification (optional)
///
/// Optional overrides:
///   OPTIMISM_PORTAL_SEPOLIA  — defaults to Base Sepolia portal address below
///   INITIAL_FUNDING          — wei to send to paymaster (default 0.05 ETH)
///
/// Run:
///   forge script script/Deploy.s.sol \
///     --rpc-url $L1_RPC_URL \
///     --broadcast \
///     --verify \
///     --etherscan-api-key $ETHERSCAN_API_KEY
///
/// After deploy, copy the printed addresses into your .env:
///   CLEARING_CONTRACT_ADDRESS=0x...
///   PAYMASTER_ADDRESS=0x...
contract DeployScript is Script {

    /// @dev Base Sepolia chain ID — the L2 we're clearing for.
    uint64 constant BASE_SEPOLIA = 84532;

    /// @dev OptimismPortal2 proxy on Ethereum Sepolia for Base Sepolia.
    ///      Source: https://docs.base.org/docs/base-contracts
    address constant PORTAL_ETH_SEPOLIA = 0x49f53e41452C74589E85cA1677426Ba426459e85;

    function run() external {
        uint256 deployerKey = vm.envUint("DEPLOYER_PRIVATE_KEY");
        address portal      = vm.envOr("OPTIMISM_PORTAL_SEPOLIA", PORTAL_ETH_SEPOLIA);
        uint256 funding     = vm.envOr("INITIAL_FUNDING", uint256(0.05 ether));
        address deployer    = vm.addr(deployerKey);

        console2.log("======================================");
        console2.log("  L1 Clearing Bridge — Deploy");
        console2.log("======================================");
        console2.log("Deployer :", deployer);
        console2.log("Portal   :", portal);
        console2.log("Funding  :", funding, "wei");
        console2.log("Chain ID :", block.chainid);
        console2.log("");

        vm.startBroadcast(deployerKey);

        // ── 1. L1ClearingHouse ────────────────────────────────────────────────
        uint64[] memory chains = new uint64[](1);
        chains[0] = BASE_SEPOLIA;

        L1ClearingHouse house = new L1ClearingHouse(portal, chains);
        console2.log("L1ClearingHouse  :", address(house));

        // ── 2. ClearingPaymaster ──────────────────────────────────────────────
        ClearingPaymaster pm = new ClearingPaymaster(address(house));
        console2.log("ClearingPaymaster:", address(pm));

        // ── 3. Fund paymaster ─────────────────────────────────────────────────
        if (funding > 0) {
            (bool ok,) = address(pm).call{value: funding}("");
            require(ok, "paymaster funding failed");
            console2.log("Funded paymaster :", funding, "wei");
        }

        // ── 4. Authorise deployer as initial clearer ──────────────────────────
        pm.authorizeClearer(deployer);
        console2.log("Clearer authorised:", deployer);

        vm.stopBroadcast();

        // ── Print .env snippet ────────────────────────────────────────────────
        console2.log("");
        console2.log("======================================");
        console2.log("  Add to .env:");
        console2.log("======================================");
        console2.log("CLEARING_CONTRACT_ADDRESS=", address(house));
        console2.log("PAYMASTER_ADDRESS=",         address(pm));
        console2.log("BRIDGE_SIGNER_PRIVATE_KEY=<your deployer key>");
        console2.log("L1_RPC_URL=<eth-sepolia-rpc>");
        console2.log("BASE_RPC_URL=<base-sepolia-rpc>");
        console2.log("BASE_WS_URL=<base-sepolia-ws>");
        console2.log("BASE_PORTAL_ADDRESS=0x49f53e41452C74589E85cA1677426Ba426459e85");
    }
}
