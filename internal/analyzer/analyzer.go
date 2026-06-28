package analyzer

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"control-room/internal/config"
	"control-room/internal/run"
	"control-room/internal/store"
	"control-room/internal/task"
)

// RunMetrics holds extracted metrics for a single run.
type RunMetrics struct {
	RunID       string `json:"run_id"`
	TaskID      string `json:"task_id"`
	ProjectID   string `json:"project_id"`
	Status      string `json:"status"`
	Agent       string `json:"agent"`
	Step        string `json:"step"`
	Errors      int    `json:"errors"`
	Summary     string `json:"summary"`
	StartedAt   string `json:"started_at"`
	EndedAt     string `json:"ended_at"`
	Duration    string `json:"duration"`
	ToolUseCount int   `json:"tool_use_count"`
	Verdict     string `json:"verdict"`
	VerdictReason string `json:"verdict_reason"`
	TaskType    string `json:"task_type"`
	TaskTitle   string `json:"task_title"`
	RedoIndex   int    `json:"redo_index"`
}

// Metrics is the aggregated summary of all analyzed runs.
type Metrics struct {
	GeneratedAt      string
	Since            string
	TotalRuns        int
	ByStatus         map[string]int
	ByAgent          map[string]int
	ByStep           map[string]int
	ByProject        map[string]int
	FailedRuns       []RunMetrics
	RedoRuns         []RunMetrics
	SlowRuns         []RunMetrics // runs longer than 10 minutes
	AllRuns          []RunMetrics
	FailureRate      float64
	AvgDurationSec   float64
	CommonFailReasons map[string]int
}

// Collect gathers metrics for runs that ended after `since`.
// If since is zero, all runs are collected.
func Collect(st *store.Store, since time.Time) (*Metrics, error) {
	runs, err := run.List(st)
	if err != nil {
		return nil, err
	}

	m := &Metrics{
		GeneratedAt:       time.Now().UTC().Format(time.RFC3339),
		Since:             since.UTC().Format(time.RFC3339),
		ByStatus:          map[string]int{},
		ByAgent:           map[string]int{},
		ByStep:            map[string]int{},
		ByProject:         map[string]int{},
		CommonFailReasons: map[string]int{},
	}

	// Load all tasks once for lookup.
	taskCache := map[string]*task.Task{}
	allTasks, _ := task.List(st)
	for i := range allTasks {
		taskCache[allTasks[i].ID] = &allTasks[i]
	}

	var totalDurationSec float64
	var countDuration int

	for _, r := range runs {
		// Filter by ended_at if since is set.
		if !since.IsZero() && r.EndedAt != "" {
			ended, err := time.Parse(time.RFC3339, r.EndedAt)
			if err == nil && ended.Before(since) {
				continue
			}
		}

		rm := RunMetrics{
			RunID:       r.ID,
			TaskID:      r.TaskID,
			ProjectID:   r.ProjectID,
			Status:      r.Status,
			Agent:       r.Agent,
			Step:        r.Step,
			Errors:      r.Errors,
			Summary:     r.Summary,
			StartedAt:   r.StartedAt,
			EndedAt:     r.EndedAt,
		}

		// Duration.
		if r.StartedAt != "" && r.EndedAt != "" {
			start, err1 := time.Parse(time.RFC3339, r.StartedAt)
			end, err2 := time.Parse(time.RFC3339, r.EndedAt)
			if err1 == nil && err2 == nil {
				dur := end.Sub(start)
				rm.Duration = dur.Round(time.Second).String()
				totalDurationSec += dur.Seconds()
				countDuration++
				if dur > 10*time.Minute {
					m.SlowRuns = append(m.SlowRuns, rm)
				}
			}
		}

		// Tool use count from events.jsonl.
		rm.ToolUseCount = countToolCalls(st, r.ID)

		// Verdict from metadata.json.
		verdict, reason := readMetadata(st, r.ID)
		rm.Verdict = verdict
		rm.VerdictReason = reason

		// Task info.
		if t, ok := taskCache[r.TaskID]; ok {
			rm.TaskType = string(t.Type)
			rm.TaskTitle = t.Title
			rm.RedoIndex = t.RedoIndex
			if t.RedoIndex > 0 {
				m.RedoRuns = append(m.RedoRuns, rm)
			}
		}

		m.AllRuns = append(m.AllRuns, rm)
		m.TotalRuns++
		m.ByStatus[r.Status]++
		m.ByAgent[r.Agent]++
		m.ByStep[r.Step]++
		m.ByProject[r.ProjectID]++

		if r.Status == "failed" {
			m.FailedRuns = append(m.FailedRuns, rm)
			// Categorize failure reasons.
			reason := rm.VerdictReason
			if reason == "" {
				reason = rm.Summary
			}
			if reason != "" {
				// Shorten to first 100 chars for grouping.
				short := reason
				if len(short) > 100 {
					short = short[:100]
				}
				m.CommonFailReasons[short]++
			}
		}
	}

	if m.TotalRuns > 0 {
		m.FailureRate = float64(len(m.FailedRuns)) / float64(m.TotalRuns) * 100
	}
	if countDuration > 0 {
		m.AvgDurationSec = totalDurationSec / float64(countDuration)
	}

	// Sort slow runs by duration descending (longest first).
	sort.Slice(m.SlowRuns, func(i, j int) bool {
		return parseDurationSec(m.SlowRuns[i].Duration) > parseDurationSec(m.SlowRuns[j].Duration)
	})

	return m, nil
}

func countToolCalls(st *store.Store, runID string) int {
	events, err := run.Logs(st, runID)
	if err != nil {
		return 0
	}
	count := 0
	for _, ev := range events {
		if ev.Type == "tool_call" && ev.Agent != "system" {
			count++
		}
	}
	return count
}

func readMetadata(st *store.Store, runID string) (verdict, reason string) {
	path := filepath.Join(st.Root, "runs", runID, "metadata.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return "", ""
	}
	return m["verdict"], m["reason"]
}

func parseDurationSec(s string) float64 {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d.Seconds()
}

// BuildPrompt creates the prompt for the analyzer hermes agent.
func BuildPrompt(m *Metrics) string {
	var b strings.Builder
	b.WriteString("You are a Control Room run analyzer agent. Your job is to analyze recent agent runs and produce a report with actionable recommendations for improving agent performance.\n\n")
	b.WriteString(fmt.Sprintf("Report generated: %s\n", m.GeneratedAt))
	b.WriteString(fmt.Sprintf("Analyzing runs since: %s\n\n", m.Since))
	b.WriteString(fmt.Sprintf("Total runs analyzed: %d\n", m.TotalRuns))
	b.WriteString(fmt.Sprintf("Failure rate: %.1f%%\n", m.FailureRate))
	b.WriteString(fmt.Sprintf("Average duration: %.0f seconds\n\n", m.AvgDurationSec))

	b.WriteString("Runs by status:\n")
	for status, count := range m.ByStatus {
		b.WriteString(fmt.Sprintf("  %s: %d\n", status, count))
	}
	b.WriteString("\nRuns by agent:\n")
	for agent, count := range m.ByAgent {
		b.WriteString(fmt.Sprintf("  %s: %d\n", agent, count))
	}
	b.WriteString("\nRuns by step:\n")
	for step, count := range m.ByStep {
		b.WriteString(fmt.Sprintf("  %s: %d\n", step, count))
	}
	b.WriteString("\nRuns by project:\n")
	for proj, count := range m.ByProject {
		b.WriteString(fmt.Sprintf("  %s: %d\n", proj, count))
	}

	if len(m.FailedRuns) > 0 {
		b.WriteString(fmt.Sprintf("\n--- Failed runs (%d) ---\n", len(m.FailedRuns)))
		limit := m.FailedRuns
		if len(limit) > 20 {
			limit = limit[:20]
		}
		for _, rm := range limit {
			b.WriteString(fmt.Sprintf("  %s [%s/%s] task=%s type=%s dur=%s tools=%d verdict=%s\n    reason: %s\n",
				rm.RunID, rm.Agent, rm.Step, rm.TaskID, rm.TaskType, rm.Duration, rm.ToolUseCount, rm.Verdict, rm.VerdictReason))
		}
		if len(m.FailedRuns) > 20 {
			b.WriteString(fmt.Sprintf("  ... and %d more\n", len(m.FailedRuns)-20))
		}
		b.WriteString("\nCommon failure reasons:\n")
		type reasonCount struct {
			reason string
			count  int
		}
		var rcs []reasonCount
		for r, c := range m.CommonFailReasons {
			rcs = append(rcs, reasonCount{r, c})
		}
		sort.Slice(rcs, func(i, j int) bool { return rcs[i].count > rcs[j].count })
		for _, rc := range rcs {
			b.WriteString(fmt.Sprintf("  (%d) %s\n", rc.count, rc.reason))
		}
	}

	if len(m.RedoRuns) > 0 {
		b.WriteString(fmt.Sprintf("\n--- Redo runs (%d) ---\n", len(m.RedoRuns)))
		limit := m.RedoRuns
		if len(limit) > 15 {
			limit = limit[:15]
		}
		for _, rm := range limit {
			b.WriteString(fmt.Sprintf("  %s [%s/%s] redo_index=%d task=%s type=%s verdict=%s\n    reason: %s\n",
				rm.RunID, rm.Agent, rm.Step, rm.RedoIndex, rm.TaskID, rm.TaskType, rm.Verdict, rm.VerdictReason))
		}
	}

	if len(m.SlowRuns) > 0 {
		b.WriteString(fmt.Sprintf("\n--- Slow runs (>10min, top 10) ---\n"))
		limit := m.SlowRuns
		if len(limit) > 10 {
			limit = limit[:10]
		}
		for _, rm := range limit {
			b.WriteString(fmt.Sprintf("  %s [%s/%s] dur=%s tools=%d task=%s\n",
				rm.RunID, rm.Agent, rm.Step, rm.Duration, rm.ToolUseCount, rm.TaskID))
		}
	}

	b.WriteString("\n--- Per-run tool usage (all runs, compact) ---\n")
	for _, rm := range m.AllRuns {
		b.WriteString(fmt.Sprintf("%s|status=%s|agent=%s|step=%s|dur=%s|tools=%d|verdict=%s\n",
			rm.RunID, rm.Status, rm.Agent, rm.Step, rm.Duration, rm.ToolUseCount, rm.Verdict))
	}

	b.WriteString(`
---
Produce a markdown report with these sections:
1. Summary — overall health, key metrics
2. Failure Analysis — what failed and why, patterns
3. Performance — slow runs, tool usage patterns, outliers
4. Redo Analysis — tasks that needed retries, root causes
5. Recommendations — concrete, actionable suggestions for improving agents, prompts, gates, or configuration

Write the report to ` + "`analysis-report.md`" + ` in the current working directory (NOT in a subdirectory).
Keep it concise but specific. Reference actual run IDs and task IDs.
`)
	return b.String()
}

// Run invokes the hermes analyzer agent, collects metrics, and saves the report.
// It returns the report path and any error.
func Run(st *store.Store, profile, provider, model string, since time.Time) (string, error) {
	if profile == "" {
		profile = "hw_agent_controller"
	}

	m, err := Collect(st, since)
	if err != nil {
		return "", fmt.Errorf("collect metrics: %w", err)
	}

	if m.TotalRuns == 0 {
		return "", fmt.Errorf("no runs to analyze since %s", m.Since)
	}

	prompt := BuildPrompt(m)

	// Prepare reports directory.
	reportsDir := filepath.Join(st.Root, "reports")
	if err := os.MkdirAll(reportsDir, 0o755); err != nil {
		return "", fmt.Errorf("create reports dir: %w", err)
	}

	// Save the metrics snapshot alongside the report.
	metricsPath := filepath.Join(reportsDir, fmt.Sprintf("metrics-%s.json",
		time.Now().UTC().Format("20060102-150405")))
	metricsData, _ := json.MarshalIndent(m, "", "  ")
	_ = os.WriteFile(metricsPath, metricsData, 0o644)

	// Invoke hermes.
	reportPath := filepath.Join(reportsDir, "analysis-report.md")
	out, err := invokeHermes(profile, provider, model, prompt, reportsDir)
	if err != nil {
		// Save the raw output even on error so we can inspect what happened.
		_ = os.WriteFile(filepath.Join(reportsDir, "last-analysis-raw.txt"), []byte(out), 0o644)
		return "", fmt.Errorf("hermes analyzer failed: %w\noutput: %s", err, truncate(out, 2000))
	}

	// Save raw output.
	_ = os.WriteFile(filepath.Join(reportsDir, "last-analysis-raw.txt"), []byte(out), 0o644)

	return reportPath, nil
}

// invokeHermes runs the hermes CLI with the given prompt and returns its output.
func invokeHermes(profile, provider, model, prompt, workdir string) (string, error) {
	hermesBin := "/home/" + config.DefaultHermesUser() + "/.local/bin/hermes"
	if _, err := os.Stat(hermesBin); err != nil {
		hermesBin = "hermes"
	}

	args := []string{
		"--profile", profile,
		"chat", "-q", prompt,
	}
	if provider != "" {
		args = append(args, "--provider", provider)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, "--toolsets", "terminal,file", "--yolo", "--source", "tool",
		"--max-turns", "80")

	// Build env with UTF-8 overrides.
	envBase := os.Environ()
	filtered := make([]string, 0, len(envBase)+5)
	for _, e := range envBase {
		if strings.HasPrefix(e, "LANG=") || strings.HasPrefix(e, "LC_") ||
			strings.HasPrefix(e, "PYTHONIOENCODING=") || strings.HasPrefix(e, "PYTHONUTF8=") {
			continue
		}
		filtered = append(filtered, e)
	}
	user := config.DefaultHermesUser()
	cleanEnv := append(filtered,
		"HOME=/home/"+user,
		"PYTHONIOENCODING=utf-8",
		"PYTHONUTF8=1",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
	)

	currentUser := os.Getenv("USER")
	if currentUser == "" {
		currentUser = filepath.Base(os.Getenv("HOME"))
	}

	var cmd *exec.Cmd
	if currentUser == user {
		cmd = exec.Command(hermesBin, args...)
		cmd.Env = cleanEnv
	} else {
		envPrefix := []string{"env", "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1", "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8"}
		sudoArgs := append([]string{"-u", user}, envPrefix...)
		sudoArgs = append(sudoArgs, hermesBin)
		sudoArgs = append(sudoArgs, args...)
		cmd = exec.Command("sudo", sudoArgs...)
		cmd.Env = cleanEnv
	}

	if workdir != "" {
		cmd.Dir = workdir
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("hermes exited: %w", err)
	}
	return string(out), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// lastAnalyzedPath returns the path to the marker file that records the last
// successful analysis timestamp.
func lastAnalyzedPath(st *store.Store) string {
	return filepath.Join(st.Root, "reports", ".last_analyzed")
}

// LastAnalyzed reads the timestamp of the last successful analysis.
// Returns zero time if no previous analysis exists.
func LastAnalyzed(st *store.Store) time.Time {
	data, err := os.ReadFile(lastAnalyzedPath(st))
	if err != nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	if err != nil {
		return time.Time{}
	}
	return t
}

// MarkAnalyzed writes the current time as the last analysis timestamp.
func MarkAnalyzed(st *store.Store) error {
	path := lastAnalyzedPath(st)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	return os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339)), 0o644)
}

// RunSinceLast only analyzes runs that ended after the last analysis.
// If no previous analysis exists, analyzes all runs.
func RunSinceLast(st *store.Store, profile, provider, model string) (string, error) {
	since := LastAnalyzed(st)
	reportPath, err := Run(st, profile, provider, model, since)
	if err != nil {
		return reportPath, err
	}
	_ = MarkAnalyzed(st)
	return reportPath, nil
}

// Loop runs the analyzer periodically with the given interval.
// It blocks until the context (via process termination) ends.
// If window is non-zero, each iteration analyzes runs within that rolling
// window (e.g. last 30h). If window is zero, each iteration is incremental
// (only runs since the last successful analysis).
func Loop(st *store.Store, profile, provider, model string, interval, window time.Duration, cb func(msg string)) {
	mode := "incremental"
	if window > 0 {
		mode = fmt.Sprintf("window=%s", window)
	}
	cb(fmt.Sprintf("analyzer loop started, interval=%s, %s, profile=%s", interval, mode, profile))
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run once immediately on startup.
	for {
		if window > 0 {
			since := time.Now().UTC().Add(-window)
			reportPath, err := Run(st, profile, provider, model, since)
			if err != nil {
				cb(fmt.Sprintf("analyzer error: %v", err))
			} else {
				cb(fmt.Sprintf("analyzer report saved: %s", reportPath))
			}
		} else {
			reportPath, err := RunSinceLast(st, profile, provider, model)
			if err != nil {
				cb(fmt.Sprintf("analyzer error: %v", err))
			} else {
				cb(fmt.Sprintf("analyzer report saved: %s", reportPath))
			}
		}
		<-ticker.C
	}
}