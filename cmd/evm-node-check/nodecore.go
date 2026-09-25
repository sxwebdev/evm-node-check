package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/sxwebdev/evm-node-check/internal/nodecore"
	"github.com/urfave/cli/v3"
)

func nodecoreFlags() []cli.Flag {
	defaults := nodecore.DefaultOptions()
	return []cli.Flag{
		&cli.StringFlag{
			Name:     "config",
			Aliases:  []string{"c"},
			Usage:    "Path to the nodecore YAML config (upstream-config.upstreams is used)",
			Required: true,
		},
		&cli.StringFlag{
			Name:    "metrics-url",
			Aliases: []string{"m"},
			Usage:   "nodecore Prometheus metrics endpoint",
			Value:   defaults.MetricsURL,
		},
		&cli.IntFlag{
			Name:    "wait",
			Aliases: []string{"w"},
			Usage:   "Seconds after nodecore start to let upstreams settle",
			Value:   int(defaults.Wait / time.Second),
		},
		&cli.BoolFlag{
			Name:    "verbose",
			Aliases: []string{"v"},
			Usage:   "Enable verbose output",
			Value:   false,
		},
	}
}

func runNodecore(ctx context.Context, cmd *cli.Command) error {
	logger := newLogger(cmd.Bool("verbose"))

	cfg, err := nodecore.Load(cmd.String("config"))
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	opts := nodecore.DefaultOptions()
	opts.MetricsURL = cmd.String("metrics-url")
	opts.Wait = time.Duration(cmd.Int("wait")) * time.Second

	logger.Info("loaded config", "upstreams", len(cfg.Upstreams), "metrics_url", opts.MetricsURL)

	result, err := nodecore.New(cfg, opts, logger).Check(ctx)
	if err != nil {
		return fmt.Errorf("check failed: %w", err)
	}

	printNodecoreResults(logger, result)

	if !result.Passed {
		return fmt.Errorf("some upstreams are not available")
	}

	logger.Info("all upstreams are available")
	return nil
}

func printNodecoreResults(logger *slog.Logger, result *nodecore.CheckResult) {
	available := 0
	for _, u := range result.Upstreams {
		attrs := []any{
			"id", u.ID,
			"chain", u.Chain,
			"status", u.Status,
		}
		if u.HasHead {
			attrs = append(attrs, "head", u.Head)
		}
		if u.HasLag {
			attrs = append(attrs, "head_lag", u.HeadLag)
		}

		if u.Passed {
			available++
			logger.Info("upstream OK", attrs...)
		} else {
			logger.Error("upstream FAILED", append(attrs, "reason", u.Reason)...)
		}
	}

	for _, id := range result.Unexpected {
		logger.Warn("upstream reported by nodecore is not in config (nodecore may run a different config)", "id", id)
	}

	logger.Info("summary", "total_upstreams", len(result.Upstreams), "available", available)
}
