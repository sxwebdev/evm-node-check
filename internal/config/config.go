package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	UpstreamConfig UpstreamConfig `yaml:"upstream-config"`
}

type UpstreamConfig struct {
	Upstreams []Upstream `yaml:"upstreams"`
}

type Upstream struct {
	ID         string      `yaml:"id"`
	Chain      string      `yaml:"chain"`
	Connectors []Connector `yaml:"connectors"`
}

type Connector struct {
	Type string `yaml:"type"`
	URL  string `yaml:"url"`
}

// NodeInfo is a flattened representation for the checker
type NodeInfo struct {
	ID      string
	Chain   string
	Address string
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	if len(cfg.UpstreamConfig.Upstreams) == 0 {
		return nil, fmt.Errorf("no upstreams configured in config file")
	}

	seenIDs := make(map[string]bool)
	seenURLs := make(map[string]bool)
	for _, upstream := range cfg.UpstreamConfig.Upstreams {
		if upstream.ID == "" {
			return nil, fmt.Errorf("upstream has empty id")
		}
		if seenIDs[upstream.ID] {
			return nil, fmt.Errorf("duplicate upstream id: %s", upstream.ID)
		}
		seenIDs[upstream.ID] = true

		if upstream.Chain == "" {
			return nil, fmt.Errorf("upstream %s has empty chain", upstream.ID)
		}
		if len(upstream.Connectors) == 0 {
			return nil, fmt.Errorf("upstream %s has no connectors", upstream.ID)
		}

		for _, connector := range upstream.Connectors {
			if connector.URL == "" {
				return nil, fmt.Errorf("upstream %s has empty connector URL", upstream.ID)
			}
			if seenURLs[connector.URL] {
				return nil, fmt.Errorf("duplicate connector URL: %s", connector.URL)
			}
			seenURLs[connector.URL] = true
		}
	}

	return &cfg, nil
}

// GetNodesByChain returns all nodes grouped by chain
func (c *Config) GetNodesByChain() map[string][]NodeInfo {
	result := make(map[string][]NodeInfo)

	for _, upstream := range c.UpstreamConfig.Upstreams {
		for _, connector := range upstream.Connectors {
			if connector.Type != "json-rpc" {
				continue
			}
			result[upstream.Chain] = append(result[upstream.Chain], NodeInfo{
				ID:      upstream.ID,
				Chain:   upstream.Chain,
				Address: connector.URL,
			})
		}
	}

	return result
}

// GetAllNodes returns all nodes as a flat list
func (c *Config) GetAllNodes() []NodeInfo {
	var result []NodeInfo

	for _, upstream := range c.UpstreamConfig.Upstreams {
		for _, connector := range upstream.Connectors {
			if connector.Type != "json-rpc" {
				continue
			}
			result = append(result, NodeInfo{
				ID:      upstream.ID,
				Chain:   upstream.Chain,
				Address: connector.URL,
			})
		}
	}

	return result
}
