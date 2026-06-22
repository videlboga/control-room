package run

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"control-room/internal/config"
	"control-room/internal/project"
	"control-room/internal/store"
	"control-room/internal/task"
	"control-room/internal/team"
)

// Run is a concrete task execution.
type Run struct {
	ID        string            `json:"id" yaml:"id"`
	TaskID    string            `json:"task_id" yaml:"task_id"`
	ProjectID string            `json:"project_id" yaml:"project_id"`
	TeamID    string            `json:"team_id" yaml:"team_id"`
	Status    string            `json:"status" yaml:"status"`
	Branch    string            `json:"branch" yaml:"branch"`
	Worktree  string            `json:"worktree" yaml:"worktree"`
	Agent     string            `json:"agent" yaml:"agent"`
	Step      string            `json:"step" yaml:"step"`
	Errors    int               `json:"errors" yaml:"errors"`
	Summary   string            `json:"summary,omitempty" yaml:"summary,omitempty"`
	StartedAt string            `json:"started_at" yaml:"started_at"`
	EndedAt   string            `json:"ended_at,omitempty" yaml:"ended_at,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// Event is a single log entry.
type Event struct {
	Timestamp string `json:"timestamp"`
	RunID     string `json:"run_id"`
	Agent     string `json:"agent"`
	Type      string `json:"type"`
	Step      string `json:"step,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Payload   string `json:"payload,omitempty"`
}

// Start creates and launches a run for the given task.
func Start(st *store.Store, taskID string) (*Run, error) {
	t, err := task.Get(st, taskID)
	if err != nil {
		return nil, err
	}
	p, err := project.Get(st, t.ProjectID)
	if err != nil {
		return nil, err
	}
	te, err := team.Get(st, t.TeamID)
	if err != nil {
		return nil, err
	}

	if p.RepoPath != "" && !project.RepoExists(p.RepoPath) {
		return nil, errors.New("project repo is not a git repository: " + p.RepoPath)
	}

	user := st.HermesUser
	if user == "" {
		user = "cyberkitty"
	}

	runID := "run_" + uuid.New().String()[:8]
	r := &Run{
		ID:        runID,
		TaskID:    taskID,
		ProjectID: t.ProjectID,
		TeamID:    t.TeamID,
		Status:    "pending",
		Branch:    "agent/" + runID,
		Agent:     defaultAgentName(te),
		Step:      "setup",
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		Metadata: map[string]string{
			"task_title": t.Title,
			"team_name":  te.Name,
		},
	}
	_ = st.EnsureDir("runs", runID)
	_ = ensureHermesOwnership(st.Root, user)
	if err := st.WriteJSON([]string{"runs", runID, "run.json"}, r); err != nil {
		return nil, err
	}

	if p.RepoPath != "" {
		wtRoot := filepath.Join(st.Root, "worktrees", t.ProjectID, runID)
		_ = os.MkdirAll(filepath.Join(st.Root, "worktrees", t.ProjectID), 0o755)
		_ = ensureHermesOwnership(filepath.Join(st.Root, "worktrees", t.ProjectID), user)
		_ = os.MkdirAll(wtRoot, 0o755)
		_ = ensureHermesOwnership(wtRoot, user)

		out, err := runGitAsHermes(user, p.RepoPath, "worktree", "add", "-b", r.Branch, wtRoot)
		if err != nil {
			out2, err2 := runGitAsHermes(user, p.RepoPath, "worktree", "add", wtRoot, r.Branch)
			if err2 != nil {
				_ = logEvent(st, r, "system", "error", "git", string(out)+"\n"+string(out2))
				r.Status = "failed"
				r.Errors++
				_ = st.WriteJSON([]string{"runs", runID, "run.json"}, r)
				return r, fmt.Errorf("git worktree failed: %w\n%s", err, out)
			}
			out = out2
		}
		r.Worktree = wtRoot
		_ = ensureHermesOwnership(wtRoot, user)
		_ = exec.Command("git", "-C", wtRoot, "config", "user.email", "hw@hermes.local").Run()
		_ = exec.Command("git", "-C", wtRoot, "config", "user.name", "Hermes Workspace").Run()
		_ = logEvent(st, r, "system", "tool_call", "git", "worktree add "+wtRoot+" "+r.Branch+"\n"+string(out))
	}

	r.Status = "running"
	r.Step = "context"
	_ = logEvent(st, r, "system", "info", "", fmt.Sprintf("assembled context for project %s team %s", p.ID, te.ID))
	_ = st.WriteJSON([]string{"runs", runID, "run.json"}, r)

	// Concurrency limiter: claim a filesystem slot before launching the agent.
	// This keeps peak Hermes memory bounded on hosts with limited RAM.
	maxConcurrent := st.MaxConcurrentRuns
	if maxConcurrent <= 0 {
		maxConcurrent = config.DefaultMaxConcurrentRuns
	}
	_ = logEvent(st, r, "system", "info", "", fmt.Sprintf("waiting for concurrency slot (limit %d)", maxConcurrent))
	_ = st.WriteJSON([]string{"runs", runID, "run.json"}, r)
	slot, err := acquireSlot(st, maxConcurrent)
	if err != nil {
		r.Status = "failed"
		r.Errors++
		_ = logEvent(st, r, "system", "error", "", err.Error())
		_ = st.WriteJSON([]string{"runs", runID, "run.json"}, r)
		return r, err
	}
	_ = logEvent(st, r, "system", "info", "", fmt.Sprintf("acquired concurrency slot %d", slot))
	_ = st.WriteJSON([]string{"runs", runID, "run.json"}, r)

	go execute(st, r, t, p, te, slot)

	return r, nil
}

// Get loads a run by id.
func Get(st *store.Store, id string) (*Run, error) {
	var r Run
	err := st.ReadJSON([]string{"runs", id, "run.json"}, &r)
	return &r, err
}

// List all runs.
func List(st *store.Store) ([]Run, error) {
	entries, err := os.ReadDir(filepath.Join(st.Root, "runs"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Run
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var r Run
		if err := st.ReadJSON([]string{"runs", e.Name(), "run.json"}, &r); err == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

// Logs returns all events for a run.
func Logs(st *store.Store, id string) ([]Event, error) {
	path := filepath.Join(st.Root, "runs", id, "events.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readEvents(f)
}

// WaitFor blocks until the run reaches a terminal state, calling cb for new events.
func WaitFor(st *store.Store, id string, cb func(Event)) error {
	runPath := filepath.Join(st.Root, "runs", id, "run.json")
	eventsPath := filepath.Join(st.Root, "runs", id, "events.jsonl")

	f, err := os.Open(eventsPath)
	if err != nil {
		return err
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	for {
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				var ev Event
				if err := json.Unmarshal(line, &ev); err == nil {
					cb(ev)
				}
			}
			if err != nil {
				if err == io.EOF {
					break
				}
				return err
			}
		}

		var r Run
		if err := readJSONFile(runPath, &r); err == nil {
			if r.Status == "done" || r.Status == "failed" || r.Status == "cancelled" {
				return nil
			}
		}

		time.Sleep(500 * time.Millisecond)
	}
}

// Cancel a running run.
func Cancel(st *store.Store, id string) error {
	r, err := Get(st, id)
	if err != nil {
		return err
	}
	if r.Status == "done" || r.Status == "failed" || r.Status == "cancelled" {
		return errors.New("run already finished")
	}
	r.Status = "cancelled"
	r.EndedAt = time.Now().UTC().Format(time.RFC3339)
	_ = logEvent(st, r, "system", "info", "", "run cancelled by user")
	return st.WriteJSON([]string{"runs", id, "run.json"}, r)
}

func execute(st *store.Store, r *Run, t *task.Task, p *project.Project, te *team.Team, slot int) {
	if st.StubMode {
		executeStub(st, r, t, p, te, slot)
		return
	}

	step, agentName, profile := stepForTaskType(t, te)
	if step == "" {
		step = "implement"
	}
	if profile == "" {
		profile = defaultProfileForStep(step)
	}
	if agentName == "" {
		agentName = defaultAgentName(te)
	}

	user := st.HermesUser
	if user == "" {
		user = "cyberkitty"
	}

	r.Agent = agentName
	r.Step = step
	_ = logEvent(st, r, agentName, "step", "", step)
	_ = st.WriteJSON([]string{"runs", r.ID, "run.json"}, r)

	prompt := buildPrompt(st, r, t, p, te, step, 0, nil)
	out, err := runHermes(user, profile, prompt, r.Worktree)
	if err != nil {
		r.Errors++
		_ = logEvent(st, r, agentName, "error", "hermes", err.Error())
		_ = st.WriteJSON([]string{"runs", r.ID, "run.json"}, r)
	}
	_ = logEvent(st, r, agentName, "tool_call", "hermes", out)
	writeRunMetadata(st, r, t, out)

	// Update task BEFORE marking run done, so detached watchers do not exit
	// before the task status is persisted.
	t.Status = "done"
	_ = task.Update(st, t)

	r.Status = "done"
	r.Summary = fmt.Sprintf("Completed workflow for task %s using team %s", t.ID, te.ID)
	r.EndedAt = time.Now().UTC().Format(time.RFC3339)
	_ = logEvent(st, r, "system", "info", "", r.Summary)
	_ = st.WriteJSON([]string{"runs", r.ID, "run.json"}, r)

	_ = releaseSlot(st, slot)
}

// stepForTaskType maps an orchestrator task type to a single team workflow step
// and the agent that should execute it.
func stepForTaskType(t *task.Task, te *team.Team) (string, string, string) {
	switch t.Type {
	case task.TypeResearch:
		name, profile := te.AgentForStep("research")
		return "research", name, profile
	case task.TypeQAReview:
		name, profile := te.AgentForStep("review")
		if name == "" {
			name, profile = te.AgentForStep("verify")
		}
		return "review", name, profile
	case task.TypePMPlan:
		name, profile := te.AgentForStep("plan")
		return "plan", name, profile
	case task.TypeEngineering:
		name, profile := te.AgentForStep("implement")
		return "implement", name, profile
	case task.TypeQAVerify:
		name, profile := te.AgentForStep("verify")
		return "verify", name, profile
	case task.TypePMConsistency:
		name, profile := te.AgentForStep("review")
		if name == "" {
			name, profile = te.AgentForStep("verify")
		}
		return "review", name, profile
	}
	return "", "", ""
}

// stubPlan mirrors the orchestrator Plan shape without importing the orchestrator
// package (that would create an import cycle).
type stubPlan struct {
	Tasks []stubPlanTask `json:"tasks"`
}

type stubPlanTask struct {
	ID             string   `json:"id"`
	Type           string   `json:"type"`
	Specialization string   `json:"specialization"`
	Title          string   `json:"title"`
	Description    string   `json:"description,omitempty"`
	Dependencies   []string `json:"dependencies,omitempty"`
}

// executeStub simulates an agent run without invoking Hermes.
// It is used for deterministic end-to-end tests of the orchestrator.
func executeStub(st *store.Store, r *Run, t *task.Task, p *project.Project, te *team.Team, slot int) {
	_ = logEvent(st, r, "stub", "info", "", fmt.Sprintf("stub run for task %s type %s", t.ID, t.Type))

	agentName := defaultAgentName(te)
	r.Agent = agentName
	r.Step = "stub"
	_ = st.WriteJSON([]string{"runs", r.ID, "run.json"}, r)

	verdict := "approve"
	reason := "stub approved"

	switch t.Type {
	case task.TypeQAVerify:
		// First QA verification attempt is rejected to exercise the redo path.
		if t.RedoIndex == 0 {
			verdict = "reject"
			reason = "stub: first QA verification rejected to test redo"
			_ = st.WriteJSON([]string{"runs", r.ID, "metadata.json"}, map[string]string{
				"verdict": "reject",
				"reason":  reason,
			})
		} else {
			// Write a fake diff and review note so gate checks pass.
			if r.Worktree != "" {
				_ = writeStubDiff(r.Worktree, "qa-verify")
			}
			_ = st.WriteJSON([]string{"runs", r.ID, "metadata.json"}, map[string]string{
				"verdict":         "approve",
				"reason":          "stub QA verify passed",
				"qa_review_note":  "- checked diff exists\n- checked unit tests\n- checked lint clean",
			})
		}
	case task.TypePMPlan:
		// Generate a multi-task engineering plan with dependencies to test DAG expansion.
		plan := stubPlan{
			Tasks: []stubPlanTask{
				{ID: "eng-core", Type: "engineering", Specialization: "backend", Title: "Implement core logic", Dependencies: []string{}},
				{ID: "eng-tests", Type: "engineering", Specialization: "backend", Title: "Add unit tests", Dependencies: []string{"eng-core"}},
				{ID: "eng-cli", Type: "engineering", Specialization: "cli", Title: "Wire CLI commands", Dependencies: []string{"eng-core"}},
				{ID: "eng-docs", Type: "engineering", Specialization: "docs", Title: "Update documentation", Dependencies: []string{"eng-cli"}},
			},
		}
		planJSON, _ := json.Marshal(plan)
		writeRunMetadata(st, r, t, "")
		_ = st.WriteJSON([]string{"runs", r.ID, "metadata.json"}, map[string]string{
			"verdict": "approve",
			"reason":  "stub PM plan generated",
			"plan":    string(planJSON),
		})
	default:
		writeRunMetadata(st, r, t, "")
	}

	// Override verdict/reason for non-plan types, except qa_verify which already wrote
	// its metadata including the review note above.
	if t.Type != task.TypePMPlan && t.Type != task.TypeQAVerify {
		_ = st.WriteJSON([]string{"runs", r.ID, "metadata.json"}, map[string]string{
			"verdict": verdict,
			"reason":  reason,
		})
	}

	_ = logEvent(st, r, "stub", "tool_call", "verdict", fmt.Sprintf("verdict=%s reason=%s", verdict, reason))

	t.Status = "done"
	_ = task.Update(st, t)

	r.Status = "done"
	r.Summary = fmt.Sprintf("Completed stub workflow for task %s using team %s", t.ID, te.ID)
	r.EndedAt = time.Now().UTC().Format(time.RFC3339)
	_ = logEvent(st, r, "system", "info", "", r.Summary)
	_ = st.WriteJSON([]string{"runs", r.ID, "run.json"}, r)

	_ = releaseSlot(st, slot)
}

// acquireSlot claims one of N filesystem slots; returns the slot number (1..N).
func acquireSlot(st *store.Store, max int) (int, error) {
	if max <= 0 {
		max = config.DefaultMaxConcurrentRuns
	}
	if err := st.EnsureDir("concurrency"); err != nil {
		return 0, err
	}
	for {
		for i := 1; i <= max; i++ {
			path := filepath.Join(st.Root, "concurrency", fmt.Sprintf("slot_%d.lock", i))
			f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
			if err == nil {
				_, _ = f.WriteString(fmt.Sprintf("%d\n", os.Getpid()))
				_ = f.Close()
				return i, nil
			}
		}
		time.Sleep(3 * time.Second)
	}
}

// releaseSlot frees the claimed slot.
func releaseSlot(st *store.Store, slot int) error {
	path := filepath.Join(st.Root, "concurrency", fmt.Sprintf("slot_%d.lock", slot))
	return os.Remove(path)
}

func runHermes(user, profile, prompt, worktree string) (string, error) {
	args := []string{
		"--profile", profile,
		"chat", "-q", prompt,
		"--toolsets", "file,terminal",
		"--yolo",
	}
	quotedArgs := ""
	for _, a := range args {
		if quotedArgs != "" {
			quotedArgs += " "
		}
		quotedArgs += fmt.Sprintf("%q", a)
	}
	var cmd *exec.Cmd
	if worktree != "" {
		cmd = exec.Command("sudo", "-u", user, "bash", "-lc", fmt.Sprintf("cd %q && hermes %s", worktree, quotedArgs))
	} else {
		cmd = exec.Command("sudo", "-u", user, "bash", "-lc", fmt.Sprintf("hermes %s", quotedArgs))
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("hermes exited: %w\n%s", err, out)
	}
	return cleanHermesOutput(string(out)), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func cleanHermesOutput(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	for _, line := range lines {
		if strings.Contains(line, "preparing") || strings.Contains(line, "┊") {
			continue
		}
		if strings.HasPrefix(line, "Resume this session") ||
			strings.HasPrefix(line, "Session:") ||
			strings.HasPrefix(line, "Duration:") ||
			strings.HasPrefix(line, "Messages:") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func buildPrompt(st *store.Store, r *Run, t *task.Task, p *project.Project, te *team.Team, step string, stepIdx int, previous []string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("You are the '%s' agent in team '%s'.\n", r.Agent, te.Name))
	b.WriteString(fmt.Sprintf("Current step: %s (step %d of workflow %v).\n", step, stepIdx+1, te.Workflow))
	b.WriteString(fmt.Sprintf("Project: %s (%s).\n", p.ID, p.Title))
	if len(p.Rules) > 0 {
		b.WriteString(fmt.Sprintf("Project rules: %v.\n", p.Rules))
	}

	docs, _ := project.ReadDocs(st, p.ID, 4000)
	if len(docs) > 0 {
		b.WriteString("\nProject documentation:\n")
		for path, content := range docs {
			b.WriteString(fmt.Sprintf("--- %s ---\n%s\n", filepath.Base(path), truncate(content, 4000)))
		}
	}

	b.WriteString(fmt.Sprintf("\nTask: %s\n", t.Title))
	if t.Description != "" {
		b.WriteString(fmt.Sprintf("Description: %s\n", t.Description))
	}
	b.WriteString(fmt.Sprintf("Task type: %s\n", t.Type))
	b.WriteString(fmt.Sprintf("Your role: %s\n", step))
	if t.Type == task.TypePMPlan {
		b.WriteString("\nAs the PM planner, produce a detailed implementation plan. " +
			"Write the plan as compact JSON to the file docs/plan.json in the working directory. " +
			"The JSON must be under the key 'plan' with an array of engineering tasks. " +
			"Each task: id, type='engineering', specialization, title, optional description, dependencies (array of ids). " +
			"The JSON must be compact (single line), with no literal newlines inside string values — use \\n escapes if needed. " +
			"After writing the file, report a concise summary of what you did.\n\n" +
			"Example contents of docs/plan.json:\n" +
			"{\"plan\":{\"tasks\":[{\"id\":\"eng-core\",\"type\":\"engineering\",\"specialization\":\"backend\",\"title\":\"Implement core\",\"dependencies\":[]}]}}\n")
	}
	if t.VerdictReason != "" {
		b.WriteString(fmt.Sprintf("Rejection reason to address: %s\n", t.VerdictReason))
	}
	if r.Worktree != "" {
		b.WriteString(fmt.Sprintf("Working directory (git worktree): %s\n", r.Worktree))
		b.WriteString("You may read files and run git commands here. Prefer small, focused changes.\n")
	}
	if len(previous) > 0 {
		b.WriteString("\nPrevious steps (last 2 summaries):\n")
		start := 0
		if len(previous) > 2 {
			start = len(previous) - 2
		}
		for i := start; i < len(previous); i++ {
			b.WriteString(truncate(previous[i], 800))
			b.WriteString("\n---\n")
		}
	}
	b.WriteString(fmt.Sprintf("\nPerform the '%s' step and report a concise summary of what you did.", step))
	return b.String()
}

func defaultAgentName(te *team.Team) string {
	for name, ref := range te.Agents {
		if ref.Role == "lead" || ref.Role == "worker" {
			return name
		}
	}
	for name := range te.Agents {
		return name
	}
	return "agent"
}

func defaultProfileForStep(step string) string {
	switch step {
	case "review":
		return "hw_agent_reviewer"
	default:
		return "hw_agent_coder"
	}
}

func ensureHermesOwnership(path, user string) error {
	return exec.Command("chown", "-R", user+":"+user, path).Run()
}

func runGitAsHermes(user, repo string, args ...string) ([]byte, error) {
	quotedArgs := ""
	for _, a := range args {
		if quotedArgs != "" {
			quotedArgs += " "
		}
		quotedArgs += fmt.Sprintf("%q", a)
	}
	cmd := exec.Command("sudo", "-u", user, "bash", "-lc", fmt.Sprintf("cd %q && git %s", repo, quotedArgs))
	return cmd.CombinedOutput()
}

// writeStubDiff creates a tiny fake uncommitted change so gate checks see a non-empty diff.
func writeStubDiff(worktree, marker string) error {
	f, err := os.OpenFile(filepath.Join(worktree, "stub-output.md"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "\n## %s\n\nstub change %d\n", marker, time.Now().UnixNano())
	_ = f.Close()
	if err != nil {
		return err
	}
	_ = exec.Command("git", "-C", worktree, "add", "stub-output.md").Run()
	// Leave the change staged but not committed so git diff HEAD is non-empty.
	return nil
}


// writeRunMetadata writes a deterministic metadata.json for the orchestrator.
// Agents can be instructed to override via their output; here we use a safe default.
func writeRunMetadata(st *store.Store, r *Run, t *task.Task, agentOutput string) {
	meta := map[string]string{
		"verdict": "approve",
		"reason":  "agent completed step " + string(t.Type),
	}
	if t.Type == task.TypePMPlan {
		planJSON, ok := extractPlanJSON(agentOutput, r.Worktree)
		if !ok || planJSON == "" {
			meta["verdict"] = "reject"
			meta["reason"] = "PM plan did not produce a valid JSON plan with tasks/dependencies"
			meta["plan"] = `{"plan":{"tasks":[]}}`
		} else {
			meta["plan"] = planJSON
		}
		_ = st.WriteJSON([]string{"runs", r.ID, "metadata.json"}, meta)
		return
	}
	// Try to parse explicit verdict from the last lines of agent output.
	lines := strings.Split(agentOutput, "\n")
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-5; i-- {
		line := strings.ToLower(strings.TrimSpace(lines[i]))
		if strings.Contains(line, "verdict: reject") || strings.Contains(line, "reject") {
			meta["verdict"] = "reject"
		}
		if strings.Contains(line, "verdict: approve") || strings.Contains(line, "approve") {
			meta["verdict"] = "approve"
		}
	}
	_ = st.WriteJSON([]string{"runs", r.ID, "metadata.json"}, meta)
}

// extractPlanJSON finds the last JSON object in the agent output that contains a "plan" key.
// It also tries to read docs/plan.json from the worktree if the output extraction fails.
// It returns the extracted JSON and true if a valid plan object was found.
func extractPlanJSON(output, worktree string) (string, bool) {
	// Prefer a dedicated plan file written by the PM agent.
	if worktree != "" {
		planPath := filepath.Join(worktree, "docs", "plan.json")
		if data, err := os.ReadFile(planPath); err == nil {
			if s, ok := normalizePlanJSON(string(data)); ok {
				return s, true
			}
		}
	}
	// Try to extract JSON from a markdown code block.
	if idx := strings.LastIndex(output, "```json"); idx != -1 {
		block := output[idx+len("```json"):]
		if end := strings.Index(block, "```"); end != -1 {
			candidate := strings.TrimSpace(block[:end])
			if s, ok := normalizePlanJSON(candidate); ok {
				return s, true
			}
		}
	}
	// Fallback: scan for the last JSON object containing "plan".
	start := strings.LastIndex(output, `{"plan"`)
	if start == -1 {
		start = strings.LastIndex(output, `{"tasks"`)
	}
	if start == -1 {
		return "", false
	}
	depth := 0
	end := -1
	for i := start; i < len(output); i++ {
		c := output[i]
		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				end = i
				break
			}
		}
	}
	if end == -1 {
		return "", false
	}
	return normalizePlanJSON(output[start : end+1])
}

// normalizePlanJSON validates and minifies a plan JSON object. It accepts both
// {"plan": {"tasks": [...]}} and {"plan": [...]} shapes.
func normalizePlanJSON(s string) (string, bool) {
	// Remove literal newlines inside JSON string values that were not escaped.
	s = strings.ReplaceAll(s, "\r\n", "\\n")
	s = strings.ReplaceAll(s, "\n", "\\n")
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return "", false
	}
	if _, ok := v["plan"]; !ok && v["tasks"] == nil {
		return "", false
	}
	compact, _ := json.Marshal(v)
	return string(compact), true
}

func logEvent(st *store.Store, r *Run, agent, typ, tool, payload string) error {
	ev := Event{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		RunID:     r.ID,
		Agent:     agent,
		Type:      typ,
		Step:      r.Step,
		Tool:      tool,
		Payload:   payload,
	}
	return st.AppendJSONL([]string{"runs", r.ID, "events.jsonl"}, ev)
}

func readEvents(r io.Reader) ([]Event, error) {
	var events []Event
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err == nil {
			events = append(events, ev)
		}
	}
	return events, scanner.Err()
}

func readJSONFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
