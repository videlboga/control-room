package gate

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	case task.TypeResearch:
		return runResearchChecks(st, t, r, p)
	case task.TypeQAReview:
		return runQAReviewChecks(st, t, r, p)
	case task.TypePMPlan:
		return runPMPlanChecks(st, t, r, p)
	case task.TypeEngineering:
		return runEngineeringChecks(st, t, r, p)
	case task.TypeQAVerify:
		return runQAVerifyChecks(st, t, r, p)
	case task.TypePMConsistency:
		return runPMConsistencyChecks(st, t, r, p)
	default:
		return &Result{Passed: true}, nil
	}
}

// runResearchChecks ensures a non-trivial RESEARCH.md exists with required sections.
func runResearchChecks(st *store.Store, t *task.Task, r *run.Run, p *project.Project) (*Result, error) {
	res := &Result{Passed: true}
	// Prefer the task worktree artifact; fall back to project docs for downstream checks.
	docPath := ""
	if r.Worktree != "" {
		wtPath := filepath.Join(r.Worktree, "RESEARCH.md")
		if _, err := os.Stat(wtPath); err == nil {
			docPath = wtPath
		}
	}
	if docPath == "" {
		docPath = filepath.Join(p.DocsDir, "RESEARCH.md")
	}
	if _, err := os.Stat(docPath); err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("RESEARCH.md missing in project docs (%s): %v", docPath, err))
	} else {
		data, err := os.ReadFile(docPath)
		if err != nil {
			res.Errors = append(res.Errors, "cannot read RESEARCH.md: "+err.Error())
		} else {
			text := string(data)
			if len(strings.TrimSpace(text)) < 200 {
				res.Errors = append(res.Errors, "RESEARCH.md is too short (< 200 chars)")
			}
			required := []string{"Stack", "Architecture", "Acceptance Criteria"}
			lower := strings.ToLower(text)
			for _, sec := range required {
				if !strings.Contains(lower, strings.ToLower(sec)) {
					res.Errors = append(res.Errors, "RESEARCH.md missing required section: "+sec)
				}
			}
		}
	}
	res.Passed = len(res.Errors) == 0
	return res, nil
}

// runQAReviewChecks adapts to the parent task type.
func runQAReviewChecks(st *store.Store, t *task.Task, r *run.Run, p *project.Project) (*Result, error) {
	res := &Result{Passed: true}

	parent, err := task.Get(st, t.ParentID)
	if err != nil {
		res.Errors = append(res.Errors, "cannot load parent task: "+err.Error())
		res.Passed = false
		return res, nil
	}

	switch parent.Type {
	case task.TypeResearch:
		// QA reviews the research document.
		g, err := runResearchChecks(st, t, r, p)
		if err != nil {
			res.Errors = append(res.Errors, "research doc audit error: "+err.Error())
		} else {
			res.Errors = append(res.Errors, g.Errors...)
		}
	case task.TypeEngineering:
		// QA reviews the engineering worktree: diff + tests + build.
		if r.Worktree == "" {
			res.Errors = append(res.Errors, "no worktree available for engineering QA review")
		} else {
			if !hasDiff(r.Worktree) {
				res.Errors = append(res.Errors, "engineering worktree has no changes")
			}
			if err := runCommand(r.Worktree, "go test ./..."); err != nil {
				res.Errors = append(res.Errors, "tests failed: "+err.Error())
			}
			if err := runCommand(r.Worktree, "go vet ./..."); err != nil {
				res.Errors = append(res.Errors, "lint failed: "+err.Error())
			}
			if err := runCommand(r.Worktree, "go build ./..."); err != nil {
				res.Errors = append(res.Errors, "build failed: "+err.Error())
			}
		}
	}

	res.Passed = len(res.Errors) == 0
	return res, nil
}

// planFile represents the JSON plan produced by the PM agent.
type planFile struct {
	Plan struct {
		Tasks []struct {
			ID             string   `json:"id"`
			Type           string   `json:"type"`
			Specialization string   `json:"specialization"`
			Title          string   `json:"title"`
			Description    string   `json:"description,omitempty"`
			Dependencies   []string `json:"dependencies,omitempty"`
		} `json:"tasks"`
	} `json:"plan"`
}

// runPMPlanChecks validates docs/plan.json structure.
func runPMPlanChecks(st *store.Store, t *task.Task, r *run.Run, p *project.Project) (*Result, error) {
	res := &Result{Passed: true}
	// Prefer the task worktree artifact; fall back to project docs after copyPlanDoc.
	planPath := ""
	if r.Worktree != "" {
		wtPath := filepath.Join(r.Worktree, "docs", "plan.json")
		if _, err := os.Stat(wtPath); err == nil {
			planPath = wtPath
		}
	}
	if planPath == "" {
		planPath = filepath.Join(p.DocsDir, "docs", "plan.json")
	}
	if _, err := os.Stat(planPath); err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("docs/plan.json missing in project docs (%s): %v", planPath, err))
		res.Passed = false
		return res, nil
	}

	data, err := os.ReadFile(planPath)
	if err != nil {
		res.Errors = append(res.Errors, "cannot read docs/plan.json: "+err.Error())
		res.Passed = false
		return res, nil
	}

	var pf planFile
	if err := json.Unmarshal(data, &pf); err != nil {
		res.Errors = append(res.Errors, "docs/plan.json is not valid JSON: "+err.Error())
		res.Passed = false
		return res, nil
	}

	tasks := pf.Plan.Tasks
	if len(tasks) == 0 {
		res.Errors = append(res.Errors, "docs/plan.json contains no tasks")
	} else if len(tasks) < 2 {
		res.Errors = append(res.Errors, "PM plan too coarse: fewer than 2 engineering tasks")
	}

	ids := make(map[string]bool)
	for _, tt := range tasks {
		if tt.ID == "" {
			res.Errors = append(res.Errors, "plan task missing id")
			continue
		}
		if ids[tt.ID] {
			res.Errors = append(res.Errors, "duplicate plan task id: "+tt.ID)
		}
		ids[tt.ID] = true
		if tt.Type != "engineering" {
			res.Errors = append(res.Errors, "plan task "+tt.ID+" has type '"+tt.Type+"', expected 'engineering'")
		}
		if tt.Specialization == "" {
			res.Errors = append(res.Errors, "plan task "+tt.ID+" missing specialization")
		}
		if strings.TrimSpace(tt.Title) == "" {
			res.Errors = append(res.Errors, "plan task "+tt.ID+" missing title")
		}
	}

	// Validate dependencies exist and detect cycles.
	for _, tt := range tasks {
		for _, dep := range tt.Dependencies {
			if !ids[dep] {
				res.Errors = append(res.Errors, "plan task "+tt.ID+" depends on unknown task "+dep)
			}
		}
	}
	if cycle := detectCycle(tasks); cycle != "" {
		res.Errors = append(res.Errors, "dependency cycle detected: "+cycle)
	}

	res.Passed = len(res.Errors) == 0
	return res, nil
}

func detectCycle(tasks []struct {
	ID             string   `json:"id"`
	Type           string   `json:"type"`
	Specialization string   `json:"specialization"`
	Title          string   `json:"title"`
	Description    string   `json:"description,omitempty"`
	Dependencies   []string `json:"dependencies,omitempty"`
}) string {
	adj := make(map[string][]string)
	ids := make(map[string]bool)
	for _, tt := range tasks {
		ids[tt.ID] = true
		adj[tt.ID] = tt.Dependencies
	}
	state := make(map[string]int) // 0=unvisited, 1=visiting, 2=done
	var path []string
	var dfs func(string) bool
	dfs = func(u string) bool {
		state[u] = 1
		path = append(path, u)
		for _, v := range adj[u] {
			if !ids[v] {
				continue
			}
			if state[v] == 1 {
				return true
			}
			if state[v] == 0 && dfs(v) {
				return true
			}
		}
		path = path[:len(path)-1]
		state[u] = 2
		return false
	}
	for _, tt := range tasks {
		if state[tt.ID] == 0 {
			if dfs(tt.ID) {
				return strings.Join(append(path, path[0]), " -> ")
			}
		}
	}
	return ""
}

// runEngineeringChecks runs hard build/test checks inside the worktree.
func runEngineeringChecks(st *store.Store, t *task.Task, r *run.Run, p *project.Project) (*Result, error) {
	res := &Result{Passed: true}
	if r.Worktree == "" {
		res.Errors = append(res.Errors, "no worktree available for engineering checks")
		res.Passed = false
		return res, nil
	}

	// Always ensure the code builds.
	if err := runCommand(r.Worktree, "go build ./..."); err != nil {
		res.Errors = append(res.Errors, "build failed: "+err.Error())
	}
	// Run tests if any exist; a missing test package is not an error.
	if err := runCommand(r.Worktree, "go test ./..."); err != nil {
		res.Errors = append(res.Errors, "tests failed: "+err.Error())
	}
	if err := runCommand(r.Worktree, "go vet ./..."); err != nil {
		res.Errors = append(res.Errors, "vet failed: "+err.Error())
	}

	// Specialization-specific file checks.
	switch t.Specialization {
	case "frontend":
		if !hasFiles(r.Worktree, `.*\.(html|tmpl|css|js)$`) {
			res.Errors = append(res.Errors, "frontend task produced no html/tmpl/css/js files")
		}
	case "backend":
		if !hasFiles(r.Worktree, `.*\.go$`) {
			res.Errors = append(res.Errors, "backend task produced no .go files")
		}
	case "tests":
		if !hasFiles(r.Worktree, `.*_test\.go$`) {
			res.Errors = append(res.Errors, "tests task produced no *_test.go files")
		}
	case "templates":
		if !hasFiles(r.Worktree, `.*\.(html|tmpl)$`) {
			res.Errors = append(res.Errors, "template task produced no html/tmpl files")
		}
	}

	res.Passed = len(res.Errors) == 0
	return res, nil
}

// runQAVerifyChecks validates the final merged state in the project repo (main branch).
func runQAVerifyChecks(st *store.Store, t *task.Task, r *run.Run, p *project.Project) (*Result, error) {
	res := &Result{Passed: true}

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

	// Structural check: if the plan expects a cmd/main entrypoint, verify it exists.
	if hasFiles(verifyDir, `.*\.go$`) && !hasFiles(verifyDir, `(^|/)cmd/[^/]+/main\.go$`) {
		res.Errors = append(res.Errors, "no cmd/*/main.go entrypoint found in final repo")
	}

	res.Passed = len(res.Errors) == 0
	return res, nil
}

// runPMConsistencyChecks ensures the final merged main has no conflicts and matches the spec.
func runPMConsistencyChecks(st *store.Store, t *task.Task, r *run.Run, p *project.Project) (*Result, error) {
	res := &Result{Passed: true}

	if r.Worktree != "" && hasDiff(r.Worktree) {
		if err := runCommand(r.Worktree, "git merge-tree $(git rev-parse HEAD) $(git rev-parse $(git branch --show-upstream))"); err != nil {
			res.Errors = append(res.Errors, "merge conflicts detected against base branch")
		}
	}

	// Verify RESEARCH.md is present in the final repo, linking implementation back to spec.
	researchPath := filepath.Join(p.RepoPath, "RESEARCH.md")
	if _, err := os.Stat(researchPath); err != nil {
		res.Errors = append(res.Errors, "RESEARCH.md not present in final project repo")
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

func hasFiles(dir, pattern string) bool {
	if dir == "" {
		return false
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	found := false
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		if re.MatchString(rel) {
			found = true
			return filepath.SkipDir
		}
		return nil
	})
	return found
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
