package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// WorkspaceConfig holds top-level workspace settings.
type WorkspaceConfig struct {
	Root                string `yaml:"root"`
	HermesUser          string `yaml:"hermes_user"`
	HermesSourceProfile string `yaml:"hermes_source_profile"`
	MaxConcurrentRuns   int    `yaml:"max_concurrent_runs"`
}

const (
	DefaultHermesUser          = "cyberkitty"
	DefaultHermesSourceProfile = "qwen8"
	DefaultMaxConcurrentRuns   = 4
)

// LoadOrCreate loads workspace.yaml or creates defaults.
func LoadOrCreate(root string) (*WorkspaceConfig, error) {
	cfgPath := filepath.Join(root, "workspace.yaml")
	cfg := &WorkspaceConfig{
		Root:                root,
		HermesUser:          DefaultHermesUser,
		HermesSourceProfile: DefaultHermesSourceProfile,
		MaxConcurrentRuns:   DefaultMaxConcurrentRuns,
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(root, 0o755); err != nil {
				return nil, err
			}
			data, _ := yaml.Marshal(cfg)
			_ = os.WriteFile(cfgPath, data, 0o644)
			return cfg, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	// Fill defaults for older configs.
	changed := false
	if cfg.HermesUser == "" {
		cfg.HermesUser = DefaultHermesUser
		changed = true
	}
	if cfg.HermesSourceProfile == "" {
		cfg.HermesSourceProfile = DefaultHermesSourceProfile
		changed = true
	}
	if cfg.MaxConcurrentRuns <= 0 {
		cfg.MaxConcurrentRuns = DefaultMaxConcurrentRuns
		changed = true
	}
	if changed {
		data, _ := yaml.Marshal(cfg)
		_ = os.WriteFile(cfgPath, data, 0o644)
	}
	return cfg, nil
}
