package nodecore_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sxwebdev/evm-node-check/internal/nodecore"
)

// fakeMetrics serves a sequence of responses; the last one repeats.
type fakeMetrics struct {
	mu        sync.Mutex
	responses []fakeResponse
	requests  atomic.Int32
}

type fakeResponse struct {
	status int // 0 means 200
	body   string
}

func (f *fakeMetrics) start(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := int(f.requests.Add(1))
		f.mu.Lock()
		resp := f.responses[min(n, len(f.responses))-1]
		f.mu.Unlock()
		if resp.status != 0 {
			w.WriteHeader(resp.status)
		}
		fmt.Fprint(w, resp.body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/metrics"
}

// metrics renders a Prometheus text body. A zero start omits
// process_start_time_seconds.
func metrics(start time.Time, samples ...string) string {
	var b strings.Builder
	b.WriteString("# HELP nodecore_upstream_availability_status Current availability status\n")
	b.WriteString("# TYPE nodecore_upstream_availability_status gauge\n")
	if !start.IsZero() {
		fmt.Fprintf(&b, "process_start_time_seconds %.2f\n", float64(start.UnixMilli())/1000)
	}
	for _, s := range samples {
		b.WriteString(s + "\n")
	}
	return b.String()
}

func status(id, chain string, v int) string {
	return fmt.Sprintf(`nodecore_upstream_availability_status{chain=%q,upstream=%q} %d`, chain, id, v)
}

func testOptions(url string) nodecore.Options {
	return nodecore.Options{
		MetricsURL:     url,
		Wait:           time.Minute,
		ConnectTimeout: 2 * time.Second,
		PollInterval:   10 * time.Millisecond,
		RequestTimeout: 2 * time.Second,
	}
}

func newChecker(opts nodecore.Options, upstreams ...nodecore.Upstream) *nodecore.Checker {
	return nodecore.New(&nodecore.Config{Upstreams: upstreams}, opts, slog.New(slog.DiscardHandler))
}

// longAgo is a nodecore start time well outside any wait window, so the check
// evaluates the first snapshot.
var longAgo = time.Now().Add(-time.Hour)

func TestCheckEvaluatesStatuses(t *testing.T) {
	t.Parallel()

	f := &fakeMetrics{responses: []fakeResponse{{body: metrics(longAgo,
		status("ok", "sepolia", 1),
		`nodecore_upstream_heads{chain="sepolia",upstream="ok"} 1.1778631e+07`,
		`nodecore_upstream_head_lag{chain="sepolia",upstream="ok"} 0`,
		status("young", "sepolia", 2),
		status("behind", "base-sepolia", 3),
		`nodecore_upstream_head_lag{chain="base-sepolia",upstream="behind"} 72853`,
		status("syncing-no-lag", "base-sepolia", 3),
		status("down", "ton-testnet", 4),
		status("weird", "ton-testnet", 7),
		`nodecore_upstream_heads{chain="ton-testnet",upstream="head-only"} 5`,
		status("zeta-extra", "sepolia", 1),
		status("alpha-extra", "sepolia", 1),
	)}}}
	opts := testOptions(f.start(t))

	result, err := newChecker(opts,
		nodecore.Upstream{ID: "ok", Chain: "sepolia"},
		nodecore.Upstream{ID: "young", Chain: "sepolia"},
		nodecore.Upstream{ID: "behind", Chain: "base-sepolia"},
		nodecore.Upstream{ID: "syncing-no-lag", Chain: "base-sepolia"},
		nodecore.Upstream{ID: "down", Chain: "ton-testnet"},
		nodecore.Upstream{ID: "weird", Chain: "ton-testnet"},
		nodecore.Upstream{ID: "head-only", Chain: "ton"},
		nodecore.Upstream{ID: "gone", Chain: "optimism-sepolia"},
	).Check(t.Context())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}

	want := []nodecore.UpstreamResult{
		{ID: "ok", Chain: "sepolia", Status: nodecore.StatusAvailable, Head: 11778631, HasHead: true, HasLag: true, Passed: true},
		{ID: "young", Chain: "sepolia", Status: nodecore.StatusImmature, Reason: "still immature 1m0s after nodecore start"},
		{ID: "behind", Chain: "base-sepolia", Status: nodecore.StatusSyncing, HeadLag: 72853, HasLag: true, Reason: "syncing: 72853 blocks behind head"},
		{ID: "syncing-no-lag", Chain: "base-sepolia", Status: nodecore.StatusSyncing, Reason: "syncing"},
		{ID: "down", Chain: "ton-testnet", Status: nodecore.StatusUnavailable, Reason: "unavailable: failing nodecore validation, see nodecore logs"},
		{ID: "weird", Chain: "ton-testnet", Status: nodecore.StatusUnknown, Reason: "unknown availability status"},
		// Chain comes from nodecore when reported; status is missing without
		// an availability sample.
		{ID: "head-only", Chain: "ton-testnet", Status: nodecore.StatusMissing, Head: 5, HasHead: true},
		{ID: "gone", Chain: "optimism-sepolia", Status: nodecore.StatusMissing},
	}
	if len(result.Upstreams) != len(want) {
		t.Fatalf("got %d results, want %d: %+v", len(result.Upstreams), len(want), result.Upstreams)
	}
	for i, w := range want {
		got := result.Upstreams[i]
		if w.Status == nodecore.StatusMissing {
			if !strings.Contains(got.Reason, "failed initial validation") {
				t.Errorf("%s: Reason = %q, want failed initial validation", w.ID, got.Reason)
			}
			w.Reason = got.Reason
		}
		if got != w {
			t.Errorf("Upstreams[%d] =\n  %+v\nwant\n  %+v", i, got, w)
		}
	}

	if result.Passed {
		t.Error("Passed = true, want false")
	}
	if got, want := strings.Join(result.Unexpected, ","), "alpha-extra,zeta-extra"; got != want {
		t.Errorf("Unexpected = %q, want %q", got, want)
	}
	if n := f.requests.Load(); n != 1 {
		t.Errorf("requests = %d, want 1 (nodecore started long ago, nothing to wait for)", n)
	}
}

func TestCheckAllAvailable(t *testing.T) {
	t.Parallel()
	// Fresh start, but everything has settled: no need to wait.
	f := &fakeMetrics{responses: []fakeResponse{{body: metrics(time.Now(),
		status("a", "sepolia", 1),
		status("b", "ton", 1),
	)}}}

	result, err := newChecker(testOptions(f.start(t)),
		nodecore.Upstream{ID: "a", Chain: "sepolia"},
		nodecore.Upstream{ID: "b", Chain: "ton"},
	).Check(t.Context())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !result.Passed || len(result.Unexpected) != 0 {
		t.Errorf("Passed = %v, Unexpected = %v; want true, none", result.Passed, result.Unexpected)
	}
	if n := f.requests.Load(); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
}

func TestCheckWaitsForSettling(t *testing.T) {
	t.Parallel()
	now := time.Now()
	f := &fakeMetrics{responses: []fakeResponse{
		{status: http.StatusServiceUnavailable, body: "starting"}, // metrics server not ready
		{body: metrics(now)}, // no upstreams registered yet
		{body: metrics(now, status("a", "sepolia", 2))},
		{body: metrics(now, status("a", "sepolia", 1))},
	}}

	result, err := newChecker(testOptions(f.start(t)), nodecore.Upstream{ID: "a", Chain: "sepolia"}).Check(t.Context())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !result.Passed {
		t.Errorf("Passed = false: %+v", result.Upstreams)
	}
	if n := f.requests.Load(); n != 4 {
		t.Errorf("requests = %d, want 4 (stop as soon as settled)", n)
	}
}

func TestCheckStopsWaitingAtDeadline(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		start time.Time
	}{
		{name: "deadline from nodecore start", start: time.Now()},
		{name: "deadline from check start without start metric", start: time.Time{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// "b" never appears, as with an upstream that failed initial validation.
			f := &fakeMetrics{responses: []fakeResponse{{body: metrics(tt.start, status("a", "sepolia", 1))}}}
			opts := testOptions(f.start(t))
			opts.Wait = 300 * time.Millisecond

			begin := time.Now()
			result, err := newChecker(opts,
				nodecore.Upstream{ID: "a", Chain: "sepolia"},
				nodecore.Upstream{ID: "b", Chain: "sepolia"},
			).Check(t.Context())
			elapsed := time.Since(begin)

			if err != nil {
				t.Fatalf("Check() error = %v", err)
			}
			if elapsed < 250*time.Millisecond || elapsed > 3*time.Second {
				t.Errorf("Check() took %v, want about the %v wait", elapsed, opts.Wait)
			}
			if result.Passed || result.Upstreams[1].Status != nodecore.StatusMissing {
				t.Errorf("Passed/b status = %v/%q, want false/%q", result.Passed, result.Upstreams[1].Status, nodecore.StatusMissing)
			}
			if n := f.requests.Load(); n < 2 {
				t.Errorf("requests = %d, want polling while waiting", n)
			}
		})
	}
}

func TestCheckErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response fakeResponse
		wantErr  []string
	}{
		{
			name:     "HTTP error",
			response: fakeResponse{status: http.StatusInternalServerError, body: "boom"},
			wantErr:  []string{"nodecore metrics are not reachable", "HTTP 500: boom"},
		},
		{name: "missing value", response: fakeResponse{body: "nodecore_upstream_heads\n"}, wantErr: []string{"line 1: sample without value"}},
		{name: "labels without value", response: fakeResponse{body: `nodecore_upstream_heads{upstream="a"}` + "\n"}, wantErr: []string{"sample without value"}},
		{name: "invalid value", response: fakeResponse{body: "process_start_time_seconds abc\n"}, wantErr: []string{`invalid value "abc"`}},
		{name: "unterminated label", response: fakeResponse{body: `nodecore_upstream_heads{upstream="a} 1` + "\n"}, wantErr: []string{`unterminated label value for "upstream"`}},
		{name: "unquoted label", response: fakeResponse{body: `nodecore_upstream_heads{upstream=a} 1` + "\n"}, wantErr: []string{"malformed label"}},
		{name: "label without equals", response: fakeResponse{body: `nodecore_upstream_heads{upstream} 1` + "\n"}, wantErr: []string{"malformed label"}},
		{name: "line too long", response: fakeResponse{body: "x " + strings.Repeat("1", 2<<20)}, wantErr: []string{"failed to read metrics"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeMetrics{responses: []fakeResponse{tt.response}}
			opts := testOptions(f.start(t))
			opts.ConnectTimeout = 100 * time.Millisecond

			begin := time.Now()
			result, err := newChecker(opts, nodecore.Upstream{ID: "a", Chain: "sepolia"}).Check(t.Context())
			// An unreachable nodecore must not hold the check for the whole
			// upstream wait window.
			if elapsed := time.Since(begin); elapsed < 100*time.Millisecond || elapsed > 5*time.Second {
				t.Errorf("Check() took %v, want about ConnectTimeout %v, not Wait %v", elapsed, opts.ConnectTimeout, opts.Wait)
			}
			if err == nil {
				t.Fatalf("Check() = %+v, want error", result)
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Check() error = %q, want it to contain %q", err, want)
				}
			}
			// Errors are retried until the connect window closes.
			if n := f.requests.Load(); n < 2 {
				t.Errorf("requests = %d, want retries", n)
			}
		})
	}
}

func TestCheckTransportErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		url     string
		wantErr string
	}{
		// Port 1 on loopback refuses connections.
		{name: "connection refused", url: "http://127.0.0.1:1/metrics", wantErr: "failed to fetch nodecore metrics"},
		{name: "malformed url", url: "http://[::1/metrics", wantErr: "failed to build metrics request"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts := testOptions(tt.url)
			opts.ConnectTimeout = 0

			_, err := newChecker(opts, nodecore.Upstream{ID: "a", Chain: "sepolia"}).Check(t.Context())
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Check() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestCheckContextCancelled(t *testing.T) {
	t.Parallel()
	f := &fakeMetrics{responses: []fakeResponse{{body: metrics(time.Now())}}}
	opts := testOptions(f.start(t))
	opts.PollInterval = time.Hour

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	_, err := newChecker(opts, nodecore.Upstream{ID: "a", Chain: "sepolia"}).Check(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Check() error = %v, want context.DeadlineExceeded", err)
	}
}

func TestCheckMetricsFormat(t *testing.T) {
	t.Parallel()
	body := strings.Join([]string{
		"# HELP something",
		"",
		"go_goroutines 42",
		`nodecore_upstream_heads{} 1`, // no upstream label: ignored
		`nodecore_upstream_availability_status{chain="x"} 1`,
		`nodecore_upstream_availability_status{ chain = "ton" , upstream="quo\"te\\d\nx" } 1 1790328054000`,
		`nodecore_upstream_availability_status{chain="sepolia",upstream="nan"} NaN`,
		`nodecore_upstream_rating{chain="sepolia",method="eth_call",upstream="other"} 5`,
		fmt.Sprintf("process_start_time_seconds %.2f", float64(longAgo.UnixMilli())/1000),
	}, "\n")
	f := &fakeMetrics{responses: []fakeResponse{{body: body}}}

	result, err := newChecker(testOptions(f.start(t)),
		nodecore.Upstream{ID: "quo\"te\\d\nx", Chain: "ton"},
		nodecore.Upstream{ID: "nan", Chain: "sepolia"},
	).Check(t.Context())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}

	if got := result.Upstreams[0]; got.Status != nodecore.StatusAvailable || got.Chain != "ton" {
		t.Errorf("escaped upstream = %+v, want available on ton", got)
	}
	if got := result.Upstreams[1]; got.Status != nodecore.StatusUnknown {
		t.Errorf("NaN upstream status = %q, want %q", got.Status, nodecore.StatusUnknown)
	}
	// Metrics other than the three upstream ones do not count as reported.
	if len(result.Unexpected) != 0 {
		t.Errorf("Unexpected = %v, want none", result.Unexpected)
	}
	if n := f.requests.Load(); n != 1 {
		t.Errorf("requests = %d, want 1 (start time parsed, window closed)", n)
	}
}

func TestDefaultOptions(t *testing.T) {
	t.Parallel()
	opts := nodecore.DefaultOptions()
	if opts.MetricsURL == "" || opts.Wait <= 0 || opts.ConnectTimeout <= 0 || opts.ConnectTimeout >= opts.Wait ||
		opts.PollInterval <= 0 || opts.RequestTimeout <= 0 {
		t.Errorf("DefaultOptions() = %+v, want URL and positive durations", opts)
	}
}
