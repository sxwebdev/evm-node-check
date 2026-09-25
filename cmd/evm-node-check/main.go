package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/urfave/cli/v3"
)

func main() {
	cmd := &cli.Command{
		Name:   "evm-node-check",
		Usage:  "Check EVM RPC nodes, TON Center API keys and nodecore upstreams",
		Flags:  evmFlags(true),
		Action: runRoot,
		Commands: []*cli.Command{
			{
				Name:   "evm",
				Usage:  "Check EVM RPC nodes for consistency",
				Flags:  evmFlags(false),
				Action: runEVM,
			},
			{
				Name:   "ton",
				Usage:  "Validate TON Center API keys on mainnet and testnet",
				Flags:  tonFlags(),
				Action: runTON,
			},
			{
				Name:   "nodecore",
				Usage:  "Check that every upstream from a nodecore config is available in a running nodecore",
				Flags:  nodecoreFlags(),
				Action: runNodecore,
			},
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func newLogger(verbose bool) *slog.Logger {
	logLevel := slog.LevelInfo
	if verbose {
		logLevel = slog.LevelDebug
	}

	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))
}
