package dashboard

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

// updateBeliefs triggers a Python script that uses LLM to update the belief graph
// for a project after a run completes. Runs async — doesn't block the watcher.
func (w *Watcher) updateBeliefs(projectID, taskID, status, agent, step, summary string) {
	// Build a compact description of what happened
	event := fmt.Sprintf("task=%s agent=%s step=%s status=%s summary=%s",
		taskID, agent, step, status, truncate(summary, 200))

	// Call the belief updater script
	cmd := exec.Command("python3",
		os.ExpandEnv("$HOME/.hermes/profiles/qwen8/scripts/belief_updater.py"),
		"--project", projectID,
		"--event", event,
	)
	cmd.Dir = "/home/cyberkitty/Projects/control-room"

	output, err := cmd.CombinedOutput()
	if err != nil {
		slog.Warn("belief update", "project", projectID, "err", err, "output", string(output)[:200])
		return
	}
	result := strings.TrimSpace(string(output))
	if result != "" {
		slog.Info("belief update", "project", projectID, "result", result[:min(100, len(result))])
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}