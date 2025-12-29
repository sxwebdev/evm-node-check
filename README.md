# evm-node-check

CLI tool to validate consistency of EVM RPC nodes.

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
evm-node-check -c config.yaml
```

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
evm-node-check -c config.yaml

# Allow larger block gap and check more blocks
evm-node-check -c config.yaml -g 20 -b 10

# Skip debug mode check (for nodes without debug API)
evm-node-check -c config.yaml --skip-debug-check

# Verbose output
evm-node-check -c config.yaml -v
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
          url: http://65.108.12.169:8545
    - id: eth-testnet-2
      chain: sepolia
      connectors:
        - type: json-rpc
          url: http://51.195.60.61:8545

    # BSC testnet (97)
    - id: bsc-testnet-1
      chain: bsc-testnet
      connectors:
        - type: json-rpc
          url: http://144.76.115.142:8545

    # Polygon testnet (80002)
    - id: polygon-testnet-1
      chain: polygon-amoy
      connectors:
        - type: json-rpc
          url: http://157.90.68.155:8545
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

## License

MIT
