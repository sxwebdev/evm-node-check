package ton_test

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sxwebdev/evm-node-check/internal/ton"
)

const masterShard = "-9223372036854775808"

// fakeNet emulates one TON Center network. Like the real service, it serves
// requests without a key anonymously and rejects unknown keys with 401.
type fakeNet struct {
	keys      map[string]bool
	globalID  int32
	v2Seqno   uint32
	v3Seqno   uint32
	blockTime time.Time
	// override replaces the default handler for a path.
	override map[string]http.HandlerFunc

	// rps limits accepted requests per fixed one-second window (0 = no limit).
	rps int

	mu          sync.Mutex
	rateLimited int // number of upcoming requests to answer with 429
	window      time.Time
	inWindow    int

	requests atomic.Int32
}

func newFakeNet(globalID int32, keys ...string) *fakeNet {
	f := &fakeNet{
		keys:      make(map[string]bool),
		globalID:  globalID,
		v2Seqno:   1000,
		v3Seqno:   1002,
		blockTime: time.Now(),
		override:  make(map[string]http.HandlerFunc),
	}
	for _, k := range keys {
		f.keys[k] = true
	}
	return f
}

func (f *fakeNet) start(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (f *fakeNet) serve(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)

	if key := r.Header.Get("X-API-Key"); key != "" && !f.keys[key] {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"ok": false, "error": "API key does not exist","code":401}`)
		return
	}

	f.mu.Lock()
	limited := f.rateLimited > 0
	if limited {
		f.rateLimited--
	}
	if f.rps > 0 && !limited {
		if now := time.Now(); now.Sub(f.window) >= time.Second {
			f.window, f.inWindow = now, 0
		}
		f.inWindow++
		limited = f.inWindow > f.rps
	}
	f.mu.Unlock()
	if limited {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"ok": false, "error": "Ratelimit exceed","code":429}`)
		return
	}

	if h, ok := f.override[r.URL.Path]; ok {
		h(w, r)
		return
	}

	switch r.URL.Path {
	case "/api/v2/getMasterchainInfo":
		fmt.Fprintf(w, `{"ok":true,"result":{"last":{"workchain":-1,"shard":%q,"seqno":%d}}}`, masterShard, f.v2Seqno)
	case "/api/v2/getBlockHeader":
		q := r.URL.Query()
		if q.Get("workchain") != "-1" || q.Get("shard") != masterShard || q.Get("seqno") != strconv.Itoa(int(f.v2Seqno)) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"ok":false,"error":"unexpected block id %s","code":400}`, r.URL.RawQuery)
			return
		}
		fmt.Fprintf(w, `{"ok":true,"result":{"global_id":%d,"gen_utime":%d}}`, f.globalID, f.blockTime.Unix())
	case "/api/v3/masterchainInfo":
		fmt.Fprintf(w, `{"last":{"seqno":%d,"global_id":%d,"gen_utime":"%d"}}`, f.v3Seqno, f.globalID, f.blockTime.Unix())
	default:
		http.NotFound(w, r)
	}
}

func testOptions() ton.Options {
	return ton.Options{
		Timeout:     5 * time.Second,
		MaxBlockAge: time.Minute,
		MaxSeqnoGap: 10,
		MaxRetries:  2,
		RetryDelay:  time.Millisecond,
	}
}

func newChecker(t *testing.T, mainnet, testnet *fakeNet, opts ton.Options, keys ...ton.Key) *ton.Checker {
	t.Helper()
	cfg := &ton.Config{
		Endpoints: ton.Endpoints{Mainnet: mainnet.start(t), Testnet: testnet.start(t)},
		Keys:      keys,
	}
	return ton.New(cfg, opts, slog.New(slog.DiscardHandler))
}

func newCheckerFromConfig(cfg *ton.Config) *ton.Checker {
	return ton.New(cfg, testOptions(), slog.New(slog.DiscardHandler))
}
