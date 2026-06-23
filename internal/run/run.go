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
	"sync"
	"syscall"
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
	PID       int               `json:"pid,omitempty" yaml:"pid,omitempty"`
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
		user = config.DefaultHermesUser()
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
		if !st.StubMode {
			_ = ensureHermesOwnership(filepath.Join(st.Root, "worktrees", t.ProjectID), user)
		}
		_ = os.MkdirAll(wtRoot, 0o755)
		if !st.StubMode {
			_ = ensureHermesOwnership(wtRoot, user)
		}

		baseRef := "HEAD"
		if !st.StubMode {
			_ = ensureHermesOwnership(p.RepoPath, user)
		}
		_ = exec.Command("sudo", "-u", user, "git", "config", "--global", "--add", "safe.directory", p.RepoPath).Run()
		if p.BaseCommit != "" {
			baseRef = p.BaseCommit
		}
		out, err := runGitAsHermes(st.StubMode, user, p.RepoPath, "worktree", "add", "-b", r.Branch, wtRoot, baseRef)
		if err != nil {
			_ = logEvent(st, r, "system", "error", "git", string(out))
			r.Status = "failed"
			r.Errors++
			_ = st.WriteJSON([]string{"runs", runID, "run.json"}, r)
			return r, fmt.Errorf("git worktree failed: %w\n%s", err, out)
		}
		r.Worktree = wtRoot
		if !st.StubMode {
			_ = ensureHermesOwnership(wtRoot, user)
		}
		_ = exec.Command("git", "-C", wtRoot, "config", "user.email", "hw@hermes.local").Run()
		_ = exec.Command("git", "-C", wtRoot, "config", "user.name", "Hermes Workspace").Run()
		_ = exec.Command("sudo", "-u", user, "git", "config", "--global", "--add", "safe.directory", wtRoot).Run()
		_ = exec.Command("sudo", "-u", user, "git", "config", "--global", "--add", "safe.directory", p.RepoPath).Run()
		_ = logEvent(st, r, "system", "tool_call", "git", "worktree add "+wtRoot+" "+r.Branch+" from "+baseRef+"\n"+string(out))
		_ = seedWorktreeDocs(st, r, p, wtRoot)
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

// seedWorktreeDocs copies project docs (e.g. RESEARCH.md, plan.json) into the run worktree
// so that every agent can read the shared source of truth regardless of branch.
func seedWorktreeDocs(st *store.Store, r *Run, p *project.Project, wt string) error {
	if wt == "" {
		return nil
	}
	var sources []string
	if p.DocsDir != "" {
		sources = append(sources, p.DocsDir)
	}
	if p.RepoPath != "" {
		sources = append(sources, p.RepoPath)
	}
	for _, srcDir := range sources {
		if err := filepath.Walk(srcDir, func(src string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			rel, err := filepath.Rel(srcDir, src)
			if err != nil {
				return err
			}
			base := filepath.Base(rel)
			if base != "RESEARCH.md" && base != "plan.json" && !strings.HasPrefix(rel, "docs") {
				return nil
			}
			dst := filepath.Join(wt, rel)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			data, err := os.ReadFile(src)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dst, data, 0o644); err != nil {
				return err
			}
			_ = logEvent(st, r, "system", "info", "", "seeded doc "+rel)
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// Get loads a run by id.
func Get(st *store.Store, id string) (*Run, error) {
	var r Run
	err := st.ReadJSON([]string{"runs", id, "run.json"}, &r)
	return &r, err
}

// ListByTask returns all runs for a given task ID.
func ListByTask(st *store.Store, taskID string) ([]Run, error) {
	all, err := List(st)
	if err != nil {
		return nil, err
	}
	var out []Run
	for _, r := range all {
		if r.TaskID == taskID {
			out = append(out, r)
		}
	}
	return out, nil
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
	if r.PID > 0 {
		_ = logEvent(st, r, "system", "info", "", fmt.Sprintf("sending SIGTERM to hermes process %d", r.PID))
		if proc, err := os.FindProcess(r.PID); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
			go func() {
				time.Sleep(10 * time.Second)
				if proc2, err := os.FindProcess(r.PID); err == nil {
					_ = proc2.Signal(syscall.SIGKILL)
				}
			}()
		}
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
		user = config.DefaultHermesUser()
	}

	r.Agent = agentName
	r.Step = step
	_ = logEvent(st, r, agentName, "step", "", step)
	_ = st.WriteJSON([]string{"runs", r.ID, "run.json"}, r)

	prompt := buildPrompt(st, r, t, p, te, step, 0, nil)
	maxTurns := 60
	if t.Type == task.TypeEngineering {
		maxTurns = 120
	}
	activityPath := filepath.Join(st.Root, "runs", r.ID, "activity.log")
	out, err := runHermes(user, profile, prompt, r.Worktree, activityPath, maxTurns, func(pid int) {
		r.PID = pid
		_ = st.WriteJSON([]string{"runs", r.ID, "run.json"}, r)
	})
	hermesFailed := err != nil
	if hermesFailed {
		r.Errors++
		_ = logEvent(st, r, agentName, "error", "hermes", err.Error())
		_ = st.WriteJSON([]string{"runs", r.ID, "run.json"}, r)
	}
	_ = logEvent(st, r, agentName, "tool_call", "hermes", out)
	writeRunMetadata(st, r, t, out, hermesFailed)

	// Auto-commit any changes the agent produced so downstream worktrees inherit them.
	if !hermesFailed && p.RepoPath != "" && r.Worktree != "" {
		_, _ = runGitAsHermes(st.StubMode, user, r.Worktree, "add", "-A")
		if _, err := runGitAsHermes(st.StubMode, user, r.Worktree, "diff", "--cached", "--quiet"); err != nil {
			commitMsg := fmt.Sprintf("agent: %s %s\n\n%s", t.Type, t.ID, t.Title)
			if out, cerr := runGitAsHermes(st.StubMode, user, r.Worktree, "commit", "-m", commitMsg); cerr != nil {
				_ = logEvent(st, r, "system", "error", "git", "commit failed: "+cerr.Error()+"\n"+string(out))
			} else {
				_ = logEvent(st, r, "system", "info", "git", "committed agent changes")
			}
		}
	}

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
	case task.TypeResearch:
		// Write a minimal but valid RESEARCH.md so gate checks pass in stub mode.
		if r.Worktree != "" {
			wtPath := filepath.Join(r.Worktree, "RESEARCH.md")
			_ = os.WriteFile(wtPath, []byte(`# RESEARCH.md

## Stack
- Language: Go
- Framework: standard library

## Architecture
- Directory layout: cmd/main entrypoint, internal packages
- Key files: main.go, go.mod

## Acceptance Criteria
- Greet returns expected string.
- Tests pass.

## Engineering Notes
- Keep changes minimal and focused.
`), 0o644)
			// Register the doc with the project so downstream agents/gates can read it.
			_ = project.AddDoc(st, p.ID, wtPath)
		}
	case task.TypeQAVerify:
		// First QA verification attempt is rejected to exercise the engineering redo path.
		if t.RedoIndex == 0 {
			verdict = "reject"
			reason = "stub: first QA verification rejected to test redo"
			_ = st.WriteJSON([]string{"runs", r.ID, "metadata.json"}, map[string]string{
				"verdict": "reject",
				"reason":  reason,
			})
			break
		}
		// Write a fake diff and review note so gate checks pass.
		if r.Worktree != "" {
			_ = writeStubDiff(r.Worktree, "qa-verify")
		}
		_ = st.WriteJSON([]string{"runs", r.ID, "metadata.json"}, map[string]string{
			"verdict":        "approve",
			"reason":         "stub QA verify passed",
			"qa_review_note": "- checked diff exists\n- checked unit tests\n- checked lint clean",
		})
	case task.TypePMPlan:
		// Generate a multi-task engineering plan with dependencies to test DAG expansion.
		planDir := filepath.Join(r.Worktree, "docs")
		_ = os.MkdirAll(planDir, 0o755)
		plan := stubPlan{
			Tasks: []stubPlanTask{
				{ID: "eng-core", Type: "engineering", Specialization: "backend", Title: "Implement core logic", Dependencies: []string{}},
				{ID: "eng-tests", Type: "engineering", Specialization: "backend", Title: "Add unit tests", Dependencies: []string{"eng-core"}},
				{ID: "eng-cli", Type: "engineering", Specialization: "cli", Title: "Wire CLI commands", Dependencies: []string{"eng-core"}},
				{ID: "eng-docs", Type: "engineering", Specialization: "docs", Title: "Update documentation", Dependencies: []string{"eng-cli"}},
			},
		}
		inner, _ := json.Marshal(plan) // {"tasks":[...]}
		wrapper := map[string]json.RawMessage{"plan": inner}
		planJSON, _ := json.Marshal(wrapper) // {"plan":{"tasks":[...]}}
		planFile := filepath.Join(planDir, "plan.json")
		_ = os.WriteFile(planFile, planJSON, 0o644)
		_ = project.AddDoc(st, p.ID, planFile)
		_ = st.WriteJSON([]string{"runs", r.ID, "metadata.json"}, map[string]string{
			"verdict": "approve",
			"reason":  "stub PM plan generated",
			"plan":    string(planJSON),
		})
	case task.TypeEngineering:
		// Regular engineering tasks always approve in stub mode.
		// (Redos created after a QA reject also approve so the workflow can finish.)
		// Ensure the stub engineering run produces a compilable cmd entrypoint so gate checks pass.
		if r.Worktree != "" {
			cmdDir := filepath.Join(r.Worktree, "cmd", p.ID)
			_ = os.MkdirAll(cmdDir, 0o755)
			mainPath := filepath.Join(cmdDir, "main.go")
			_ = os.WriteFile(mainPath, []byte(`package main

import "fmt"

func Greet(name string) string {
	return fmt.Sprintf("Hello, %s!", name)
}

func main() {
	fmt.Println(Greet("world"))
}
`), 0o644)
			// Replace the placeholder root main.go (if any) with a compilable version
			// so that `go build ./...` does not fail on an empty package main.
			rootMain := filepath.Join(r.Worktree, "main.go")
			if info, err := os.Stat(rootMain); err == nil && info.Size() < 50 {
				_ = os.WriteFile(rootMain, []byte(`package main

func main() {}
`), 0o644)
			}
			testPath := filepath.Join(cmdDir, "main_test.go")
			_ = os.WriteFile(testPath, []byte(`package main

import "testing"

func TestGreet(t *testing.T) {
	if got := Greet("world"); got != "Hello, world!" {
		t.Fatalf("unexpected greeting: %s", got)
	}
}
`), 0o644)
			// Stage and commit changes so they can be merged back to the project repo.
			_, _ = runGitAsHermes(true, "", r.Worktree, "add", ".")
			_, _ = runGitAsHermes(true, "", r.Worktree, "commit", "-m", "stub: engineering "+t.ID)
		}
	default:
		writeRunMetadata(st, r, t, "", false)
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

// isProcessAlive checks whether a PID is still running on Linux.
func isProcessAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// acquireSlot claims one of N filesystem slots; returns the slot number (1..N).
// It reclaims locks held by dead processes so crashes do not permanently
// exhaust the concurrency pool.
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
			// Lock exists: check if owner is alive.
			data, _ := os.ReadFile(path)
			var pid int
			fmt.Sscanf(string(data), "%d", &pid)
			if pid > 0 && !isProcessAlive(pid) {
				_ = os.Remove(path)
				f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
				if err == nil {
					_, _ = f.WriteString(fmt.Sprintf("%d\n", os.Getpid()))
					_ = f.Close()
					return i, nil
				}
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

// activityWriter appends heartbeat messages to a file so watchers can verify
// the agent is still making progress instead of relying on a wall-clock timeout.
type activityWriter struct {
	path string
	mu   sync.Mutex
}

func (a *activityWriter) write(format string, args ...interface{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f, err := os.OpenFile(a.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "[%s] "+format+"\n", append([]interface{}{time.Now().UTC().Format(time.RFC3339)}, args...)...)
}

func runHermes(user, profile, prompt, worktree, activityPath string, maxTurns int, setPID func(int)) (string, error) {
	if maxTurns <= 0 {
		maxTurns = 60
	}
	act := &activityWriter{path: activityPath}
	act.write("start profile=%s worktree=%s", profile, worktree)

	args := []string{
		"--profile", profile,
		"chat", "-q", prompt,
		"--toolsets", "file,terminal",
		"--yolo",
		"--source", "tool",
		"--max-turns", fmt.Sprintf("%d", maxTurns),
	}
	hermesBin := "/home/" + user + "/.local/bin/hermes"
	if _, err := os.Stat(hermesBin); err != nil {
		hermesBin = "hermes"
	}
	baseArgs := append([]string{"-u", user, hermesBin}, args...)
	cmd := exec.Command("sudo", baseArgs...)
	if worktree != "" {
		cmd.Dir = worktree
	}
	// Preserve PATH and other env needed by Hermes when running under sudo.
	cmd.Env = append(os.Environ(), "HOME=/home/"+user)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start hermes: %w", err)
	}
	if setPID != nil && cmd.Process != nil {
		setPID(cmd.Process.Pid)
	}

	var wg sync.WaitGroup
	var outBuf strings.Builder
	var outMu sync.Mutex
	tee := func(r io.Reader) {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			line := scanner.Text()
			outMu.Lock()
			outBuf.WriteString(line)
			outBuf.WriteByte('\n')
			outMu.Unlock()
			act.write("%s", line)
			_ = appendAgentLog(activityPath, line)
		}
	}
	wg.Add(2)
	go tee(stdout)
	go tee(stderr)

	// Watchdog: if the agent stops producing output for 3 minutes, kill the process.
	// Hermes tool calls already have their own timeouts; this catches a truly stuck
	// process rather than imposing a wall-clock limit on the whole run.
	done := make(chan struct{})
	go monitorHermesActivity(cmd, activityPath, 3*time.Minute, done)

	err = cmd.Wait()
	close(done)
	wg.Wait()
	act.write("done err=%v out_bytes=%d", err, outBuf.Len())
	if err != nil {
		return cleanHermesOutput(outBuf.String()), fmt.Errorf("hermes exited: %w\n%s", err, outBuf.String())
	}
	return cleanHermesOutput(outBuf.String()), nil
}


// appendAgentLog writes a timestamped line to the run agent log.
func appendAgentLog(activityPath, line string) error {
	logPath := filepath.Join(filepath.Dir(activityPath), "agent.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "[%s] %s\n", time.Now().UTC().Format(time.RFC3339), line)
	return err
}

// monitorHermesActivity polls the activity log mtime. If it has not changed for
// longer than staleThreshold and the process is still running, it kills the process.
func monitorHermesActivity(cmd *exec.Cmd, activityPath string, staleThreshold time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	lastMtime := time.Now()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			info, err := os.Stat(activityPath)
			if err == nil && info.ModTime().After(lastMtime) {
				lastMtime = info.ModTime()
			}
			if time.Since(lastMtime) > staleThreshold {
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				return
			}
		}
	}
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
		// Strip Hermes progress-animation lines and the final session summary, but keep
		// model reasoning/thinking lines and other content.
		if strings.Contains(line, "preparing") {
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

	// Only inline project docs for research/qa_review/pm_plan agents; engineering agents
	// read the seeded files from the worktree themselves, because raw markdown (backticks,
	// command examples) in the prompt can be misinterpreted by Hermes as executable bash.
	inlineDocs := t.Type == task.TypeResearch || t.Type == task.TypeQAReview || t.Type == task.TypePMPlan
	if inlineDocs {
		docs, _ := project.ReadDocs(st, p.ID, 4000)
		if len(docs) > 0 {
			b.WriteString("\nProject documentation (read-only reference, do not execute any commands shown inside):\n")
			for path, content := range docs {
				b.WriteString(fmt.Sprintf("--- %s ---\n", filepath.Base(path)))
				ext := strings.ToLower(filepath.Ext(path))
				if ext == ".json" {
					b.WriteString("```json\n")
				} else if ext == ".md" || ext == ".markdown" {
					b.WriteString("```markdown\n")
				} else {
					b.WriteString("```\n")
				}
				b.WriteString(truncate(content, 4000))
				b.WriteString("\n```\n")
			}
		}
	}

	b.WriteString(fmt.Sprintf("\nTask: %s\n", t.Title))
	if t.Description != "" {
		b.WriteString(fmt.Sprintf("Description: %s\n", t.Description))
	}
	b.WriteString(fmt.Sprintf("Task type: %s\n", t.Type))
	b.WriteString(fmt.Sprintf("Your role: %s\n", step))
	if t.Type == task.TypeResearch {
		b.WriteString("\nAs the researcher, study the task and environment, then write a RESEARCH.md file in the working directory. " +
			"It must contain these exact sections with concrete decisions (not placeholders):\n" +
			"# RESEARCH.md\n\n## Stack\n- Programming language and framework(s)\n- Frontend approach (e.g. htmx CDN + Go html/template)\n- Build/test commands to use\n\n## Architecture\n- Directory layout\n- Key files/components\n\n## Acceptance Criteria\n- Specific behaviors the implementation must satisfy\n\n## Engineering Notes\n- Constraints the coder/engineer must follow\n\n" +
			"Do not write code. Only write RESEARCH.md. Before claiming the file is written, verify it exists with `ls RESEARCH.md`. The next QA reviewer will verify this document, and the PM/engineer will use it as the source of truth.\n")
	}
	if t.Type == task.TypePMPlan {
		b.WriteString("\nAs the PM planner, produce a detailed implementation plan. " +
			"Write the plan as compact JSON to the file docs/plan.json in the working directory. " +
			"The JSON must be under the key 'plan' with an array of engineering tasks. " +
			"Each task: id, type='engineering', specialization, title, optional description, dependencies (array of ids). " +
			"The JSON must be compact (single line), with no literal newlines inside string values — use \\n escapes if needed. " +
			"CRITICAL: every engineering task must be TINY. A task is too big if it requires more than one of: a single endpoint, a single HTML template, a single handler function, or a single test file. " +
			"Break every feature into the smallest possible sub-tasks. For example, separate 'create API endpoint', 'list API endpoint', 'HTML list template', 'HTML form template', and 'handler wiring' into distinct tasks. " +
			"Prefer tasks that can be completed in under 15 tool-calling iterations. " +
			"Use specializations: backend for JSON API handlers, template for HTML/template files, tests for test files, css for styles, cli for wiring/flags. " +
			"After writing the file, report a concise summary of what you did.\n\n" +
			"Example contents of docs/plan.json:\n" +
			"{\"plan\":{\"tasks\":[{\"id\":\"eng-core\",\"type\":\"engineering\",\"specialization\":\"backend\",\"title\":\"Implement core\",\"dependencies\":[]}]}}\n")
	}
	if t.VerdictReason != "" {
		b.WriteString(fmt.Sprintf("Rejection reason to address: %s\n", t.VerdictReason))
		b.WriteString("You are continuing this task after a previous rejection. Review the previous attempt's worktree if available and address the stated reason before giving your verdict.\n")
	}
	if r.Worktree != "" {
		b.WriteString(fmt.Sprintf("Working directory (git worktree): %s\n", r.Worktree))
		if p.BaseCommit != "" {
			b.WriteString(fmt.Sprintf("Base commit for this worktree: %s\n", p.BaseCommit))
		}
		b.WriteString("You may read files and run git commands here. Prefer small, focused changes.\n")
		switch t.Type {
		case task.TypeQAReview:
			b.WriteString("\nMANDATORY: verify paths and files exist before referring to them. Use `ls`, `find`, or `git status` to confirm actual locations. Before giving your verdict, read RESEARCH.md in this worktree. " +
				"Verify that it contains concrete Stack, Architecture, Acceptance Criteria and Engineering Notes sections. " +
				"If the research document is missing or unclear, reject the task in your summary. " +
				"docs/plan.json is NOT required for this review step.\n")
		case task.TypeQAVerify:
			b.WriteString("\nMANDATORY: verify paths and files exist before referring to them. Verify the implementation against RESEARCH.md acceptance criteria. " +
				"Run the project test command and lint command if available. " +
				"If tests fail or acceptance criteria are not met, reject the task in your summary.\n")
		default:
			b.WriteString("\nRead RESEARCH.md and docs/plan.json in this worktree if they exist; use them as guidance. " +
				"Follow the stack, architecture and acceptance criteria from RESEARCH.md when available. " +
				"If these files are missing, proceed using the task description and produce a small, correct implementation. Do not reject solely because documentation is missing.\n")
		}
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
	b.WriteString("\n\nIMPORTANT: throughout your response, include a brief reasoning block before every significant decision or tool call. " +
		"Wrap each reasoning in XML-like tags: <reasoning>your short reasoning here</reasoning>. " +
		"These reasoning blocks will be captured in the run log for transparency. Keep reasoning concise (1-2 sentences).")
	b.WriteString("\n\nAt the very end of your response, on its own line, you MUST output exactly one of:\n")
	b.WriteString("verdict: approve\n")
	b.WriteString("or\n")
	b.WriteString("verdict: reject\n")
	b.WriteString("Nothing else may follow the verdict line.\n")
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

func runGitAsHermes(stub bool, user, repo string, args ...string) ([]byte, error) {
	quotedArgs := ""
	for _, a := range args {
		if quotedArgs != "" {
			quotedArgs += " "
		}
		quotedArgs += fmt.Sprintf("%q", a)
	}
	var cmd *exec.Cmd
	if stub {
		// In stub mode we run git as the current process user, no sudo required.
		cmd = exec.Command("bash", "-c", fmt.Sprintf("cd %q && git %s", repo, quotedArgs))
	} else {
		cmd = exec.Command("sudo", "-u", user, "bash", "-lc", fmt.Sprintf("cd %q && git %s", repo, quotedArgs))
	}
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

// extractAgentResponse strips the hermes CLI framing (query, banners, resume hints)
// and returns only the assistant's response text.
func extractAgentResponse(out string) string {
	// Hermes CLI output is:
	//   Query: <prompt>
	//   Initializing agent...
	//   ────────────────────────
	//   <blank line>
	//   [optional banner]
	//   <assistant response>
	//   [optional footer + resume hint]
	// The prompt ends at the last line of dashes. Take everything after that line.
	lines := strings.Split(out, "\n")
	lastDash := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "────────────────────────────────────────" {
			lastDash = i
			break
		}
	}
	if lastDash == -1 || lastDash+1 >= len(lines) {
		return out
	}
	return strings.Join(lines[lastDash+1:], "\n")
}

// writeRunMetadata writes a deterministic metadata.json for the orchestrator.
// Agents can be instructed to override via their output; here we use a safe default.
func writeRunMetadata(st *store.Store, r *Run, t *task.Task, agentOutput string, hermesFailed bool) {
	agentOutput = extractAgentResponse(agentOutput)
	meta := map[string]string{
		"verdict": "approve",
		"reason":  "agent completed step " + string(t.Type),
	}
	if hermesFailed {
		meta["verdict"] = "reject"
		meta["reason"] = "hermes run failed"
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
	// Try to parse explicit verdict from the agent output.
	// If the agent explicitly says reject anywhere in its output, reject.
	lower := strings.ToLower(agentOutput)
	rejectMarkers := []string{
		"verdict: reject", "review result: reject", "decision: reject",
		"qa review rejected", "rejected the task", "reject: the task",
		"reject \u2192", "i reject", "task is rejected",
	}
	approveMarkers := []string{
		"verdict: approve", "review result: approve", "decision: approve",
		"qa review approved", "approved the task", "approve: the task",
		"approve \u2192", "i approve", "task is approved",
	}
	rejected := false
	for _, m := range rejectMarkers {
		if strings.Contains(lower, m) {
			rejected = true
			break
		}
	}
	approved := false
	for _, m := range approveMarkers {
		if strings.Contains(lower, m) {
			approved = true
			break
		}
	}
	if rejected {
		meta["verdict"] = "reject"
	} else if approved {
		meta["verdict"] = "approve"
	}
	// Last-5-lines sanity check: only explicit failure markers trigger reject.
	lines := strings.Split(agentOutput, "\n")
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-5; i-- {
		line := strings.ToLower(strings.TrimSpace(lines[i]))
		if strings.Contains(line, "verdict: reject") || strings.Contains(line, "task failed") || strings.Contains(line, "build failed") || strings.Contains(line, "tests failed") {
			meta["verdict"] = "reject"
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
