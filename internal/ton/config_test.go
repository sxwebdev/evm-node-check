package ton_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sxwebdev/evm-node-check/internal/ton"
)

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		yaml          string
		wantEndpoints ton.Endpoints
		wantKeys      []ton.Key
	}{
		{
			name: "defaults endpoints",
			yaml: `
keys:
  - name: a
    network: mainnet
    key: k1
  - name: b
    network: testnet
    key: k2
  - name: c
    key: k3
`,
			wantEndpoints: ton.Endpoints{Mainnet: ton.DefaultMainnetEndpoint, Testnet: ton.DefaultTestnetEndpoint},
			wantKeys: []ton.Key{
				{Name: "a", Network: ton.Mainnet, Key: "k1"},
				{Name: "b", Network: ton.Testnet, Key: "k2"},
				{Name: "c", Key: "k3"},
			},
		},
		{
			name: "custom endpoints with trailing slashes",
			yaml: `
endpoints:
  mainnet: https://ton.example.com/
  testnet: http://127.0.0.1:8081//
keys:
  - name: a
    key: k1
`,
			wantEndpoints: ton.Endpoints{Mainnet: "https://ton.example.com", Testnet: "http://127.0.0.1:8081"},
			wantKeys:      []ton.Key{{Name: "a", Key: "k1"}},
		},
		{
			name: "one endpoint overridden",
			yaml: `
endpoints:
  testnet: https://testnet.example.com
keys:
  - name: a
    key: k1
`,
			wantEndpoints: ton.Endpoints{Mainnet: ton.DefaultMainnetEndpoint, Testnet: "https://testnet.example.com"},
			wantKeys:      []ton.Key{{Name: "a", Key: "k1"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := ton.Parse([]byte(tt.yaml))
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if cfg.Endpoints != tt.wantEndpoints {
				t.Errorf("Endpoints = %+v, want %+v", cfg.Endpoints, tt.wantEndpoints)
			}
			if len(cfg.Keys) != len(tt.wantKeys) {
				t.Fatalf("got %d keys, want %d", len(cfg.Keys), len(tt.wantKeys))
			}
			for i, want := range tt.wantKeys {
				if cfg.Keys[i] != want {
					t.Errorf("Keys[%d] = %+v, want %+v", i, cfg.Keys[i], want)
				}
			}
			if got := cfg.Endpoints.URL(ton.Mainnet); got != tt.wantEndpoints.Mainnet {
				t.Errorf("URL(mainnet) = %q, want %q", got, tt.wantEndpoints.Mainnet)
			}
			if got := cfg.Endpoints.URL(ton.Testnet); got != tt.wantEndpoints.Testnet {
				t.Errorf("URL(testnet) = %q, want %q", got, tt.wantEndpoints.Testnet)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{name: "malformed yaml", yaml: "keys: [", wantErr: "failed to parse config file"},
		{name: "no keys", yaml: "keys: []", wantErr: "no keys configured"},
		{name: "empty name", yaml: "keys: [{key: k1}]", wantErr: "key #1 has empty name"},
		{name: "duplicate name", yaml: "keys: [{name: a, key: k1}, {name: a, key: k2}]", wantErr: "duplicate key name: a"},
		{name: "unknown network", yaml: "keys: [{name: a, network: devnet, key: k1}]", wantErr: `invalid network "devnet"`},
		{name: "network is case sensitive", yaml: "keys: [{name: a, network: Mainnet, key: k1}]", wantErr: `invalid network "Mainnet"`},
		{name: "empty key", yaml: "keys: [{name: a}]", wantErr: "key a has empty key"},
		{name: "duplicate key", yaml: "keys: [{name: a, key: k1}, {name: b, key: k1}]", wantErr: "key b duplicates another key value"},
		{name: "endpoint without scheme", yaml: "endpoints: {mainnet: toncenter.com}\nkeys: [{name: a, key: k1}]", wantErr: `invalid endpoint URL: "toncenter.com"`},
		{name: "endpoint with bad scheme", yaml: "endpoints: {testnet: 'ftp://x'}\nkeys: [{name: a, key: k1}]", wantErr: "invalid endpoint URL"},
		{name: "unparsable endpoint", yaml: "endpoints: {mainnet: 'http://[::1'}\nkeys: [{name: a, key: k1}]", wantErr: "invalid endpoint URL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := ton.Parse([]byte(tt.yaml))
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

	path := filepath.Join(t.TempDir(), "ton.yaml")
	if err := os.WriteFile(path, []byte("keys: [{name: a, network: testnet, key: k1}]"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := ton.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Keys) != 1 || cfg.Keys[0].Network != ton.Testnet {
		t.Errorf("Load() keys = %+v", cfg.Keys)
	}

	if _, err := ton.Load(filepath.Join(t.TempDir(), "missing.yaml")); !os.IsNotExist(unwrapAll(err)) {
		t.Errorf("Load(missing) error = %v, want not-exist", err)
	}
}

func TestLoadExampleConfig(t *testing.T) {
	t.Parallel()
	cfg, err := ton.Load("../../ton.example.yaml")
	if err != nil {
		t.Fatalf("Load(ton.example.yaml) error = %v", err)
	}
	if len(cfg.Keys) == 0 {
		t.Error("example config has no keys")
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
