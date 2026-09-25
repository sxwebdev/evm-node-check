package ton_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sxwebdev/evm-node-check/internal/ton"
)

const (
	mainnetID = -239
	testnetID = -3

	mainKey = "mainnet-key-0000000000000000000000000000000000000000000000000001"
	testKey = "testnet-key-0000000000000000000000000000000000000000000000000002"
	badKey  = "invalid-key-0000000000000000000000000000000000000000000000000003"
)

func checkOne(t *testing.T, mainnet, testnet *fakeNet, opts ton.Options, key ton.Key) ton.KeyResult {
	t.Helper()
	result := newChecker(t, mainnet, testnet, opts, key).Check(t.Context())
	if len(result.Keys) != 1 {
		t.Fatalf("got %d key results, want 1", len(result.Keys))
	}
	if result.Passed != result.Keys[0].Passed {
		t.Errorf("CheckResult.Passed = %v, key Passed = %v", result.Passed, result.Keys[0].Passed)
	}
	return result.Keys[0]
}

func assertReason(t *testing.T, got ton.KeyResult, wantReason []string) {
	t.Helper()
	if len(wantReason) == 0 && got.Reason != "" {
		t.Errorf("Reason = %q, want empty", got.Reason)
	}
	for _, want := range wantReason {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("Reason = %q, want it to contain %q", got.Reason, want)
		}
	}
}

func TestCheckNetworkDetection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		key          ton.Key
		wantStatus   ton.Status
		wantDetected ton.Network
		wantPassed   bool
		wantReason   []string
	}{
		{
			name:         "mainnet key declared mainnet",
			key:          ton.Key{Name: "k", Network: ton.Mainnet, Key: mainKey},
			wantStatus:   ton.StatusValid,
			wantDetected: ton.Mainnet,
			wantPassed:   true,
		},
		{
			name:         "testnet key declared testnet",
			key:          ton.Key{Name: "k", Network: ton.Testnet, Key: testKey},
			wantStatus:   ton.StatusValid,
			wantDetected: ton.Testnet,
			wantPassed:   true,
		},
		{
			name:         "mainnet key autodetected",
			key:          ton.Key{Name: "k", Key: mainKey},
			wantStatus:   ton.StatusValid,
			wantDetected: ton.Mainnet,
			wantPassed:   true,
		},
		{
			name:         "testnet key autodetected",
			key:          ton.Key{Name: "k", Key: testKey},
			wantStatus:   ton.StatusValid,
			wantDetected: ton.Testnet,
			wantPassed:   true,
		},
		{
			name:         "testnet key declared mainnet",
			key:          ton.Key{Name: "k", Network: ton.Mainnet, Key: testKey},
			wantStatus:   ton.StatusValid,
			wantDetected: ton.Testnet,
			wantReason:   []string{"key belongs to testnet, not mainnet"},
		},
		{
			name:         "mainnet key declared testnet",
			key:          ton.Key{Name: "k", Network: ton.Testnet, Key: mainKey},
			wantStatus:   ton.StatusValid,
			wantDetected: ton.Mainnet,
			wantReason:   []string{"key belongs to mainnet, not testnet"},
		},
		{
			name:       "unknown key declared",
			key:        ton.Key{Name: "k", Network: ton.Mainnet, Key: badKey},
			wantStatus: ton.StatusInvalid,
			wantReason: []string{"rejected on both mainnet and testnet"},
		},
		{
			name:       "unknown key autodetect",
			key:        ton.Key{Name: "k", Key: badKey},
			wantStatus: ton.StatusInvalid,
			wantReason: []string{"rejected on both mainnet and testnet"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mainnet := newFakeNet(mainnetID, mainKey)
			testnet := newFakeNet(testnetID, testKey)

			got := checkOne(t, mainnet, testnet, testOptions(), tt.key)

			if got.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q (reason: %s)", got.Status, tt.wantStatus, got.Reason)
			}
			if got.DetectedNetwork != tt.wantDetected {
				t.Errorf("DetectedNetwork = %q, want %q", got.DetectedNetwork, tt.wantDetected)
			}
			if got.Passed != tt.wantPassed {
				t.Errorf("Passed = %v, want %v (reason: %s)", got.Passed, tt.wantPassed, got.Reason)
			}
			if got.Network != tt.key.Network || got.Name != tt.key.Name {
				t.Errorf("Name/Network = %q/%q, want %q/%q", got.Name, got.Network, tt.key.Name, tt.key.Network)
			}
			if got.MaskedKey != ton.MaskKey(tt.key.Key) {
				t.Errorf("MaskedKey = %q, want %q", got.MaskedKey, ton.MaskKey(tt.key.Key))
			}
			assertReason(t, got, tt.wantReason)
		})
	}
}

func TestCheckValidKeyMetrics(t *testing.T) {
	t.Parallel()
	mainnet := newFakeNet(mainnetID, mainKey)
	mainnet.blockTime = time.Now().Add(-5 * time.Second).Truncate(time.Second)
	testnet := newFakeNet(testnetID)

	got := checkOne(t, mainnet, testnet, testOptions(), ton.Key{Name: "k", Network: ton.Mainnet, Key: mainKey})

	if !got.Passed {
		t.Fatalf("Passed = false, reason: %s", got.Reason)
	}
	if got.V2Seqno != mainnet.v2Seqno || got.V3Seqno != mainnet.v3Seqno {
		t.Errorf("seqno v2/v3 = %d/%d, want %d/%d", got.V2Seqno, got.V3Seqno, mainnet.v2Seqno, mainnet.v3Seqno)
	}
	if !got.V2BlockTime.Equal(mainnet.blockTime) || !got.V3BlockTime.Equal(mainnet.blockTime) {
		t.Errorf("block time v2/v3 = %v/%v, want %v", got.V2BlockTime, got.V3BlockTime, mainnet.blockTime)
	}
	if got.Latency <= 0 {
		t.Errorf("Latency = %v, want > 0", got.Latency)
	}
	// masterchainInfo v2, getBlockHeader, masterchainInfo v3; testnet untouched.
	if n := mainnet.requests.Load(); n != 3 {
		t.Errorf("mainnet requests = %d, want 3", n)
	}
	if n := testnet.requests.Load(); n != 0 {
		t.Errorf("testnet requests = %d, want 0", n)
	}
}

func TestCheckRateLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		rateLimited int
		wantStatus  ton.Status
		wantPassed  bool
		// wantRequests counts all mainnet requests, including 3 for a healthy key.
		wantRequests int32
	}{
		{name: "recovers after retries", rateLimited: 2, wantStatus: ton.StatusValid, wantPassed: true, wantRequests: 5},
		{name: "retries exhausted", rateLimited: 100, wantStatus: ton.StatusRateLimited, wantRequests: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mainnet := newFakeNet(mainnetID, mainKey)
			mainnet.rateLimited = tt.rateLimited
			testnet := newFakeNet(testnetID)

			got := checkOne(t, mainnet, testnet, testOptions(), ton.Key{Name: "k", Network: ton.Mainnet, Key: mainKey})

			if got.Status != tt.wantStatus || got.Passed != tt.wantPassed {
				t.Errorf("Status/Passed = %q/%v, want %q/%v (reason: %s)",
					got.Status, got.Passed, tt.wantStatus, tt.wantPassed, got.Reason)
			}
			if n := mainnet.requests.Load(); n != tt.wantRequests {
				t.Errorf("mainnet requests = %d, want %d", n, tt.wantRequests)
			}
			// A declared network that is rate limited must not fall through to
			// the other network: the key's validity is simply unknown.
			if n := testnet.requests.Load(); n != 0 {
				t.Errorf("testnet requests = %d, want 0", n)
			}
		})
	}
}

func TestCheckRateLimitRespectsTimeout(t *testing.T) {
	t.Parallel()
	mainnet := newFakeNet(mainnetID, mainKey)
	mainnet.rateLimited = 100
	testnet := newFakeNet(testnetID)
	opts := testOptions()
	opts.Timeout = 50 * time.Millisecond
	opts.MaxRetries = 100
	opts.RetryDelay = time.Hour

	start := time.Now()
	got := checkOne(t, mainnet, testnet, opts, ton.Key{Name: "k", Network: ton.Mainnet, Key: mainKey})

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("check took %v, want it bounded by the %v timeout", elapsed, opts.Timeout)
	}
	if got.Status != ton.StatusRateLimited {
		t.Errorf("Status = %q, want %q (reason: %s)", got.Status, ton.StatusRateLimited, got.Reason)
	}
}

func TestCheckUnavailable(t *testing.T) {
	t.Parallel()

	serverError := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, `{"ok":false,"error":"LITE_SERVER_UNKNOWN: timeout","code":502}`)
	}

	tests := []struct {
		name       string
		key        ton.Key
		mainnet    map[string]http.HandlerFunc
		testnet    map[string]http.HandlerFunc
		wantStatus ton.Status
		wantReason []string
		// wantTestnetRequests guards against probing the other network when
		// the declared one failed for a reason unrelated to the key.
		wantTestnetRequests int32
	}{
		{
			name:                "declared network is down",
			key:                 ton.Key{Name: "k", Network: ton.Mainnet, Key: mainKey},
			mainnet:             map[string]http.HandlerFunc{"/api/v2/getMasterchainInfo": serverError},
			wantStatus:          ton.StatusUnavailable,
			wantReason:          []string{"mainnet: HTTP 502: LITE_SERVER_UNKNOWN: timeout"},
			wantTestnetRequests: 0,
		},
		{
			name:                "autodetect, testnet key, mainnet down",
			key:                 ton.Key{Name: "k", Key: testKey},
			mainnet:             map[string]http.HandlerFunc{"/api/v2/getMasterchainInfo": serverError},
			wantStatus:          ton.StatusValid,
			wantTestnetRequests: 3,
		},
		{
			name:                "autodetect, mainnet down, testnet rejects",
			key:                 ton.Key{Name: "k", Key: mainKey},
			mainnet:             map[string]http.HandlerFunc{"/api/v2/getMasterchainInfo": serverError},
			wantStatus:          ton.StatusUnavailable,
			wantReason:          []string{"mainnet: HTTP 502"},
			wantTestnetRequests: 1,
		},
		{
			name:                "autodetect, mainnet rejects, testnet down",
			key:                 ton.Key{Name: "k", Key: testKey},
			testnet:             map[string]http.HandlerFunc{"/api/v2/getMasterchainInfo": serverError},
			wantStatus:          ton.StatusUnavailable,
			wantReason:          []string{"mainnet: API key rejected", "testnet: HTTP 502"},
			wantTestnetRequests: 1,
		},
		{
			name: "non-JSON success body",
			key:  ton.Key{Name: "k", Network: ton.Mainnet, Key: mainKey},
			mainnet: map[string]http.HandlerFunc{"/api/v2/getMasterchainInfo": func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, "<html>maintenance</html>")
			}},
			wantStatus: ton.StatusUnavailable,
			wantReason: []string{"failed to decode response"},
		},
		{
			name: "long non-JSON error body is truncated",
			key:  ton.Key{Name: "k", Network: ton.Mainnet, Key: mainKey},
			mainnet: map[string]http.HandlerFunc{"/api/v2/getMasterchainInfo": func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprint(w, strings.Repeat("x", 500))
			}},
			wantStatus: ton.StatusUnavailable,
			wantReason: []string{"HTTP 503: " + strings.Repeat("x", 200) + "…"},
		},
		{
			name: "v2 envelope error",
			key:  ton.Key{Name: "k", Network: ton.Mainnet, Key: mainKey},
			mainnet: map[string]http.HandlerFunc{"/api/v2/getMasterchainInfo": func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, `{"ok":false,"error":"internal","code":500}`)
			}},
			wantStatus: ton.StatusUnavailable,
			wantReason: []string{"getMasterchainInfo returned error (code 500): internal"},
		},
		{
			name: "v2 envelope rejects key",
			key:  ton.Key{Name: "k", Network: ton.Mainnet, Key: mainKey},
			mainnet: map[string]http.HandlerFunc{"/api/v2/getMasterchainInfo": func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, `{"ok":false,"error":"API key does not exist","code":401}`)
			}},
			wantStatus:          ton.StatusInvalid,
			wantReason:          []string{"rejected on both"},
			wantTestnetRequests: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mainnet := newFakeNet(mainnetID, mainKey)
			for path, h := range tt.mainnet {
				mainnet.override[path] = h
			}
			testnet := newFakeNet(testnetID, testKey)
			for path, h := range tt.testnet {
				testnet.override[path] = h
			}

			got := checkOne(t, mainnet, testnet, testOptions(), tt.key)

			if got.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q (reason: %s)", got.Status, tt.wantStatus, got.Reason)
			}
			if wantPassed := tt.wantStatus == ton.StatusValid; got.Passed != wantPassed {
				t.Errorf("Passed = %v, want %v (reason: %s)", got.Passed, wantPassed, got.Reason)
			}
			if n := testnet.requests.Load(); n != tt.wantTestnetRequests {
				t.Errorf("testnet requests = %d, want %d", n, tt.wantTestnetRequests)
			}
			assertReason(t, got, tt.wantReason)
		})
	}
}

func TestCheckHealth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setup      func(mainnet *fakeNet)
		opts       func(*ton.Options)
		wantPassed bool
		wantReason []string
	}{
		{
			name: "stale blocks",
			setup: func(f *fakeNet) {
				f.blockTime = time.Now().Add(-10 * time.Minute)
			},
			wantReason: []string{"v2 last block stale", "v3 last block stale"},
		},
		{
			name: "stale blocks ignored when check disabled",
			setup: func(f *fakeNet) {
				f.blockTime = time.Now().Add(-10 * time.Minute)
			},
			opts:       func(o *ton.Options) { o.MaxBlockAge = 0 },
			wantPassed: true,
		},
		{
			name:       "endpoint serves wrong network",
			setup:      func(f *fakeNet) { f.globalID = testnetID },
			wantReason: []string{"v2 global_id mismatch: expected -239 for mainnet, got -3", "v3 global_id mismatch"},
		},
		{
			name:       "v3 lags behind v2",
			setup:      func(f *fakeNet) { f.v2Seqno, f.v3Seqno = 1000, 989 },
			wantReason: []string{"v2/v3 seqno gap too large: v2=1000 v3=989 (max allowed: 10)"},
		},
		{
			name:       "v2 lags behind v3",
			setup:      func(f *fakeNet) { f.v2Seqno, f.v3Seqno = 989, 1000 },
			wantReason: []string{"v2/v3 seqno gap too large"},
		},
		{
			name:       "seqno gap at the limit",
			setup:      func(f *fakeNet) { f.v2Seqno, f.v3Seqno = 1000, 990 },
			wantPassed: true,
		},
		{
			name: "v3 unavailable",
			setup: func(f *fakeNet) {
				f.override["/api/v3/masterchainInfo"] = func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
					fmt.Fprint(w, `{"error":"indexer down"}`)
				}
			},
			wantReason: []string{"v3 masterchainInfo: HTTP 500: indexer down"},
		},
		{
			name: "v2 block header unavailable",
			setup: func(f *fakeNet) {
				f.override["/api/v2/getBlockHeader"] = func(w http.ResponseWriter, _ *http.Request) {
					fmt.Fprint(w, `{"ok":false,"error":"block not found","code":404}`)
				}
			},
			wantReason: []string{"v2 getBlockHeader: getBlockHeader returned error (code 404): block not found"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mainnet := newFakeNet(mainnetID, mainKey)
			tt.setup(mainnet)
			opts := testOptions()
			if tt.opts != nil {
				tt.opts(&opts)
			}

			got := checkOne(t, mainnet, newFakeNet(testnetID), opts, ton.Key{Name: "k", Network: ton.Mainnet, Key: mainKey})

			// Health problems never change the verdict on the key itself.
			if got.Status != ton.StatusValid {
				t.Errorf("Status = %q, want %q", got.Status, ton.StatusValid)
			}
			if got.Passed != tt.wantPassed {
				t.Errorf("Passed = %v, want %v (reason: %s)", got.Passed, tt.wantPassed, got.Reason)
			}
			assertReason(t, got, tt.wantReason)
		})
	}
}

func TestCheckMultipleKeys(t *testing.T) {
	t.Parallel()
	mainnet := newFakeNet(mainnetID, mainKey)
	testnet := newFakeNet(testnetID, testKey)

	result := newChecker(t, mainnet, testnet, testOptions(),
		ton.Key{Name: "main", Network: ton.Mainnet, Key: mainKey},
		ton.Key{Name: "bad", Key: badKey},
		ton.Key{Name: "test", Network: ton.Testnet, Key: testKey},
	).Check(t.Context())

	if result.Passed {
		t.Error("CheckResult.Passed = true, want false when any key fails")
	}
	want := []struct {
		name   string
		status ton.Status
	}{
		{"main", ton.StatusValid},
		{"bad", ton.StatusInvalid},
		{"test", ton.StatusValid},
	}
	if len(result.Keys) != len(want) {
		t.Fatalf("got %d key results, want %d", len(result.Keys), len(want))
	}
	for i, w := range want {
		if got := result.Keys[i]; got.Name != w.name || got.Status != w.status {
			t.Errorf("Keys[%d] = %q/%q, want %q/%q", i, got.Name, got.Status, w.name, w.status)
		}
	}
}

func TestCheckAllPassed(t *testing.T) {
	t.Parallel()
	result := newChecker(t, newFakeNet(mainnetID, mainKey), newFakeNet(testnetID, testKey), testOptions(),
		ton.Key{Name: "main", Key: mainKey},
		ton.Key{Name: "test", Key: testKey},
	).Check(t.Context())

	if !result.Passed {
		t.Errorf("CheckResult.Passed = false, want true: %+v", result.Keys)
	}
}

func TestMaskKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		key  string
		want string
	}{
		{key: "", want: ""},
		{key: "short", want: "*****"},
		{key: "123456789012", want: "************"},
		{key: "1234567890123", want: "1234…0123"},
		{key: mainKey, want: "main…0001"},
	}
	for _, tt := range tests {
		if got := ton.MaskKey(tt.key); got != tt.want {
			t.Errorf("MaskKey(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestDefaultOptions(t *testing.T) {
	t.Parallel()
	opts := ton.DefaultOptions()
	if opts.Timeout <= 0 || opts.MaxBlockAge <= 0 || opts.MaxRetries < 1 || opts.RetryDelay <= 0 {
		t.Errorf("DefaultOptions() = %+v, want positive timeout, block age, retries and delay", opts)
	}
}

func TestCheckTransportErrors(t *testing.T) {
	t.Parallel()

	truncatedBody := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Promise more bytes than are sent so reading the body fails.
		w.Header().Set("Content-Length", "100")
		fmt.Fprint(w, `{"ok":`)
	}))
	t.Cleanup(truncatedBody.Close)

	tests := []struct {
		name       string
		endpoint   string
		wantReason string
	}{
		// Port 1 on loopback refuses connections.
		{name: "connection refused", endpoint: "http://127.0.0.1:1", wantReason: "request failed"},
		// Parse rejects such URLs; this guards a Config built by hand.
		{name: "malformed endpoint", endpoint: "http://[::1", wantReason: "failed to build request"},
		{name: "truncated body", endpoint: truncatedBody.URL, wantReason: "failed to read response"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &ton.Config{
				Endpoints: ton.Endpoints{Mainnet: tt.endpoint, Testnet: tt.endpoint},
				Keys:      []ton.Key{{Name: "k", Network: ton.Mainnet, Key: mainKey}},
			}
			result := newCheckerFromConfig(cfg).Check(t.Context())

			got := result.Keys[0]
			if got.Status != ton.StatusUnavailable || got.Passed || result.Passed {
				t.Errorf("Status/Passed = %q/%v, want %q/false (reason: %s)", got.Status, got.Passed, ton.StatusUnavailable, got.Reason)
			}
			assertReason(t, got, []string{tt.wantReason})
		})
	}
}
