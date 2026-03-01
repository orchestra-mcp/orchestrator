package internal

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config holds the top-level orchestrator configuration, typically loaded from
// a plugins.yaml file.
type Config struct {
	ListenAddr string         `yaml:"listen_addr"`
	TCPAddr    string         `yaml:"tcp_addr"`
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
	ProvidesAI      []string          `yaml:"provides_ai,omitempty"`
	DependsOn       []string          `yaml:"depends_on,omitempty"`
}

// LoadConfig reads and parses a YAML configuration file at the given path.
// Relative binary paths in plugin configs are resolved relative to the
// config file's directory, so the orchestrator works correctly regardless
// of the process working directory (e.g. when launched by a macOS app).
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

	// Resolve relative binary paths and workspace args relative to the config file's dir.
	configDir := filepath.Dir(filepath.Clean(path))
	for i, p := range cfg.Plugins {
		if p.Binary != "" && !filepath.IsAbs(p.Binary) {
			cfg.Plugins[i].Binary = filepath.Join(configDir, p.Binary)
		}
		for j, arg := range p.Args {
			// Rewrite --workspace=. and --workspace=<relative> to absolute paths.
			if arg == "--workspace=." {
				cfg.Plugins[i].Args[j] = "--workspace=" + configDir
			}
		}
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
