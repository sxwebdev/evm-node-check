package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/sxwebdev/evm-node-check/internal/ton"
	"github.com/urfave/cli/v3"
)

func tonFlags() []cli.Flag {
	defaults := ton.DefaultOptions()
	return []cli.Flag{
		&cli.StringFlag{
			Name:     "config",
			Aliases:  []string{"c"},
			Usage:    "Path to YAML config file with TON Center API keys",
			Required: true,
		},
		&cli.IntFlag{
			Name:    "timeout",
			Aliases: []string{"t"},
			Usage:   "Timeout in seconds for each key check (including retries)",
			Value:   int(defaults.Timeout / time.Second),
		},
		&cli.IntFlag{
			Name:  "max-block-age",
			Usage: "Maximum allowed age in seconds of the last masterchain block (0 to disable)",
			Value: int(defaults.MaxBlockAge / time.Second),
		},
		&cli.IntFlag{
			Name:  "max-seqno-gap",
			Usage: "Maximum allowed masterchain seqno gap between API v2 and v3",
			Value: int(defaults.MaxSeqnoGap),
		},
		&cli.IntFlag{
			Name:  "max-retries",
			Usage: "Number of retries on HTTP 429 per request",
			Value: defaults.MaxRetries,
		},
		&cli.BoolFlag{
			Name:    "verbose",
			Aliases: []string{"v"},
			Usage:   "Enable verbose output",
			Value:   false,
		},
	}
}

func runTON(ctx context.Context, cmd *cli.Command) error {
	logger := newLogger(cmd.Bool("verbose"))

	cfg, err := ton.Load(cmd.String("config"))
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	logger.Info("loaded config",
		"keys", len(cfg.Keys),
		"mainnet_endpoint", cfg.Endpoints.Mainnet,
		"testnet_endpoint", cfg.Endpoints.Testnet,
	)

	opts := ton.DefaultOptions()
	opts.Timeout = time.Duration(cmd.Int("timeout")) * time.Second
	opts.MaxBlockAge = time.Duration(cmd.Int("max-block-age")) * time.Second
	opts.MaxSeqnoGap = uint32(cmd.Int("max-seqno-gap"))
	opts.MaxRetries = int(cmd.Int("max-retries"))

	result := ton.New(cfg, opts, logger).Check(ctx)

	printTONResults(logger, result)

	if !result.Passed {
		return fmt.Errorf("some keys failed checks")
	}

	logger.Info("all keys passed checks")
	return nil
}

func printTONResults(logger *slog.Logger, result *ton.CheckResult) {
	valid := 0
	for _, k := range result.Keys {
		if k.Status == ton.StatusValid {
			valid++
		}

		declared := string(k.Network)
		if declared == "" {
			declared = "auto"
		}
		attrs := []any{
			"name", k.Name,
			"key", k.MaskedKey,
			"network", declared,
			"status", k.Status,
		}
		if k.DetectedNetwork != "" {
			attrs = append(attrs,
				"detected_network", k.DetectedNetwork,
				"latency", k.Latency.Truncate(time.Millisecond),
			)
		}
		if k.V2Seqno != 0 {
			attrs = append(attrs, "v2_seqno", k.V2Seqno)
		}
		if !k.V2BlockTime.IsZero() {
			attrs = append(attrs, "v2_block_age", time.Since(k.V2BlockTime).Truncate(time.Second))
		}
		if k.V3Seqno != 0 {
			attrs = append(attrs, "v3_seqno", k.V3Seqno)
		}
		if !k.V3BlockTime.IsZero() {
			attrs = append(attrs, "v3_block_age", time.Since(k.V3BlockTime).Truncate(time.Second))
		}

		if k.Passed {
			logger.Info("key OK", attrs...)
		} else {
			logger.Error("key FAILED", append(attrs, "reason", k.Reason)...)
		}
	}

	logger.Info("summary", "total_keys", len(result.Keys), "valid_keys", valid)
}
