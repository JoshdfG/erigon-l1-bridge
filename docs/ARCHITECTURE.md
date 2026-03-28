# Erigon L1 Clearing Bridge — Architecture

## Layer map (clean architecture)

```
cmd/bridge/main.go               — wiring only; knows every layer
  ├── config/                    — typed env config, fail-fast on startup
  ├── pkg/logger/                — structured logging (zerolog in full build)
  │
  ├── internal/entity/           — PURE DOMAIN TYPES — no imports at all
  │     types.go                 — BridgeEvent, ClearingIntent, ChainID, Status
  │     chain.go                 — ChainMetadata, FinalityConfig
  │
  ├── internal/usecase/          — BUSINESS LOGIC — owns all interface definitions
  │     interfaces.go            — ChainClient, BridgeAdapter, Paymaster, Repos
  │     clearing.go              — L1 clearing state machine (the R&D thesis)
  │
  ├── internal/chain/            — ChainClient implementations (one pkg per chain)
  │     base/                    — Base mainnet (OP Stack, chain 8453) ← primary
  │     optimism/                — OP Mainnet stub (chain 10)
  │     arbitrum/                — Arbitrum One stub (chain 42161)
  │
  ├── internal/bridge/           — BridgeAdapter implementations (one pkg per chain)
  │     base/                    — OP Stack proof + finalization
  │     optimism/                — stub
  │     arbitrum/                — stub (different proof model)
  │
  ├── internal/paymaster/        — EIP-7702 gas delegation (post-Pectra)
  ├── internal/finality/         — per-chain safe/finalized block logic
  ├── internal/listener/         — multi-chain event subscription fanout
  ├── internal/clearing/         — orchestrator (ticks the use case on interval)
  │
  ├── internal/repo/             — repository implementations
  │     memory/                  — in-memory store (tests + local devnet)
  │     (postgres/)              — add here when persisting across restarts
  │
  └── internal/contracts/        — abigen-generated ABI bindings
        clearing/                — L1 ClearingBridge.sol
        paymaster/               — EIP-7702 Paymaster.sol
```

## Extending to a new chain

1. Add `ChainID*` constant to `internal/entity/types.go`
2. Create `internal/chain/<name>/client.go` implementing `usecase.ChainClient`
3. Create `internal/bridge/<name>/adapter.go` implementing `usecase.BridgeAdapter`
4. Wire in `cmd/bridge/main.go` — nothing else changes

## EIP-7702 paymaster model (post-Pectra)

The paymaster holds an EIP-7702 authorization for an EOA on the target chain.
When the L1 orchestrator submits a clearing transaction, the paymaster contract
covers gas. The node operator never needs native tokens on every target chain.

Authorization tuple: `(chain_id, address, nonce, y_parity, r, s)`

## Trust model

```
Source L2 event observed        → on-chain, no trust required
Finality check vs L1 head       → trustless (node's own view)
Proof submitted to L1 portal    → trustless (on-chain verification)
Target chain finalization        → EIP-7702 paymaster delegation
```

The orchestrator is a coordinator only. It cannot forge proofs or alter amounts.
It can only submit what the L1 portal contract independently verifies.

## Development phases

| Phase | Status | Description |
|-------|--------|-------------|
| 1 | in progress | CL/EL interface, event listener, shared types |
| 2 | pending | EIP-7702 paymaster, OP Stack proofs, finality |
| 3 | pending | L1 clearing contract, orchestrator, devnet harness |
