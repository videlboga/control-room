package team

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"control-room/internal/store"
)

// AgentRef is a reference to a Hermes agent profile.
type AgentRef struct {
	HermesAgent string `json:"hermes_agent" yaml:"hermes_agent"`
	Profile     string `json:"profile" yaml:"profile"`
	Role        string `json:"role" yaml:"role"`
	Parent      string `json:"parent,omitempty" yaml:"parent,omitempty"`
	CloneFrom   string `json:"clone_from,omitempty" yaml:"clone_from,omitempty"`
}

// Team is a group of agents with workflow.
type Team struct {
	ID       string              `json:"id" yaml:"id"`
	Name     string              `json:"name" yaml:"name"`
	Agents   map[string]AgentRef `json:"agents" yaml:"agents"`
	Workflow []string            `json:"workflow" yaml:"workflow"`
	CreatedAt string             `json:"created_at" yaml:"created_at"`
}

func Create(st *store.Store, t *Team) error {
	if t.ID == "" || t.Name == "" {
		return errors.New("team id and name are required")
	}
	if len(t.Agents) == 0 {
		return errors.New("team must have at least one agent")
	}
	if t.CreatedAt == "" {
		t.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	// In stub mode we don't need real Hermes profiles.
	if !st.StubMode {
		if err := ensureAgentProfiles(st, t); err != nil {
			return err
		}
	}
	return st.WriteJSON([]string{"teams", t.ID + ".json"}, t)
}

func Get(st *store.Store, id string) (*Team, error) {
	var t Team
	err := st.ReadJSON([]string{"teams", id + ".json"}, &t)
	return &t, err
}

func List(st *store.Store) ([]Team, error) {
	names, err := st.ListJSON([]string{"teams"})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Team
	for _, n := range names {
		var t Team
		if err := st.ReadJSON([]string{"teams", n}, &t); err == nil {
			out = append(out, t)
		}
	}
	return out, nil
}

// AgentForStep returns the agent name and profile (if set) for a workflow step.
func (t *Team) AgentForStep(step string) (string, string) {
	switch step {
	case "research":
		for name, ref := range t.Agents {
			if ref.Role == "researcher" {
				return name, ref.Profile
			}
		}
	case "plan":
		for name, ref := range t.Agents {
			if ref.Role == "pm" || ref.Role == "lead" {
				return name, ref.Profile
			}
		}
	case "implement":
		for name, ref := range t.Agents {
			if ref.Role == "worker" || ref.Role == "coder" || ref.Role == "engineer" {
				return name, ref.Profile
			}
		}
	case "review", "verify":
		for name, ref := range t.Agents {
			if ref.Role == "reviewer" || ref.Role == "qa" {
				return name, ref.Profile
			}
		}
	}
	// Fallback: first worker/lead, then any.
	for name, ref := range t.Agents {
		if ref.Role == "worker" || ref.Role == "lead" {
			return name, ref.Profile
		}
	}
	for name, ref := range t.Agents {
		return name, ref.Profile
	}
	return "agent", ""
}

// ensureAgentProfiles creates a Hermes profile for every agent that lacks one.
func ensureAgentProfiles(st *store.Store, t *Team) error {
	source := st.HermesSourceProfile
	if source == "" {
		source = "qwen8"
	}
	user := st.HermesUser
	if user == "" {
		user = "cyberkitty"
	}

	for name, ref := range t.Agents {
		profile := ref.Profile
		cloneFrom := ref.CloneFrom
		if cloneFrom == "" {
			cloneFrom = source
		}
		if profile == "" {
			profile = fmt.Sprintf("hw_agent_%s_%s", sanitize(t.ID), sanitize(name))
			ref.Profile = profile
		}
		if !profileExists(user, profile) {
			desc := fmt.Sprintf("Hermes Workspace agent profile for team %s/%s (%s)", t.ID, name, ref.Role)
			if err := createProfile(user, cloneFrom, profile, desc); err != nil {
				return fmt.Errorf("failed to create hermes profile %s: %w", profile, err)
			}
		}
		t.Agents[name] = ref
	}
	return nil
}

func profileExists(user, profile string) bool {
	cmd := exec.Command("sudo", "-u", user, "bash", "-lc", fmt.Sprintf("hermes profile list | grep -w %q", profile))
	err := cmd.Run()
	return err == nil
}

func createProfile(user, source, profile, description string) error {
	cmd := exec.Command("sudo", "-u", user, "bash", "-lc",
		fmt.Sprintf("hermes profile create --clone-from %q --description %q %q", source, description, profile))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func sanitize(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "-", "_"))
}
