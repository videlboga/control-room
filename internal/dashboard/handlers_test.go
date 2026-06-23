package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"control-room/internal/project"
	"control-room/internal/store"
	"control-room/internal/task"
	"control-room/internal/team"
)

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil { t.Fatal(err) }
	exec.Command("git", "-C", dir, "init").Run()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# "+dir), 0o644); err != nil { t.Fatal(err) }
	exec.Command("git", "-C", dir, "config", "user.email", "test@example.com").Run()
	exec.Command("git", "-C", dir, "config", "user.name", "Test").Run()
	exec.Command("git", "-C", dir, "add", ".").Run()
	if err := exec.Command("git", "-C", dir, "commit", "-m", "init").Run(); err != nil {
		t.Fatal(err)
	}
}

func setupTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	st := store.New(dir)

	// Seed team
	team1 := &team.Team{
		ID:   "test-team",
		Name: "Test Team",
		Agents: map[string]team.AgentRef{
			"agent1": {Profile: "profile1", Role: "coder"},
		},
	}
	if err := st.WriteJSON([]string{"teams", "test-team.json"}, team1); err != nil {
		t.Fatal(err)
	}

	// Seed project
	p1Dir := filepath.Join(dir, "repo1")
	p2Dir := filepath.Join(dir, "repo2")
	initGitRepo(t, p1Dir)
	initGitRepo(t, p2Dir)
	p1 := &project.Project{ID: "p1", Title: "Project One", RepoPath: p1Dir, DefaultTeam: "test-team"}
	p2 := &project.Project{ID: "p2", Title: "Project Two", RepoPath: p2Dir, DefaultTeam: "test-team"}
	if err := project.Create(st, p1); err != nil { t.Fatal(err) }
	if err := project.Create(st, p2); err != nil { t.Fatal(err) }

	// Seed tasks
	t1 := &task.Task{ProjectID: "p1", TeamID: "test-team", Title: "Task A", Type: "engineering", Status: "open"}
	t2 := &task.Task{ProjectID: "p2", TeamID: "test-team", Title: "Task B", Type: "qa_verify", Status: "approved"}
	t3 := &task.Task{ProjectID: "p1", TeamID: "test-team", Title: "Task C", Type: "engineering", Status: "done"}
	if _, err := task.Create(st, t1); err != nil { t.Fatal(err) }
	if _, err := task.Create(st, t2); err != nil { t.Fatal(err) }
	if _, err := task.Create(st, t3); err != nil { t.Fatal(err) }

	return st
}

func TestProjectListEndpoint(t *testing.T) {
	if err := LoadTemplates(); err != nil {
		t.Fatal(err)
	}
	st := setupTestStore(t)
	handler := New(st)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var projects []project.Project
	if err := json.Unmarshal(rr.Body.Bytes(), &projects); err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}
}

func TestTaskFilterEndpoint(t *testing.T) {
	if err := LoadTemplates(); err != nil {
		t.Fatal(err)
	}
	st := setupTestStore(t)
	handler := New(st)

	req := httptest.NewRequest(http.MethodGet, "/tasks?project=p1&status=open", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Task A") {
		t.Fatalf("expected Task A in response, got: %s", body)
	}
	if strings.Contains(body, "Task B") {
		t.Fatalf("did not expect Task B in filtered response")
	}
}

func TestRunEventStreaming(t *testing.T) {
	if err := LoadTemplates(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	st := store.New(dir)

	// Seed project + task + run
	pDir := filepath.Join(dir, "repo1")
	initGitRepo(t, pDir)
	p := &project.Project{ID: "p1", Title: "P", RepoPath: pDir, DefaultTeam: "team1"}
	project.Create(st, p)
	// Seed minimal team to satisfy project reference
	var teamStub team.Team
	_ = st.WriteJSON([]string{"teams", "team1.json"}, &teamStub)

	tsk := &task.Task{ProjectID: "p1", TeamID: "team1", Title: "T", Type: "engineering", Status: "open"}
	tsk, _ = task.Create(st, tsk)

	runDir := filepath.Join(dir, "runs", "run_test1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runJSON := `{"id":"run_test1","task_id":"` + tsk.ID + `","project_id":"p1","agent":"agent1","step":"implement","status":"running","started_at":"` + time.Now().Format(time.RFC3339) + `"}`
	if err := os.WriteFile(filepath.Join(runDir, "run.json"), []byte(runJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	handler := New(st)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	go func() {
		time.Sleep(1 * time.Second)
		cancel()
	}()

	handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, "data:") {
		t.Fatalf("expected SSE data, got: %s", body)
	}
	if !strings.Contains(body, "run_test1") {
		t.Fatalf("expected run_test1 in SSE, got: %s", body)
	}
}
