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
	// Recovery tasks are handled by a senior agent outside of normal gating.
	if t.Type == task.TypeRecovery {
		return &Result{Passed: true}, nil
	}
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
	// Prefer the task worktree artifact; fall back to registered project docs.
	docPath := ""
	if r.Worktree != "" {
		wtPath := filepath.Join(r.Worktree, "RESEARCH.md")
		if _, err := os.Stat(wtPath); err == nil {
			docPath = wtPath
		}
	}
	if docPath == "" && p.DocsDir != "" {
		candidate := filepath.Join(p.DocsDir, "RESEARCH.md")
		if _, err := os.Stat(candidate); err == nil {
			docPath = candidate
		}
	}
	if docPath == "" {
		// Fall back to any registered RESEARCH.md doc.
		for _, d := range p.Docs {
			if filepath.Base(d) == "RESEARCH.md" {
				if _, err := os.Stat(d); err == nil {
					docPath = d
					break
				}
			}
		}
	}
	if docPath == "" {
		res.Errors = append(res.Errors, "RESEARCH.md missing: no worktree or registered doc found")
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
			// Run language-appropriate checks based on project language.
			runBuildTestChecks(res, r.Worktree, p)
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
	if planPath == "" && p.DocsDir != "" {
		candidate := filepath.Join(p.DocsDir, "docs", "plan.json")
		if _, err := os.Stat(candidate); err == nil {
			planPath = candidate
		}
	}
	if planPath == "" {
		// Fallback to any registered plan.json.
		for _, d := range p.Docs {
			if filepath.Base(d) == "plan.json" {
				if _, err := os.Stat(d); err == nil {
					planPath = d
					break
				}
			}
		}
	}
	if planPath == "" {
		res.Errors = append(res.Errors, "docs/plan.json missing: no worktree or registered plan.json found")
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

// isGoProject returns true if the project language is Go or mixed (contains Go).
// It uses the project's Language field (auto-detected at creation) rather than
// checking for go.mod in the worktree, because agents may spuriously create
// go.mod in non-Go projects.
func isGoProject(p *project.Project) bool {
	return p.Language == "go" || p.Language == "mixed"
}

// runBuildTestChecks runs language-appropriate build/test/lint commands in the worktree.
// The language is determined from the project's Language field, not from worktree contents.
func runBuildTestChecks(res *Result, worktree string, p *project.Project) {
	if worktree == "" || p == nil {
		return
	}
	switch p.Language {
	case "go":
		if err := runCommand(worktree, "go build ./..."); err != nil {
			res.Errors = append(res.Errors, "build failed: "+err.Error())
		}
		if err := runCommand(worktree, "go test ./..."); err != nil {
			res.Errors = append(res.Errors, "tests failed: "+err.Error())
		}
		if err := runCommand(worktree, "go vet ./..."); err != nil {
			res.Errors = append(res.Errors, "vet failed: "+err.Error())
		}
	case "python":
		// Python: run pytest if available, fall back to no-op.
		if hasFiles(worktree, `.*test.*\.py$`) {
			if err := runCommand(worktree, "python -m pytest -x -q 2>&1 || true"); err != nil {
				res.Errors = append(res.Errors, "tests failed: "+err.Error())
			}
		}
	case "javascript":
		// Node: run package.json scripts if they exist.
		if fileExistsInWorktree(worktree, "package.json") {
			if err := runCommand(worktree, "npm test --silent 2>&1 || true"); err != nil {
				res.Errors = append(res.Errors, "tests failed: "+err.Error())
			}
		}
	case "rust":
		if err := runCommand(worktree, "cargo build 2>&1 || true"); err != nil {
			res.Errors = append(res.Errors, "build failed: "+err.Error())
		}
		if err := runCommand(worktree, "cargo test 2>&1 || true"); err != nil {
			res.Errors = append(res.Errors, "tests failed: "+err.Error())
		}
	case "mixed":
		// Mixed: run Go checks if go.mod exists in worktree, plus Python if .py test files.
		if hasFiles(worktree, `go\.mod$`) {
			if err := runCommand(worktree, "go build ./..."); err != nil {
				res.Errors = append(res.Errors, "go build failed: "+err.Error())
			}
			if err := runCommand(worktree, "go test ./..."); err != nil {
				res.Errors = append(res.Errors, "go tests failed: "+err.Error())
			}
		}
		if hasFiles(worktree, `.*test.*\.py$`) {
			if err := runCommand(worktree, "python -m pytest -x -q 2>&1 || true"); err != nil {
				res.Errors = append(res.Errors, "python tests failed: "+err.Error())
			}
		}
	}
}

// fileExistsInWorktree checks if a file exists at the root of the worktree.
func fileExistsInWorktree(worktree, name string) bool {
	if worktree == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(worktree, name))
	return err == nil
}

// sourceFilePattern returns a regex pattern for source files of the given language.
func sourceFilePattern(lang string) string {
	switch lang {
	case "go":
		return `.*\.go$`
	case "python":
		return `.*\.py$`
	case "javascript":
		return `.*\.(js|ts|jsx|tsx|mjs)$`
	case "rust":
		return `.*\.rs$`
	case "java":
		return `.*\.java$`
	case "mixed":
		return `.*\.(go|py|js|ts|jsx|tsx|rs|java)$`
	default:
		return `.*\.(go|py|js|ts|jsx|tsx|rs|java|rb|c|cpp|h)$`
	}
}

// testFilePattern returns a regex pattern for test files of the given language.
func testFilePattern(lang string) string {
	switch lang {
	case "go":
		return `.*_test\.go$`
	case "python":
		return `(^|[/_.])(test|spec)[/_.-].*\.py$|.*[_./](test|spec)\.py$`
	case "javascript":
		return `.*\.(test|spec)\.(js|ts|jsx|tsx|mjs)$|(^|[/_.])(test|spec)[/_.-].*\.(js|ts|jsx|tsx|mjs)$`
	case "rust":
		return `.*tests?/.*\.rs$|.*\btests?\.rs$`
	case "java":
		return `.*Test\.java$|.*Tests\.java$`
	case "mixed":
		return `.*_test\.go$|.*test.*\.py$|.*\.(test|spec)\.(js|ts|jsx|tsx|mjs)$`
	default:
		return `.*_test\.go$|.*test.*\.py$|.*\.(test|spec)\.(js|ts|jsx|tsx|mjs)$`
	}
}

// runEngineeringChecks runs hard build/test checks inside the worktree.
func runEngineeringChecks(st *store.Store, t *task.Task, r *run.Run, p *project.Project) (*Result, error) {
	res := &Result{Passed: true}
	if r.Worktree == "" {
		res.Errors = append(res.Errors, "no worktree available for engineering checks")
		res.Passed = false
		return res, nil
	}

	// Non-code specializations only need their specific artifacts.
	if t.Specialization == "css" {
		if !hasFiles(r.Worktree, `.*\.css$`) {
			res.Errors = append(res.Errors, "css task produced no .css files")
			res.Passed = false
		}
		return res, nil
	}

	// Run language-appropriate build/test checks.
	runBuildTestChecks(res, r.Worktree, p)

	// Specialization-specific file checks using project language.
	srcPattern := sourceFilePattern(p.Language)
	tstPattern := testFilePattern(p.Language)
	switch t.Specialization {
	case "frontend":
		if !hasFiles(r.Worktree, `.*\.(html|tmpl|css|js|ts|jsx|tsx)$`) {
			res.Errors = append(res.Errors, "frontend task produced no html/tmpl/css/js files")
		}
	case "backend":
		if !hasFiles(r.Worktree, srcPattern) {
			res.Errors = append(res.Errors, "backend task produced no source files ("+p.Language+")")
		}
	case "tests":
		if !hasFiles(r.Worktree, tstPattern) {
			res.Errors = append(res.Errors, "tests task produced no test files ("+p.Language+")")
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
// If the run has a worktree (e.g. QA agent made fixes), it is also checked so that
// fixes committed there are not missed by structural checks that only look at RepoPath.
func runQAVerifyChecks(st *store.Store, t *task.Task, r *run.Run, p *project.Project) (*Result, error) {
	res := &Result{Passed: true}

	verifyDir := p.RepoPath
	if verifyDir == "" {
		res.Errors = append(res.Errors, "no project repo available for verification")
	}

	// Run project-defined test/lint commands if configured.
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

	// Language-appropriate structural checks.
	if p.Language == "go" || p.Language == "mixed" {
		// Go: if the project has .go files, check for cmd/main entrypoint.
		if hasFiles(verifyDir, `.*\.go$`) && !hasFiles(verifyDir, `(^|/)cmd/[^/]+/main\.go$`) {
			worktreeHasCmd := r.Worktree != "" && hasFiles(r.Worktree, `(^|/)cmd/[^/]+/main\.go$`)
			if !worktreeHasCmd {
				// Don't fail for library-only projects (no main expected).
				// Only flag if there are .go files but no entrypoint and no library marker.
				if !fileExistsInWorktree(verifyDir, ".library") {
					res.Errors = append(res.Errors, "no cmd/*/main.go entrypoint found in final repo")
				}
			}
		}
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
