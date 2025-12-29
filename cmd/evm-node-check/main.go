package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/sxwebdev/evm-node-check/internal/checker"
	"github.com/sxwebdev/evm-node-check/internal/config"
	"github.com/urfave/cli/v3"
)

func main() {
	cmd := &cli.Command{
		Name:  "evm-node-check",
		Usage: "Check EVM RPC nodes for consistency",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "config",
				Aliases:  []string{"c"},
				Usage:    "Path to YAML config file with nodes list",
				Required: true,
			},
			&cli.IntFlag{
				Name:    "max-block-gap",
				Aliases: []string{"g"},
				Usage:   "Maximum allowed block gap between nodes",
				Value:   10,
			},
			&cli.IntFlag{
				Name:    "block-hash-count",
				Aliases: []string{"b"},
				Usage:   "Number of recent blocks to compare hashes",
				Value:   5,
			},
			&cli.BoolFlag{
				Name:    "skip-debug-check",
				Aliases: []string{"s"},
				Usage:   "Skip debug mode availability check",
				Value:   false,
			},
			&cli.BoolFlag{
				Name:    "verbose",
				Aliases: []string{"v"},
				Usage:   "Enable verbose output",
				Value:   false,
			},
		},
		Action: run,
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cmd *cli.Command) error {
	// Setup logger
	logLevel := slog.LevelInfo
	if cmd.Bool("verbose") {
		logLevel = slog.LevelDebug
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))

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
		CheckDebugMode: !cmd.Bool("skip-debug-check"),
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
		logger.Info("chain results",
			"chain", chainResult.Chain,
			"chain_id", chainResult.ExpectedChainID,
			"max_block_number", chainResult.MaxBlockNumber,
			"total_nodes", len(chainResult.Nodes),
			"failed_nodes", len(chainResult.FailedNodes),
		)

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
				logger.Info("node OK",
					"id", node.ID,
					"chain", node.Chain,
					"block_number", node.BlockNumber,
					"debug_ok", node.DebugOK,
				)
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
