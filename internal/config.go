package internal

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config holds the top-level orchestrator configuration, typically loaded from
// a plugins.yaml file.
type Config struct {
	ListenAddr string         `yaml:"listen_addr"`
	CertsDir   string         `yaml:"certs_dir"`
	Plugins    []PluginConfig `yaml:"plugins"`
}

// PluginConfig describes a single plugin binary that the orchestrator should
// launch and manage.
type PluginConfig struct {
	ID              string            `yaml:"id"`
	Binary          string            `yaml:"binary"`
	Args            []string          `yaml:"args,omitempty"`
	Env             map[string]string `yaml:"env,omitempty"`
	Config          map[string]string `yaml:"config,omitempty"` // passed during Boot
	Enabled         bool              `yaml:"enabled"`
	ProvidesStorage []string          `yaml:"provides_storage,omitempty"`
}

// LoadConfig reads and parses a YAML configuration file at the given path.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	// Apply defaults.
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "localhost:50100"
	}
	if cfg.CertsDir == "" {
		cfg.CertsDir = "~/.orchestra/certs"
	}

	return &cfg, nil
}

// LoadConfigFromBytes parses YAML configuration from raw bytes. This is useful
// for testing without requiring a file on disk.
func LoadConfigFromBytes(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "localhost:50100"
	}
	if cfg.CertsDir == "" {
		cfg.CertsDir = "~/.orchestra/certs"
	}

	return &cfg, nil
}
