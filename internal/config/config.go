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

	// Check for duplicate addresses
	seen := make(map[string]bool)
	for _, upstream := range cfg.UpstreamConfig.Upstreams {
		for _, connector := range upstream.Connectors {
			if connector.URL == "" {
				return nil, fmt.Errorf("upstream %s has empty connector URL", upstream.ID)
			}
			if seen[connector.URL] {
				return nil, fmt.Errorf("duplicate connector URL: %s", connector.URL)
			}
			seen[connector.URL] = true
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
