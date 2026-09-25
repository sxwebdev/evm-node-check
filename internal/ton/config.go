package ton

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Network string

const (
	Mainnet Network = "mainnet"
	Testnet Network = "testnet"
)

const (
	DefaultMainnetEndpoint = "https://toncenter.com"
	DefaultTestnetEndpoint = "https://testnet.toncenter.com"
)

// globalID returns the TON global network id reported in block headers.
func (n Network) globalID() int32 {
	if n == Testnet {
		return -3
	}
	return -239
}

func (n Network) other() Network {
	if n == Mainnet {
		return Testnet
	}
	return Mainnet
}

type Config struct {
	Endpoints Endpoints `yaml:"endpoints"`
	Keys      []Key     `yaml:"keys"`
}

// Endpoints holds TON Center base URLs (without the /api/vN suffix).
type Endpoints struct {
	Mainnet string `yaml:"mainnet"`
	Testnet string `yaml:"testnet"`
}

func (e Endpoints) URL(n Network) string {
	if n == Testnet {
		return e.Testnet
	}
	return e.Mainnet
}

type Key struct {
	Name string `yaml:"name"`
	// Network is the network the key was issued for. Empty means autodetect.
	Network Network `yaml:"network"`
	Key     string  `yaml:"key"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	return Parse(data)
}

func Parse(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	if cfg.Endpoints.Mainnet == "" {
		cfg.Endpoints.Mainnet = DefaultMainnetEndpoint
	}
	if cfg.Endpoints.Testnet == "" {
		cfg.Endpoints.Testnet = DefaultTestnetEndpoint
	}
	for _, endpoint := range []*string{&cfg.Endpoints.Mainnet, &cfg.Endpoints.Testnet} {
		*endpoint = strings.TrimRight(*endpoint, "/")
		u, err := url.Parse(*endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("invalid endpoint URL: %q", *endpoint)
		}
	}

	if len(cfg.Keys) == 0 {
		return nil, fmt.Errorf("no keys configured in config file")
	}

	seenNames := make(map[string]bool)
	seenKeys := make(map[string]bool)
	for i, k := range cfg.Keys {
		if k.Name == "" {
			return nil, fmt.Errorf("key #%d has empty name", i+1)
		}
		if seenNames[k.Name] {
			return nil, fmt.Errorf("duplicate key name: %s", k.Name)
		}
		seenNames[k.Name] = true

		switch k.Network {
		case "", Mainnet, Testnet:
		default:
			return nil, fmt.Errorf("key %s has invalid network %q (expected mainnet, testnet or empty)", k.Name, k.Network)
		}

		if k.Key == "" {
			return nil, fmt.Errorf("key %s has empty key", k.Name)
		}
		if seenKeys[k.Key] {
			return nil, fmt.Errorf("key %s duplicates another key value", k.Name)
		}
		seenKeys[k.Key] = true
	}

	return &cfg, nil
}
