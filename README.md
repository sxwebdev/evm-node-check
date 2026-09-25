# evm-node-check

CLI tool to validate consistency of EVM RPC nodes and validity of [TON Center](https://toncenter.com) API keys.

- `evm-node-check evm` — check EVM RPC nodes (see below)
- `evm-node-check ton` — check TON Center API keys (see [TON Center API keys](#ton-center-api-keys))
- `evm-node-check nodecore` — check which upstreams of a nodecore config are alive (see [Check nodecore upstreams](#check-nodecore-upstreams))

## Features

- Verifies all nodes within each chain have the same chain ID
- Checks block height gap between nodes (configurable threshold)
- Validates debug mode availability (`debug_traceBlockByNumber` for internal transactions)
- Compares block hashes across nodes to detect forks/inconsistencies
- Supports multiple chains in a single config file

## Installation

```bash
go install github.com/sxwebdev/evm-node-check/cmd/evm-node-check@latest
```

Or build from source:

```bash
git clone https://github.com/sxwebdev/evm-node-check.git
cd evm-node-check
go build ./cmd/evm-node-check
```

## Usage

```bash
evm-node-check evm -c config.yaml
```

Running without a subcommand (`evm-node-check -c config.yaml`) is kept as an alias of `evm`.

### Flags

| Flag                 | Short | Default  | Description                               |
| -------------------- | ----- | -------- | ----------------------------------------- |
| `--config`           | `-c`  | required | Path to YAML config file                  |
| `--max-block-gap`    | `-g`  | 10       | Maximum allowed block gap between nodes   |
| `--block-hash-count` | `-b`  | 5        | Number of recent blocks to compare hashes |
| `--skip-debug-check` | `-s`  | false    | Skip debug mode availability check        |
| `--verbose`          | `-v`  | false    | Enable verbose output                     |

### Examples

```bash
# Basic check
evm-node-check evm -c config.yaml

# Allow larger block gap and check more blocks
evm-node-check evm -c config.yaml -g 20 -b 10

# Skip debug mode check (for nodes without debug API)
evm-node-check evm -c config.yaml --skip-debug-check

# Verbose output
evm-node-check evm -c config.yaml -v
```

## Configuration

Create a YAML file with your RPC nodes:

```yaml
upstream-config:
  upstreams:
    # Ethereum testnet (11155111)
    - id: eth-testnet-1
      chain: sepolia
      connectors:
        - type: json-rpc
          url: http://sepolia-node-1.example.com:8545
    - id: eth-testnet-2
      chain: sepolia
      connectors:
        - type: json-rpc
          url: http://sepolia-node-2.example.com:8545

    # BSC testnet (97)
    - id: bsc-testnet-1
      chain: bsc-testnet
      connectors:
        - type: json-rpc
          url: http://bsc-testnet-node-1.example.com:8545

    # Polygon testnet (80002)
    - id: polygon-testnet-1
      chain: polygon-amoy
      connectors:
        - type: json-rpc
          url: http://polygon-amoy-node-1.example.com:8545
```

### Config Fields

- `id` - Unique identifier for the node (used in logs)
- `chain` - Chain name (nodes are grouped and validated within chains)
- `connectors` - List of connectors (only `json-rpc` type is supported)
  - `type` - Must be `json-rpc`
  - `url` - RPC endpoint URL

## Exit Codes

- `0` - All nodes passed checks
- `1` - One or more nodes failed checks

## Checks Performed

1. **Chain ID** - All nodes within a chain must return the same chain ID
2. **Block Gap** - No node should be more than N blocks behind the highest block
3. **Debug Mode** - Nodes must support `debug_traceBlockByNumber` with `callTracer`
4. **Block Hashes** - Recent block hashes must match across nodes (majority vote)

## TON Center API keys

`evm-node-check ton` checks that TON Center API keys are accepted and that the API behind them is healthy, on mainnet and testnet.

```bash
cp ton.example.yaml ton.yaml   # ton.yaml is git-ignored
evm-node-check ton -c ton.yaml
```

```yaml
# endpoints:                      # optional, defaults to toncenter.com
#   mainnet: https://toncenter.com
#   testnet: https://testnet.toncenter.com
keys:
  - name: backend-prod
    network: mainnet
    key: "..."
  - name: unknown-origin          # network omitted: detected automatically
    key: "..."
```

### Flags

| Flag              | Short | Default  | Description                                                   |
| ----------------- | ----- | -------- | ------------------------------------------------------------- |
| `--config`        | `-c`  | required | Path to YAML config file with keys                            |
| `--timeout`       | `-t`  | 15       | Timeout in seconds for each key check (including retries)     |
| `--max-block-age` |       | 60       | Maximum age in seconds of the last masterchain block (0 = off) |
| `--max-seqno-gap` |       | 10       | Maximum masterchain seqno gap between API v2 and v3           |
| `--max-retries`   |       | 3        | Retries on HTTP 429 per request                               |
| `--verbose`       | `-v`  | false    | Enable verbose output                                         |

### Key status

The key is always sent in the `X-API-Key` header: TON Center serves anonymous requests on mainnet, so a request without the key would succeed regardless of its validity.

- `valid` — the key was accepted by `GET /api/v2/getMasterchainInfo`
- `invalid` — TON Center answered `401` on both networks
- `rate_limited` — every attempt got `429`; validity is unknown
- `unavailable` — network error or `5xx`; validity is unknown

TON Center issues keys per network. If a key is rejected on its declared network, the other network is probed and a mismatch is reported (`key belongs to testnet, not mainnet`).

### Health checks (valid keys only)

- `global_id` of the last masterchain block matches the network (`-239` mainnet, `-3` testnet) for both API v2 and v3 — catches a misconfigured endpoint
- Last masterchain block is fresh, for both API v2 (`getBlockHeader`) and v3 (`masterchainInfo`)
- Masterchain seqno of API v2 and v3 differ by at most `--max-seqno-gap`

Exit code is `1` if any key is not valid or fails a health check.

## Local nodecore

Everything nodecore-related lives in `nodecore/`: `docker-compose.yml` runs [nodecore](https://github.com/drpcorg/nodecore) with `nodecore/nodecore.yml` (git-ignored):

```bash
cp nodecore/nodecore.example.yml nodecore/nodecore.yml   # or put your upstreams there
make nodecore-up                       # start in background
make nodecore-logs                     # follow logs
make nodecore-down                     # stop
make nodecore-check                    # which upstreams are alive
```

### Check nodecore upstreams

`evm-node-check nodecore` reports, for every upstream in `upstream-config.upstreams` of a nodecore config, whether a running nodecore considers it available:

```bash
make nodecore-check
# or
evm-node-check nodecore -c nodecore/nodecore.yml --metrics-url http://localhost:9093/metrics
```

| Flag            | Short | Default                         | Description                                         |
| --------------- | ----- | ------------------------------- | --------------------------------------------------- |
| `--config`      | `-c`  | required                        | Path to the nodecore config                         |
| `--metrics-url` | `-m`  | `http://localhost:9093/metrics` | nodecore Prometheus endpoint (`server.metrics-port`) |
| `--wait`        | `-w`  | 60                              | Seconds after nodecore start to let upstreams settle |
| `--verbose`     | `-v`  | false                           | Enable verbose output                               |

The verdict is nodecore's own (`nodecore_upstream_availability_status`, produced by its validators — use `mode: strict` to run all of them): `available`, `immature`, `syncing`, `unavailable`. nodecore does not report an upstream that never passed its initial validation (unreachable node, wrong chain, invalid API key), and `/status` only shows per-chain state, so the config is compared against the metrics and such upstreams are reported as `missing` — the cause is in `make nodecore-logs`.

Right after nodecore starts, upstreams are `immature` or not registered yet. The check polls until every upstream has settled or `--wait` seconds have passed since nodecore's start (`process_start_time_seconds`), so it returns immediately on a long-running nodecore. If the metrics endpoint does not respond at all, it is retried for only 5 seconds (a just-started nodecore), and a stopped nodecore is reported right away. Exit code is `1` if any upstream is not `available`.

The compose stack also runs Redis (no persistence) for nodecore's cache and app storages; reference it from `nodecore.yml` as `redis://redis:6379/0`. To keep a config that uses another Redis hostname unchanged, give Redis that hostname as a network alias in `nodecore/docker-compose.override.yml` (git-ignored, picked up by the `make nodecore-*` targets; see `nodecore/docker-compose.override.example.yml`).

Ports: `9090` JSON-RPC (`/queries/<chain>`), `9093` Prometheus metrics (`/metrics`), `9096` health (`/health`, `/ready`, `/status`). The container runs as a non-root user, so `nodecore/nodecore.yml` must be world-readable.

## License

MIT
