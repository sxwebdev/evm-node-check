package nodecore

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"time"
)

type Options struct {
	// MetricsURL is nodecore's Prometheus endpoint (server.metrics-port).
	MetricsURL string
	// Wait is how long after nodecore's start upstreams may still be
	// settling (immature or not yet registered). Once nodecore has been up
	// for longer than Wait, the check is evaluated immediately.
	Wait time.Duration
	// ConnectTimeout is how long an unreachable metrics endpoint is retried
	// (nodecore may have just been started). It is independent of Wait, so a
	// stopped nodecore is reported quickly.
	ConnectTimeout time.Duration
	// PollInterval is the pause between metrics polls while waiting.
	PollInterval time.Duration
	// RequestTimeout bounds a single metrics request.
	RequestTimeout time.Duration
}

func DefaultOptions() Options {
	return Options{
		MetricsURL:     "http://localhost:9093/metrics",
		Wait:           60 * time.Second,
		ConnectTimeout: 5 * time.Second,
		PollInterval:   2 * time.Second,
		RequestTimeout: 5 * time.Second,
	}
}

type Status string

const (
	StatusAvailable   Status = "available"
	StatusImmature    Status = "immature"
	StatusSyncing     Status = "syncing"
	StatusUnavailable Status = "unavailable"
	// StatusMissing means nodecore does not report the upstream at all: it
	// never passed initial validation (unreachable node, wrong chain, invalid
	// credentials, ...), or nodecore runs a different config.
	StatusMissing Status = "missing"
	StatusUnknown Status = "unknown"
)

// statusOf maps nodecore_upstream_availability_status values.
func statusOf(v float64) Status {
	switch v {
	case 1:
		return StatusAvailable
	case 2:
		return StatusImmature
	case 3:
		return StatusSyncing
	case 4:
		return StatusUnavailable
	default:
		return StatusUnknown
	}
}

type UpstreamResult struct {
	ID string
	// Chain is the chain reported by nodecore, or the configured one if the
	// upstream is missing.
	Chain   string
	Status  Status
	Head    uint64
	HasHead bool
	HeadLag int64
	HasLag  bool
	Reason  string
	Passed  bool
}

type CheckResult struct {
	Upstreams []UpstreamResult
	// Unexpected lists upstreams nodecore reports that are not in the config,
	// a sign that nodecore runs a different config.
	Unexpected []string
	Passed     bool
}

type Checker struct {
	cfg    *Config
	opts   Options
	logger *slog.Logger
	client *http.Client
}

func New(cfg *Config, opts Options, logger *slog.Logger) *Checker {
	return &Checker{
		cfg:    cfg,
		opts:   opts,
		logger: logger,
		client: &http.Client{},
	}
}

// Check polls nodecore metrics until every configured upstream has settled
// or the wait window since nodecore's start has passed, then evaluates them.
func (c *Checker) Check(ctx context.Context) (*CheckResult, error) {
	started := time.Now()
	lastPending := -1

	for {
		snap, err := c.fetch(ctx)
		if err != nil {
			// nodecore may have just been started: its metrics server comes up
			// a moment later. Anything longer means it is not running.
			if time.Since(started) >= c.opts.ConnectTimeout {
				return nil, fmt.Errorf("nodecore metrics are not reachable (is nodecore running? check its logs): %w", err)
			}
			c.logger.Debug("metrics not available yet, retrying", "error", err)
		} else {
			deadline := started.Add(c.opts.Wait)
			if !snap.startTime.IsZero() {
				deadline = snap.startTime.Add(c.opts.Wait)
			}
			pending := c.pending(snap)
			if len(pending) == 0 || !time.Now().Before(deadline) {
				return c.evaluate(snap), nil
			}
			// Log progress only when it changes to keep the output readable.
			level := slog.LevelDebug
			if len(pending) != lastPending {
				level, lastPending = slog.LevelInfo, len(pending)
			}
			c.logger.Log(ctx, level, "waiting for upstreams to settle",
				"pending", len(pending),
				"remaining", time.Until(deadline).Truncate(time.Second),
			)
			c.logger.Debug("pending upstreams", "ids", pending)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.opts.PollInterval):
		}
	}
}

// pending returns configured upstreams that have not settled yet.
func (c *Checker) pending(snap *snapshot) []string {
	var ids []string
	for _, u := range c.cfg.Upstreams {
		m := snap.upstreams[u.ID]
		if m == nil || !m.hasStatus || statusOf(m.status) == StatusImmature {
			ids = append(ids, u.ID)
		}
	}
	return ids
}

func (c *Checker) evaluate(snap *snapshot) *CheckResult {
	result := &CheckResult{
		Upstreams: make([]UpstreamResult, 0, len(c.cfg.Upstreams)),
		Passed:    true,
	}

	configured := make(map[string]bool, len(c.cfg.Upstreams))
	for _, u := range c.cfg.Upstreams {
		configured[u.ID] = true

		res := UpstreamResult{ID: u.ID, Chain: u.Chain, Status: StatusMissing}
		if m := snap.upstreams[u.ID]; m != nil {
			if m.chain != "" {
				res.Chain = m.chain
			}
			if m.hasStatus {
				res.Status = statusOf(m.status)
			}
			if m.hasHead {
				res.Head, res.HasHead = uint64(m.head), true
			}
			if m.hasLag {
				res.HeadLag, res.HasLag = int64(m.headLag), true
			}
		}

		switch res.Status {
		case StatusAvailable:
			res.Passed = true
		case StatusMissing:
			res.Reason = "not reported by nodecore: failed initial validation (unreachable, wrong chain, invalid credentials, ...), see nodecore logs"
		case StatusImmature:
			res.Reason = fmt.Sprintf("still immature %s after nodecore start", c.opts.Wait)
		case StatusSyncing:
			res.Reason = "syncing"
			if res.HasLag {
				res.Reason = fmt.Sprintf("syncing: %d blocks behind head", res.HeadLag)
			}
		case StatusUnavailable:
			res.Reason = "unavailable: failing nodecore validation, see nodecore logs"
		default:
			res.Reason = "unknown availability status"
		}

		if !res.Passed {
			result.Passed = false
		}
		result.Upstreams = append(result.Upstreams, res)
	}

	for id := range snap.upstreams {
		if !configured[id] {
			result.Unexpected = append(result.Unexpected, id)
		}
	}
	slices.Sort(result.Unexpected)

	return result
}

func (c *Checker) fetch(ctx context.Context) (*snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.opts.MetricsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build metrics request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch nodecore metrics: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return nil, fmt.Errorf("failed to fetch nodecore metrics: HTTP %d: %s", resp.StatusCode, body)
	}

	snap, err := parseSnapshot(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse nodecore metrics: %w", err)
	}
	return snap, nil
}
