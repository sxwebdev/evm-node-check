package nodecore_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sxwebdev/evm-node-check/internal/nodecore"
)

func TestParse(t *testing.T) {
	t.Parallel()

	// Other sections, including their own `- id:` lists, must be ignored.
	data := `
server:
  metrics-port: 9093
cache:
  connectors:
    - id: redis-cache
      driver: redis
upstream-config:
  mode: strict
  upstreams:
    - id: eth-1
      chain: sepolia
      connectors:
        - type: json-rpc
          url: http://a
    # - id: eth-commented
    #   chain: sepolia
    - id: ton-1-v2
      chain: ton-testnet
      connectors:
        - type: rest
          url: https://testnet.toncenter.com/api/v2
    - id: ton-2-v2
      chain: ton-testnet
      connectors:
        - type: rest
          url: https://testnet.toncenter.com/api/v2
`
	cfg, err := nodecore.Parse([]byte(data))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	want := []nodecore.Upstream{
		{ID: "eth-1", Chain: "sepolia"},
		{ID: "ton-1-v2", Chain: "ton-testnet"},
		{ID: "ton-2-v2", Chain: "ton-testnet"},
	}
	if len(cfg.Upstreams) != len(want) {
		t.Fatalf("got %d upstreams %+v, want %d", len(cfg.Upstreams), cfg.Upstreams, len(want))
	}
	for i, w := range want {
		if cfg.Upstreams[i] != w {
			t.Errorf("Upstreams[%d] = %+v, want %+v", i, cfg.Upstreams[i], w)
		}
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{name: "malformed yaml", yaml: "upstream-config: [", wantErr: "failed to parse config file"},
		{name: "no upstream-config", yaml: "server: {port: 9090}", wantErr: "no upstreams configured"},
		{name: "empty upstreams", yaml: "upstream-config: {upstreams: []}", wantErr: "no upstreams configured"},
		{name: "empty id", yaml: "upstream-config: {upstreams: [{chain: sepolia}]}", wantErr: "upstream #1 has empty id"},
		{name: "duplicate id", yaml: "upstream-config: {upstreams: [{id: a, chain: x}, {id: a, chain: y}]}", wantErr: "duplicate upstream id: a"},
		{name: "empty chain", yaml: "upstream-config: {upstreams: [{id: a}]}", wantErr: "upstream a has empty chain"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := nodecore.Parse([]byte(tt.yaml))
			if err == nil {
				t.Fatalf("Parse() = %+v, want error containing %q", cfg, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Parse() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nodecore.yml")
	if err := os.WriteFile(path, []byte("upstream-config: {upstreams: [{id: a, chain: sepolia}]}"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := nodecore.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Upstreams) != 1 || cfg.Upstreams[0].ID != "a" {
		t.Errorf("Load() upstreams = %+v", cfg.Upstreams)
	}

	if _, err := nodecore.Load(filepath.Join(t.TempDir(), "missing.yml")); !os.IsNotExist(unwrapAll(err)) {
		t.Errorf("Load(missing) error = %v, want not-exist", err)
	}
}

func TestLoadExampleConfig(t *testing.T) {
	t.Parallel()
	cfg, err := nodecore.Load("../../nodecore/nodecore.example.yml")
	if err != nil {
		t.Fatalf("Load(nodecore.example.yml) error = %v", err)
	}
	if len(cfg.Upstreams) == 0 {
		t.Error("example config has no upstreams")
	}
}

func unwrapAll(err error) error {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		err = u.Unwrap()
	}
}
