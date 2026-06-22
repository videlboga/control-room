package gate

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"control-room/internal/project"
	"control-room/internal/run"
	"control-room/internal/store"
	"control-room/internal/task"
)

// Result is the outcome of gate checks.
type Result struct {
	Passed bool     `json:"passed"`
	Errors []string `json:"errors,omitempty"`
}

// Run executes hard checks for a task before a verdict can be trusted.
func Run(st *store.Store, t *task.Task, r *run.Run, p *project.Project) (*Result, error) {
	switch t.Type {
	case task.TypeQAVerify:
		return runQAVerifyChecks(st, t, r, p)
	case task.TypePMConsistency:
		return runPMConsistencyChecks(st, t, r, p)
	default:
		return &Result{Passed: true}, nil
	}
}

func runQAVerifyChecks(st *store.Store, t *task.Task, r *run.Run, p *project.Project) (*Result, error) {
	res := &Result{Passed: true}

	// QA verify validates the final merged state in the project repo (main branch),
	// not a diff in its own worktree.
	verifyDir := p.RepoPath
	if verifyDir == "" {
		res.Errors = append(res.Errors, "no project repo available for verification")
	}

	if p.TestCommand != "" {
		if err := runCommand(verifyDir, p.TestCommand); err != nil {
			res.Errors = append(res.Errors, "tests failed: "+err.Error())
		}
	}
	if p.LintCommand != "" {
		if err := runCommand(verifyDir, p.LintCommand); err != nil {
			res.Errors = append(res.Errors, "lint failed: "+err.Error())
		}
	}

	res.Passed = len(res.Errors) == 0
	return res, nil
}

func runPMConsistencyChecks(st *store.Store, t *task.Task, r *run.Run, p *project.Project) (*Result, error) {
	res := &Result{Passed: true}

	if r.Worktree != "" && hasDiff(r.Worktree) {
		if err := runCommand(r.Worktree, "git merge-tree $(git rev-parse HEAD) $(git rev-parse $(git branch --show-upstream))"); err != nil {
			res.Errors = append(res.Errors, "merge conflicts detected against base branch")
		}
	}

	res.Passed = len(res.Errors) == 0
	return res, nil
}

func hasDiff(worktree string) bool {
	if worktree == "" {
		return false
	}
	out, err := exec.Command("git", "-C", worktree, "diff", "HEAD").CombinedOutput()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}

func runCommand(worktree, command string) error {
	if command == "" || worktree == "" {
		return nil
	}
	cmd := exec.Command("bash", "-lc", command)
	cmd.Dir = worktree
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func readMetadata(st *store.Store, runID string) (map[string]string, error) {
	path := filepath.Join(st.Root, "runs", runID, "metadata.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}
