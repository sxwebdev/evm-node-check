package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/sxwebdev/evm-node-check/internal/checker"
	"github.com/sxwebdev/evm-node-check/internal/config"
	"github.com/urfave/cli/v3"
)

// evmFlags returns the flags of the evm check. The root command reuses them
// (local, config not required) to keep the pre-subcommand invocation working.
func evmFlags(root bool) []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:     "config",
			Aliases:  []string{"c"},
			Usage:    "Path to YAML config file with nodes list",
			Required: !root,
			Local:    root,
			Hidden:   root,
		},
		&cli.IntFlag{
			Name:    "max-block-gap",
			Aliases: []string{"g"},
			Usage:   "Maximum allowed block gap between nodes",
			Value:   10,
			Local:   root,
			Hidden:  root,
		},
		&cli.IntFlag{
			Name:    "block-hash-count",
			Aliases: []string{"b"},
			Usage:   "Number of recent blocks to compare hashes",
			Value:   5,
			Local:   root,
			Hidden:  root,
		},
		&cli.IntFlag{
			Name:    "timeout",
			Aliases: []string{"t"},
			Usage:   "Timeout in seconds for each node check",
			Value:   5,
			Local:   root,
			Hidden:  root,
		},
		&cli.IntFlag{
			Name:   "max-block-age",
			Usage:  "Maximum allowed age in seconds of the head block timestamp (0 to disable)",
			Value:  120,
			Local:  root,
			Hidden: root,
		},
		&cli.IntFlag{
			Name:   "min-peer-count",
			Usage:  "Minimum allowed net_peerCount",
			Value:  3,
			Local:  root,
			Hidden: root,
		},
		&cli.BoolFlag{
			Name:    "skip-debug-check",
			Aliases: []string{"s"},
			Usage:   "Skip debug mode availability check",
			Value:   false,
			Local:   root,
			Hidden:  root,
		},
		&cli.BoolFlag{
			Name:    "verbose",
			Aliases: []string{"v"},
			Usage:   "Enable verbose output",
			Value:   false,
			Local:   root,
			Hidden:  root,
		},
	}
}

// runRoot keeps `evm-node-check -c config.yaml` working as an alias of `evm`.
func runRoot(ctx context.Context, cmd *cli.Command) error {
	if !cmd.IsSet("config") {
		return cli.ShowRootCommandHelp(cmd)
	}
	return runEVM(ctx, cmd)
}

func runEVM(ctx context.Context, cmd *cli.Command) error {
	logger := newLogger(cmd.Bool("verbose"))

	// Load config
	configPath := cmd.String("config")
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	nodesByChain := cfg.GetNodesByChain()
	totalNodes := 0
	for _, nodes := range nodesByChain {
		totalNodes += len(nodes)
	}

	logger.Info("loaded config", "chains", len(nodesByChain), "total_nodes", totalNodes)

	// Setup checker options
	opts := checker.Options{
		MaxBlockGap:    uint64(cmd.Int("max-block-gap")),
		BlockHashCount: int(cmd.Int("block-hash-count")),
		CheckTimeout:   time.Duration(cmd.Int("timeout")) * time.Second,
		CheckDebugMode: !cmd.Bool("skip-debug-check"),
		MaxBlockAge:    time.Duration(cmd.Int("max-block-age")) * time.Second,
		MinPeerCount:   uint64(cmd.Int("min-peer-count")),
	}

	// Run checker
	c := checker.New(cfg, opts, logger)
	result, err := c.Check(ctx)
	if err != nil {
		return fmt.Errorf("check failed: %w", err)
	}

	// Print results
	printResults(logger, result)

	if !result.Passed {
		return fmt.Errorf("some nodes failed checks")
	}

	logger.Info("all nodes passed checks")
	return nil
}

func printResults(logger *slog.Logger, result *checker.CheckResult) {
	for _, chainResult := range result.ChainResults {
		chainAttrs := []any{
			"chain", chainResult.Chain,
			"chain_id", chainResult.ExpectedChainID,
			"max_block_number", chainResult.MaxBlockNumber,
			"total_nodes", len(chainResult.Nodes),
			"failed_nodes", len(chainResult.FailedNodes),
		}
		if chainResult.CommonBlockNumber != 0 {
			chainAttrs = append(chainAttrs, "common_block", chainResult.CommonBlockNumber)
		}
		if chainResult.HistoricalBlockNumber != 0 {
			chainAttrs = append(chainAttrs, "historical_block", chainResult.HistoricalBlockNumber)
		}
		logger.Info("chain results", chainAttrs...)

		// Print successful nodes
		for _, node := range chainResult.Nodes {
			if node.Error != nil {
				continue
			}

			// Check if this node is in failed list
			failed := false
			for _, fn := range chainResult.FailedNodes {
				if fn.Address == node.Address {
					failed = true
					break
				}
			}

			if !failed {
				nodeAttrs := []any{
					"id", node.ID,
					"chain", node.Chain,
					"block_number", node.BlockNumber,
					"syncing", node.Syncing,
					"debug_ok", node.DebugOK,
					"latency", node.Latency,
				}
				if node.PeerCountSupported {
					nodeAttrs = append(nodeAttrs, "peer_count", node.PeerCount)
				}
				if node.HeadBlockTime > 0 {
					nodeAttrs = append(nodeAttrs,
						"head_block_age", time.Since(time.Unix(int64(node.HeadBlockTime), 0)).Truncate(time.Second),
					)
				}
				if node.TxCounts != nil && node.TxCounts.Err == nil {
					nodeAttrs = append(nodeAttrs,
						"tx_count_eth", node.TxCounts.Eth,
						"tx_count_receipts", node.TxCounts.Receipts,
						"tx_count_debug", node.TxCounts.Debug,
					)
				}
				if node.HistoricalTxCounts != nil && node.HistoricalTxCounts.Err == nil {
					nodeAttrs = append(nodeAttrs,
						"historical_tx_count_eth", node.HistoricalTxCounts.Eth,
						"historical_tx_count_receipts", node.HistoricalTxCounts.Receipts,
						"historical_tx_count_debug", node.HistoricalTxCounts.Debug,
					)
				}
				logger.Info("node OK", nodeAttrs...)
			}
		}
	}

	// Print failed nodes
	if len(result.FailedNodes) > 0 {
		logger.Warn("failed nodes detected")
		for _, fn := range result.FailedNodes {
			logger.Error("node FAILED",
				"id", fn.ID,
				"chain", fn.Chain,
				"address", fn.Address,
				"reason", fn.Reason,
			)
		}
	}
}
