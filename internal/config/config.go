package config

import (
	"os"
	"os/user"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// WorkspaceConfig holds top-level workspace settings.
type WorkspaceConfig struct {
	Root                string `yaml:"root"`
	HermesUser          string `yaml:"hermes_user"`
	HermesSourceProfile string `yaml:"hermes_source_profile"`
	MaxConcurrentRuns   int    `yaml:"max_concurrent_runs"`
	StubMode            bool   `yaml:"stub_mode"`
}

const (
	DefaultHermesSourceProfile = "qwen8"
	DefaultMaxConcurrentRuns   = 4
)

// DefaultHermesUser returns the user that owns Hermes profiles.
// It respects CONTROL_ROOM_HERMES_USER, then the current USER, then the
// effective username reported by the OS. This avoids hard-coding a specific
// account.
func DefaultHermesUser() string {
	if u := os.Getenv("CONTROL_ROOM_HERMES_USER"); u != "" {
		return u
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	u, _ := user.Current()
	if u != nil {
		return u.Username
	}
	return ""
}

// DefaultWorkspace returns a default workspace root.
// It respects CONTROL_ROOM_WORKSPACE, then $HOME/.control-room.
func DefaultWorkspace() string {
	if w := os.Getenv("CONTROL_ROOM_WORKSPACE"); w != "" {
		return w
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		return filepath.Join(home, ".control-room")
	}
	return "/tmp/control-room"
}

// LoadOrCreate loads workspace.yaml or creates defaults.
func LoadOrCreate(root string) (*WorkspaceConfig, error) {
	cfgPath := filepath.Join(root, "workspace.yaml")
	cfg := &WorkspaceConfig{
		Root:                root,
		HermesUser:          DefaultHermesUser(),
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
		cfg.HermesUser = DefaultHermesUser()
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
