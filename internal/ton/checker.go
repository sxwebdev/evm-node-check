package ton

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Options struct {
	// Timeout bounds the whole check of a single key, including retries.
	Timeout time.Duration
	// MaxBlockAge is the maximum allowed age of the last masterchain block
	// reported by each API version (0 to disable).
	MaxBlockAge time.Duration
	// MaxSeqnoGap is the maximum allowed difference between the masterchain
	// seqno reported by API v2 and API v3.
	MaxSeqnoGap uint32
	// MaxRetries is the number of retries on HTTP 429 per request.
	MaxRetries int
	// RetryDelay is the pause between retries on HTTP 429.
	RetryDelay time.Duration
	// MinRPS is the request rate a key must sustain (0 to disable). TON Center
	// does not expose the plan of a key, so the rate is probed: requests are
	// sent at MinRPS for RateProbeDuration and nearly all must be accepted.
	// The free plan allows 10 rps, paid ones 25 rps and more.
	MinRPS int
	// RateProbeDuration is how long the rate probe sends requests.
	RateProbeDuration time.Duration
	// RateProbeCooldown is the pause before each rate probe, so that earlier
	// requests do not count against the probe.
	RateProbeCooldown time.Duration
}

func DefaultOptions() Options {
	return Options{
		Timeout:     15 * time.Second,
		MaxBlockAge: time.Minute,
		MaxSeqnoGap: 10,
		MaxRetries:  3,
		RetryDelay:  time.Second,

		MinRPS:            30,
		RateProbeDuration: 2 * time.Second,
		RateProbeCooldown: time.Second,
	}
}

type Status string

const (
	// StatusValid means TON Center accepted the key.
	StatusValid Status = "valid"
	// StatusInvalid means TON Center rejected the key (HTTP 401).
	StatusInvalid Status = "invalid"
	// StatusRateLimited means every attempt got HTTP 429, so validity is unknown.
	StatusRateLimited Status = "rate_limited"
	// StatusUnavailable means TON Center failed for a reason unrelated to the key.
	StatusUnavailable Status = "unavailable"
)

var (
	errInvalidKey  = errors.New("API key rejected")
	errRateLimited = errors.New("rate limited")
)

func statusOf(err error) Status {
	switch {
	case err == nil:
		return StatusValid
	case errors.Is(err, errInvalidKey):
		return StatusInvalid
	case errors.Is(err, errRateLimited):
		return StatusRateLimited
	default:
		return StatusUnavailable
	}
}

type KeyResult struct {
	Name      string
	MaskedKey string
	// Network is the network declared in the config (empty for autodetect).
	Network Network
	// DetectedNetwork is the network that accepted the key (empty if none did).
	DetectedNetwork Network
	Status          Status
	Latency         time.Duration
	V2Seqno         uint32
	V2BlockTime     time.Time
	V3Seqno         uint32
	V3BlockTime     time.Time
	// ProbeSent and ProbeAccepted are the rate probe counters (zero if the
	// probe did not run).
	ProbeSent     int
	ProbeAccepted int
	// AcceptedRPS is the rate TON Center accepted during the rate probe.
	AcceptedRPS float64
	// LowRateLimit means the key could not sustain MinRPS, which usually
	// means its account is on the free plan.
	LowRateLimit bool
	Reason       string
	Passed       bool
}

type CheckResult struct {
	Keys   []KeyResult
	Passed bool
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

func (c *Checker) Check(ctx context.Context) *CheckResult {
	result := &CheckResult{
		Keys:   make([]KeyResult, len(c.cfg.Keys)),
		Passed: true,
	}

	var wg sync.WaitGroup
	for i, k := range c.cfg.Keys {
		wg.Go(func() {
			result.Keys[i] = c.checkKey(ctx, k)
		})
	}
	wg.Wait()

	// Rate limits are shared by all keys of an account on a network, so the
	// probes run one at a time to not interfere with each other.
	if c.opts.MinRPS > 0 {
		for i := range result.Keys {
			if result.Keys[i].Status == StatusValid {
				c.checkRate(ctx, c.cfg.Keys[i].Key, &result.Keys[i])
			}
		}
	}

	for _, k := range result.Keys {
		if !k.Passed {
			result.Passed = false
		}
	}

	return result
}

func (c *Checker) checkKey(ctx context.Context, k Key) KeyResult {
	res := KeyResult{
		Name:      k.Name,
		MaskedKey: MaskKey(k.Key),
		Network:   k.Network,
	}

	ctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()

	info, err := c.detectNetwork(ctx, k, &res)
	res.Status = statusOf(err)
	if err != nil {
		res.Reason = err.Error()
		return res
	}
	res.V2Seqno = info.Last.Seqno

	var problems []string
	if k.Network != "" && res.DetectedNetwork != k.Network {
		problems = append(problems, fmt.Sprintf("key belongs to %s, not %s", res.DetectedNetwork, k.Network))
	}
	problems = append(problems, c.checkHealth(ctx, k.Key, info, &res)...)

	res.Reason = strings.Join(problems, "; ")
	res.Passed = len(problems) == 0
	return res
}

// detectNetwork probes the declared network first, then the other one. TON
// Center issues keys per network, so a key rejected by one network may still
// be valid on the other. The returned error reflects the declared network, or
// for autodetect, the most informative failure.
func (c *Checker) detectNetwork(ctx context.Context, k Key, res *KeyResult) (*v2MasterchainInfo, error) {
	first := k.Network
	if first == "" {
		first = Mainnet
	}

	start := time.Now()
	info, err := c.v2MasterchainInfo(ctx, first, k.Key)
	res.Latency = time.Since(start)
	if err == nil {
		res.DetectedNetwork = first
		return info, nil
	}
	if k.Network != "" && !errors.Is(err, errInvalidKey) {
		return nil, fmt.Errorf("%s: %w", first, err)
	}

	second := first.other()
	start = time.Now()
	otherInfo, otherErr := c.v2MasterchainInfo(ctx, second, k.Key)
	if otherErr == nil {
		res.Latency = time.Since(start)
		res.DetectedNetwork = second
		return otherInfo, nil
	}

	// Prefer a non-auth failure: it means validity could not be established.
	// Only otherErr is wrapped so that the status reflects it.
	if errors.Is(err, errInvalidKey) && !errors.Is(otherErr, errInvalidKey) && k.Network == "" {
		return nil, fmt.Errorf("%s: %v; %s: %w", first, err, second, otherErr)
	}
	if errors.Is(err, errInvalidKey) && errors.Is(otherErr, errInvalidKey) {
		return nil, fmt.Errorf("%w on both %s and %s", errInvalidKey, first, second)
	}
	return nil, fmt.Errorf("%s: %w", first, err)
}

// checkHealth verifies that both API versions serve the expected network and
// are up to date. It returns human-readable problems (empty if healthy).
func (c *Checker) checkHealth(ctx context.Context, key string, info *v2MasterchainInfo, res *KeyResult) []string {
	var problems []string
	n := res.DetectedNetwork

	header, err := c.v2BlockHeader(ctx, n, key, info.Last)
	if err != nil {
		problems = append(problems, fmt.Sprintf("v2 getBlockHeader: %v", err))
	} else {
		res.V2BlockTime = time.Unix(header.GenUtime, 0)
		problems = append(problems, c.checkBlock("v2", n, header.GlobalID, res.V2BlockTime)...)
	}

	v3, err := c.v3MasterchainInfo(ctx, n, key)
	if err != nil {
		problems = append(problems, fmt.Sprintf("v3 masterchainInfo: %v", err))
	} else {
		res.V3Seqno = v3.Last.Seqno
		res.V3BlockTime = time.Unix(v3.Last.GenUtime, 0)
		problems = append(problems, c.checkBlock("v3", n, v3.Last.GlobalID, res.V3BlockTime)...)

		gap := max(res.V2Seqno, res.V3Seqno) - min(res.V2Seqno, res.V3Seqno)
		if gap > c.opts.MaxSeqnoGap {
			problems = append(problems, fmt.Sprintf("v2/v3 seqno gap too large: v2=%d v3=%d (max allowed: %d)",
				res.V2Seqno, res.V3Seqno, c.opts.MaxSeqnoGap))
		}
	}

	return problems
}

func (c *Checker) checkBlock(api string, n Network, globalID int32, blockTime time.Time) []string {
	var problems []string
	if globalID != n.globalID() {
		problems = append(problems, fmt.Sprintf("%s global_id mismatch: expected %d for %s, got %d",
			api, n.globalID(), n, globalID))
	}
	if c.opts.MaxBlockAge > 0 {
		if age := time.Since(blockTime); age > c.opts.MaxBlockAge {
			problems = append(problems, fmt.Sprintf("%s last block stale: age %s (max allowed: %s)",
				api, age.Truncate(time.Second), c.opts.MaxBlockAge))
		}
	}
	return problems
}

// checkRate probes the rate limit of a valid key and records a problem if the
// key cannot sustain MinRPS.
func (c *Checker) checkRate(ctx context.Context, key string, res *KeyResult) {
	select {
	case <-ctx.Done():
		res.addProblem(fmt.Sprintf("rate probe: %v", ctx.Err()))
		return
	case <-time.After(c.opts.RateProbeCooldown):
	}

	ctx, cancel := context.WithTimeout(ctx, c.opts.RateProbeDuration+c.opts.Timeout)
	defer cancel()

	sent, accepted, err := c.probeRate(ctx, res.DetectedNetwork, key)
	res.ProbeSent, res.ProbeAccepted = sent, accepted
	res.AcceptedRPS = float64(accepted) / c.opts.RateProbeDuration.Seconds()
	if err != nil {
		res.addProblem(fmt.Sprintf("rate probe: %v", err))
		return
	}
	// Allow a few rejections caused by network jitter.
	if accepted*10 < sent*9 {
		res.LowRateLimit = true
		res.addProblem(fmt.Sprintf("rate limit below %d rps: %d/%d requests accepted (~%.1f rps), account is likely on the free plan",
			c.opts.MinRPS, accepted, sent, res.AcceptedRPS))
	}
}

// probeRate sends getMasterchainInfo at MinRPS for RateProbeDuration without
// retrying on HTTP 429, and returns how many requests were sent and accepted.
func (c *Checker) probeRate(ctx context.Context, n Network, key string) (sent, accepted int, err error) {
	total := max(1, int(float64(c.opts.MinRPS)*c.opts.RateProbeDuration.Seconds()))
	ticker := time.NewTicker(time.Second / time.Duration(c.opts.MinRPS))
	defer ticker.Stop()

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	record := func(ok bool, err error) {
		mu.Lock()
		defer mu.Unlock()
		if ok {
			accepted++
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	for sent < total {
		if sent > 0 {
			select {
			case <-ctx.Done():
			case <-ticker.C:
			}
			if err := ctx.Err(); err != nil {
				record(false, err)
				break
			}
		}
		sent++
		wg.Go(func() {
			status, body, err := c.do(ctx, n, key, "/api/v2/getMasterchainInfo", nil)
			switch {
			case err != nil:
				record(false, err)
			case status == http.StatusOK:
				record(true, nil)
			case status == http.StatusTooManyRequests:
				record(false, nil)
			default:
				record(false, fmt.Errorf("HTTP %d: %s", status, errorMessage(body)))
			}
		})
	}
	wg.Wait()

	return sent, accepted, firstErr
}

func (r *KeyResult) addProblem(problem string) {
	if r.Reason != "" {
		r.Reason += "; "
	}
	r.Reason += problem
	r.Passed = false
}

// MaskKey hides the middle of an API key so it can be logged safely.
func MaskKey(key string) string {
	if len(key) <= 12 {
		return strings.Repeat("*", len(key))
	}
	return key[:4] + "…" + key[len(key)-4:]
}

type blockID struct {
	Workchain int32  `json:"workchain"`
	Shard     string `json:"shard"`
	Seqno     uint32 `json:"seqno"`
}

type v2Response[T any] struct {
	OK     bool   `json:"ok"`
	Result T      `json:"result"`
	Error  string `json:"error"`
	Code   int    `json:"code"`
}

type v2MasterchainInfo struct {
	Last blockID `json:"last"`
}

type v2BlockHeader struct {
	GlobalID int32 `json:"global_id"`
	GenUtime int64 `json:"gen_utime"`
}

type v3MasterchainInfo struct {
	Last struct {
		Seqno    uint32 `json:"seqno"`
		GlobalID int32  `json:"global_id"`
		GenUtime int64  `json:"gen_utime,string"`
	} `json:"last"`
}

func (c *Checker) v2MasterchainInfo(ctx context.Context, n Network, key string) (*v2MasterchainInfo, error) {
	return v2Get[v2MasterchainInfo](ctx, c, n, key, "getMasterchainInfo", nil)
}

func (c *Checker) v2BlockHeader(ctx context.Context, n Network, key string, id blockID) (*v2BlockHeader, error) {
	query := url.Values{
		"workchain": {fmt.Sprint(id.Workchain)},
		"shard":     {id.Shard},
		"seqno":     {fmt.Sprint(id.Seqno)},
	}
	return v2Get[v2BlockHeader](ctx, c, n, key, "getBlockHeader", query)
}

func (c *Checker) v3MasterchainInfo(ctx context.Context, n Network, key string) (*v3MasterchainInfo, error) {
	var resp v3MasterchainInfo
	if err := c.get(ctx, n, key, "/api/v3/masterchainInfo", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// v2Get calls an API v2 method and unwraps the ok/result envelope.
func v2Get[T any](ctx context.Context, c *Checker, n Network, key, method string, query url.Values) (*T, error) {
	var resp v2Response[T]
	if err := c.get(ctx, n, key, "/api/v2/"+method, query, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		if resp.Code == http.StatusUnauthorized {
			return nil, fmt.Errorf("%w: %s", errInvalidKey, resp.Error)
		}
		return nil, fmt.Errorf("%s returned error (code %d): %s", method, resp.Code, resp.Error)
	}
	return &resp.Result, nil
}

// get performs an authenticated GET, retrying on HTTP 429, and decodes the
// JSON body into out. The key is always sent: TON Center serves anonymous
// requests on mainnet, so omitting it would make any key look valid.
func (c *Checker) get(ctx context.Context, n Network, key, path string, query url.Values, out any) error {
	for attempt := 0; ; attempt++ {
		status, body, err := c.do(ctx, n, key, path, query)
		if err != nil {
			return err
		}

		switch status {
		case http.StatusOK:
			if err := json.Unmarshal(body, out); err != nil {
				return fmt.Errorf("failed to decode response: %w", err)
			}
			return nil
		case http.StatusUnauthorized:
			return fmt.Errorf("%w: %s", errInvalidKey, errorMessage(body))
		case http.StatusTooManyRequests:
			if attempt >= c.opts.MaxRetries {
				return fmt.Errorf("%w after %d attempts", errRateLimited, attempt+1)
			}
			c.logger.Debug("rate limited, retrying", "network", n, "path", path, "attempt", attempt+1)
			select {
			case <-ctx.Done():
				return fmt.Errorf("%w: %w", errRateLimited, ctx.Err())
			case <-time.After(c.opts.RetryDelay):
			}
		default:
			return fmt.Errorf("HTTP %d: %s", status, errorMessage(body))
		}
	}
}

// do performs a single authenticated GET and returns the status and body.
func (c *Checker) do(ctx context.Context, n Network, key, path string, query url.Values) (int, []byte, error) {
	u := c.cfg.Endpoints.URL(n) + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("X-API-Key", key)

	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("failed to read response: %w", err)
	}
	return resp.StatusCode, body, nil
}

// errorMessage extracts the "error" field of a TON Center error body, falling
// back to a truncated raw body.
func errorMessage(body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return e.Error
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
