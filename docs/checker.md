# EVM Node Checker — How It Works

## Purpose

The tool validates a pool of EVM-compatible RPC nodes for production readiness. The goal is to filter out nodes that technically respond but are practically unusable: nodes lagging behind the chain, still syncing, isolated from the P2P network, diverging from the majority on block data, or running with broken indexes.

## Input

The input is a YAML config listing RPC endpoints grouped by chain name (`ethereum-mainnet`, `arbitrum-sepolia`, `bsc`, …). All nodes within a single chain are probed independently and then cross-compared.

## Pipeline

The checker iterates chain by chain. Within each chain, nodes are first probed in parallel to collect metrics, then validated against thresholds and against each other.

### Stage 1. Metric collection per node (parallel)

Each node is polled under its own timeout to gather the following signals:

- **Chain ID** — the network identifier the node claims to serve. The latency of this call is also recorded as a baseline responsiveness metric.
- **Block number** — the current chain head as seen by the node.
- **Sync progress** — the syncing state. Interpreted strictly: a non-nil progress object is treated as "syncing" only if the standard fields show a real lag beyond the configured block gap. This is necessary for L2s (Arbitrum and similar), where `eth_syncing` returns vendor-specific objects even on a fully synced node.
- **Peer count** — the number of P2P peers. If the method is not supported by the node (JSON-RPC `-32601`) or returns `0`, the signal is marked as "not applicable" and skipped during validation — this is normal for L2s and Avalanche subnets.
- **Hashes of the last N blocks** — used for cross-node fork/reorg detection.
- **Head block timestamp** — extracted from the latest block header; used for freshness checks.
- **Debug API** — availability of `debug_traceBlockByNumber` with the callTracer is probed to distinguish trace-capable nodes from regular ones.

### Stage 2. Deriving chain-level baselines

From the collected data, the checker computes:

- **Expected Chain ID** — taken from the first successfully responding node and used as the reference for the rest.
- **Maximum block number** — the max across all live nodes, forming the baseline for block-gap calculations.

### Stage 3. Threshold validation

Each node goes through the following checks in order. The first failing check records the reason, marks the node as failed, and skips the remaining checks for that node in the same run.

1. **Connection error** — if metric collection failed (TCP/TLS/RPC), the node is rejected immediately.
2. **Chain ID matches the reference** — catches misconfigured endpoints pointing at the wrong network.
3. **Block gap within threshold** — `maxBlockNumber - nodeBlockNumber` must fit within `MaxBlockGap`. Catches lagging nodes.
4. **Node is not syncing** — see semantics above: the flag is raised only on a real, measurable lag in the standard SyncProgress fields.
5. **Peer count above the minimum** — applied only for nodes where the method is supported and returned > 0. A node isolated from P2P will soon fork away from the network.
6. **Head block freshness** — the age of the latest block (wall clock minus `block.timestamp`) must not exceed `MaxBlockAge`. Catches the "whole chain frozen" scenario, where the block gap is technically zero but the chain is not advancing.
7. **Debug API available** — optional, gated by an option. Required for nodes that must support tracing.

### Stage 4. Transaction count consistency on a common block

After threshold validation, among the nodes that passed every prior check, the checker picks a **common block** — the minimum of their `BlockNumber`. This block is guaranteed to exist on every candidate, ruling out false divergence caused by one node simply being behind.

For each such node, three RPC methods are invoked in parallel for that block:

- **Fetch the block with full transactions** — the source of truth for the transaction set.
- **Fetch all block receipts** — execution results for the same transactions.
- **Trace all block transactions via the debug API** — execution traces.

The element count of each response is then validated:

- **Intra-node consistency** — all three counts must match. A mismatch signals a broken archive reindex, a partial snapshot, a desynced node-service database, or a misconfigured tracer.
- **Cross-node consistency** — majority vote determines the canonical count; nodes in the minority are marked as failed. This catches hidden divergence in the transaction set that header-hash comparison alone would miss.

### Stage 5. Block hash majority voting

For each of the last N blocks, the checker builds a distribution of hashes across nodes. If everyone agrees, the block is skipped. If there is disagreement, the majority hash is picked, and nodes with minority hashes are marked as failed with the specific block that diverged.

This catches forks and reorgs: a node stuck on a stale branch will have a correct chain ID and a normal block gap, but its hashes will not match the majority.

## Output

For each node the checker returns either "OK" with the collected metrics (block number, peer count, block age, latency, transaction counts), or a failed entry with a short human-readable reason.

The overall verdict: **PASSED** if every node in every chain passed; **FAILED** if at least one node failed. The CLI returns a non-zero exit code on FAILED, so the tool drops cleanly into CI/monitoring.

## What each check filters out

| Check                 | Scenario it catches                                                                                 |
| --------------------- | --------------------------------------------------------------------------------------------------- |
| Connection error      | Node unreachable, RPC unresponsive, broken TLS                                                      |
| Chain ID mismatch     | Wrong endpoint in the config (different network)                                                    |
| Block gap             | Node lagging behind the majority more than allowed                                                  |
| Syncing               | Node actively catching up to the chain, its data is incomplete                                      |
| Peer count            | Node isolated from P2P, will soon drift from the network                                            |
| Head block freshness  | The entire chain or node frozen despite a nominally valid block number                              |
| Debug API             | Node cannot be used for tracing                                                                     |
| Tx count (internal)   | Desync between methods on the same node: broken receipts index, faulty tracer, inconsistent archive |
| Tx count (cross-node) | Node silently diverging on the transaction set                                                      |
| Block hash majority   | Node stuck on a fork or stale branch after a reorg                                                  |

## Concurrency

- Chains are processed sequentially (there are usually few of them, and isolation simplifies the report).
- Nodes within a chain are probed in parallel, each under its own timeout.
- The three RPC calls used for transaction-count cross-check run in parallel per node via an errgroup — a shared timeout covers all of them, and the first error cancels the siblings.

## Configuration

All thresholds are exposed as options and wired through the CLI: maximum allowed block gap, minimum peer count, maximum head block age, number of blocks for hash cross-check, per-node timeout, and a flag to require debug API support. This lets operators tune strictness per network (L1 vs L2, mainnet vs testnet) without touching the code.
