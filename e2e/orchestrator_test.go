package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"control-room/internal/epic"
	"control-room/internal/store"
	"control-room/internal/task"
)

// TestOrchestratorStubEndToEnd builds the CLI and runs a full epic through the
// stub orchestrator, asserting that every expected step is reached.
func TestOrchestratorStubEndToEnd(t *testing.T) {
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "demo-repo")
	workspace := filepath.Join(tmp, "workspace")
	crBin := filepath.Join(tmp, "cr")

	buildCR(t, crBin)
	initDemoRepo(t, repoDir)

	runCR(t, crBin, workspace, "project", "create",
		"--id", "demo",
		"--title", "Demo project",
		"--repo", repoDir,
		"--default-team", "dev",
		"--test-command", "go test ./...",
		"--lint-command", "go vet ./...",
	)

	teamFile := writeTeamFile(t, tmp)
	runCR(t, crBin, workspace, "team", "create", "--file", teamFile)

	out := runCR(t, crBin, workspace, "epic", "create",
		"--title", "Add auth",
		"--description", "Add JWT auth to the service",
		"--project", "demo",
	)
	epicID := epicIDFromOutput(out)
	if epicID == "" {
		t.Fatalf("failed to parse epic id from output: %s", out)
	}

	// Run synchronously to make assertions deterministic.
	runCR(t, crBin, workspace, "orchestrate", "run", "--epic", epicID, "--stub")

	st := &store.Store{Root: workspace}

	e, err := epic.Get(st, epicID)
	if err != nil {
		t.Fatalf("failed to get epic: %v", err)
	}
	if e.Status != "done" {
		t.Fatalf("expected epic status done, got %s", e.Status)
	}

	tasks, err := task.ListByEpic(st, epicID)
	if err != nil {
		t.Fatalf("failed to list tasks: %v", err)
	}

	counts := countTaskTypes(tasks)
	want := map[string]int{
		"research":       1,
		"qa_review":      1,
		"pm_plan":        1,
		"engineering":    4, // stub plan has 4 engineering tasks
		"qa_verify":      2, // first rejected, then redo
		"pm_consistency": 1,
	}
	for typ, n := range want {
		if counts[typ] != n {
			t.Fatalf("expected %d %s tasks, got %d (counts=%v)", n, typ, counts[typ], counts)
		}
	}

	// Engineering tasks should be done/approved and qa_verify redo should be approved.
	approved := 0
	for _, tt := range tasks {
		if tt.Status == task.StatusApproved || tt.Status == task.StatusDone {
			approved++
		}
	}
	if approved < len(tasks)-1 {
		t.Fatalf("expected most tasks approved/done, got %d/%d", approved, len(tasks))
	}
}

// TestOrchestratorWatchParallel runs the watch command and verifies that
// independent engineering tasks started in the same batch.
func TestOrchestratorWatchParallel(t *testing.T) {
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "demo-repo")
	workspace := filepath.Join(tmp, "workspace")
	crBin := filepath.Join(tmp, "cr")

	buildCR(t, crBin)
	initDemoRepo(t, repoDir)

	runCR(t, crBin, workspace, "project", "create",
		"--id", "demo",
		"--title", "Demo project",
		"--repo", repoDir,
		"--default-team", "dev",
		"--test-command", "go test ./...",
		"--lint-command", "go vet ./...",
	)

	teamFile := writeTeamFile(t, tmp)
	runCR(t, crBin, workspace, "team", "create", "--file", teamFile)

	out := runCR(t, crBin, workspace, "epic", "create",
		"--title", "Watch test",
		"--description", "Test watch parallelism",
		"--project", "demo",
	)
	epicID := epicIDFromOutput(out)

	output := runCR(t, crBin, workspace, "orchestrate", "watch", "--epic", epicID, "--stub")

	if !strings.Contains(output, "batch_ready 2") {
		t.Fatalf("expected parallel batch of 2 engineering tasks, output:\n%s", output)
	}
	if !strings.Contains(output, "epic_done") {
		t.Fatalf("expected epic_done in output:\n%s", output)
	}
}

func buildCR(t *testing.T, out string) {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("failed to get project root: %v", err)
	}
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", out, "./cmd/cr")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"GOPATH="+filepath.Join(os.TempDir(), "go-e2e-cache"),
		"GOMODCACHE="+filepath.Join(os.TempDir(), "go-e2e-cache", "pkg", "mod"),
		"GOCACHE="+filepath.Join(os.TempDir(), "go-e2e-build-cache"),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build cr: %v\n%s", err, out)
	}
}

func initDemoRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create repo dir: %v", err)
	}
	writeFile(t, filepath.Join(dir, "go.mod"), "module demo\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nimport \"fmt\"\n\nfunc Greet(name string) string {\n\treturn fmt.Sprintf(\"Hello, %s!\", name)\n}\n\nfunc main() {\n\tfmt.Println(Greet(\"world\"))\n}\n")
	writeFile(t, filepath.Join(dir, "main_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestGreet(t *testing.T) {\n\tif got := Greet(\"world\"); got != \"Hello, world!\" {\n\t\tt.Fatalf(\"unexpected greeting: %s\", got)\n\t}\n}\n")

	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"add", "."},
		{"commit", "-m", "init"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
}

func writeTeamFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "team.json")
	data := map[string]interface{}{
		"id":   "dev",
		"name": "Dev team",
		"agents": map[string]interface{}{
			"coder": map[string]interface{}{"role": "worker", "profile": "hw_agent_coder"},
		},
	}
	b, _ := json.Marshal(data)
	writeFile(t, path, string(b))
	return path
}

func runCR(t *testing.T, bin, workspace string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"--stub", "-w", workspace}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cr %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

func epicIDFromOutput(out string) string {
	// Expected: "epic <id> created"
	parts := strings.Fields(out)
	for i, p := range parts {
		if p == "epic" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
}

func countTaskTypes(tasks []task.Task) map[string]int {
	m := make(map[string]int)
	for _, t := range tasks {
		m[string(t.Type)]++
	}
	return m
}

// allow unused imports to be removed by the compiler
var _ = fmt.Sprintf
var _ = time.Now
