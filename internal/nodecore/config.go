package nodecore

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the part of a nodecore config needed to know which upstreams
// nodecore is expected to run. Every other section is ignored.
type Config struct {
	Upstreams []Upstream
}

type Upstream struct {
	ID    string `yaml:"id"`
	Chain string `yaml:"chain"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	return Parse(data)
}

func Parse(data []byte) (*Config, error) {
	var raw struct {
		UpstreamConfig struct {
			Upstreams []Upstream `yaml:"upstreams"`
		} `yaml:"upstream-config"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	upstreams := raw.UpstreamConfig.Upstreams
	if len(upstreams) == 0 {
		return nil, fmt.Errorf("no upstreams configured in config file")
	}

	seen := make(map[string]bool)
	for i, u := range upstreams {
		if u.ID == "" {
			return nil, fmt.Errorf("upstream #%d has empty id", i+1)
		}
		if seen[u.ID] {
			return nil, fmt.Errorf("duplicate upstream id: %s", u.ID)
		}
		seen[u.ID] = true
		if u.Chain == "" {
			return nil, fmt.Errorf("upstream %s has empty chain", u.ID)
		}
	}

	return &Config{Upstreams: upstreams}, nil
}
