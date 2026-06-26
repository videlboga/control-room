package dashboard

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"control-room/internal/epic"
	"control-room/internal/project"
	"control-room/internal/run"
	"control-room/internal/store"
	"control-room/internal/task"
	"control-room/internal/team"
)

//go:embed static/*
var staticFS embed.FS

// Server is the dashboard HTTP server. It serves a Svelte SPA from the embedded
// static/ directory, a JSON REST API for backwards compatibility, and a
// WebSocket endpoint at /ws for live updates backed by SQLite + fsnotify.
type Server struct {
	store   *DashboardStore
	db      *DB
	hub     *Hub
	watcher *Watcher
}

// New constructs the dashboard server. It opens the SQLite database at
// {workspace}/dashboard.db, syncs the filesystem store into it, starts the
// WebSocket hub and the fsnotify watcher, and returns an http.Handler.
func New(st *store.Store) http.Handler {
	ds := NewDashboardStore(st)
	dbPath := filepath.Join(st.Root, "dashboard.db")
	db, err := InitDB(dbPath)
	if err != nil {
		slog.Error("dashboard sqlite init", "path", dbPath, "err", err)
		// Fall back to an in-memory DB so the server still starts; the watcher
		// will keep retrying syncs.
		db, err = InitDB(":memory:")
		if err != nil {
			slog.Error("dashboard in-memory sqlite init", "err", err)
		}
	}
	if db != nil {
		if err := db.SyncAll(ds); err != nil {
			slog.Warn("dashboard initial sync", "err", err)
		}
	}
	hub := NewHub(db, ds)
	srv := &Server{store: ds, db: db, hub: hub}
	if db != nil {
		w, err := NewWatcher(db, ds, hub)
		if err != nil {
			slog.Warn("watcher init", "err", err)
		} else {
			if err := w.Start(); err != nil {
				slog.Warn("watcher start", "err", err)
			}
			srv.watcher = w
		}
	}
	return srv.routes()
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// WebSocket.
	mux.HandleFunc("/ws", s.hub.HandleWS)

	// REST API (backwards compatible with the old SSR dashboard).
	mux.HandleFunc("/api/v1/agents", s.apiAgents)
	mux.HandleFunc("/api/v1/agents/{team}/{name}", s.apiAgentDelete)
	mux.HandleFunc("/api/v1/projects", s.apiProjects)
	mux.HandleFunc("/api/v1/projects/{id}", s.apiProjectDetail)
	mux.HandleFunc("/api/v1/projects/{id}/docs", s.apiProjectDoc)
	mux.HandleFunc("/api/v1/epics", s.apiEpics)
	mux.HandleFunc("/api/v1/epics/{id}/comments", s.apiEpicComments)
	mux.HandleFunc("/api/v1/tasks", s.apiTasks)
	mux.HandleFunc("/api/v1/tasks/{id}", s.apiTaskUpdate)
	mux.HandleFunc("/api/v1/tasks/{id}/comments", s.apiTaskComments)
	mux.HandleFunc("/api/v1/tasks/{id}/runs", s.apiTaskRuns)
	mux.HandleFunc("/api/v1/tasks/stats", s.apiTaskStats)
	mux.HandleFunc("/api/v1/runs", s.apiRuns)
	mux.HandleFunc("/api/v1/runs/active", s.apiActiveRuns)
	mux.HandleFunc("/api/v1/runs/{id}", s.apiRunDetail)
	mux.HandleFunc("/api/v1/runs/{id}/events", s.apiRunEvents)
	mux.HandleFunc("/api/v1/runs/{id}/agent-log", s.apiAgentLog)
	mux.HandleFunc("/api/v1/runs/{id}/comments", s.apiRunComments)
	mux.HandleFunc("/api/v1/runs/{id}/redo", s.apiRunRedo)
	mux.HandleFunc("/api/v1/events", s.apiEventsLegacy) // legacy SSE shim
	mux.HandleFunc("/api/v1/orchestrate", s.apiOrchestrate)

	// Static SPA — catch-all, must be registered last.
	mux.HandleFunc("/", s.spaHandler)

	return mux
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func jsonResponse(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// broadcast notifies WebSocket subscribers that the board changed.
func (s *Server) broadcast(t string) {
	if s.hub == nil {
		return
	}
	s.hub.BroadcastMessage("board", WSMessage{Channel: "board", Type: t})
}

// ---------------------------------------------------------------------------
// Static SPA handler with index.html fallback.
// ---------------------------------------------------------------------------

func (s *Server) spaHandler(w http.ResponseWriter, r *http.Request) {
	// Reject API-looking paths that didn't match a registered route.
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/")
	if rel == "" {
		rel = "index.html"
	}
	// Prevent path traversal.
	rel = filepath.Clean(rel)
	if strings.HasPrefix(rel, "..") || strings.Contains(rel, "..") {
		http.NotFound(w, r)
		return
	}
	full := "static/" + rel
	data, err := staticFS.ReadFile(full)
	if err != nil {
		// SPA fallback: serve index.html for any unknown route so client-side
		// routing (e.g. /tasks/123) works.
		data, err = staticFS.ReadFile("static/index.html")
		if err != nil {
			http.Error(w, "index.html not found", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
		return
	}
	setContentType(w, rel)
	w.Write(data)
}

func setContentType(w http.ResponseWriter, name string) {
	switch filepath.Ext(name) {
	case ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case ".js":
		w.Header().Set("Content-Type", "application/javascript")
	case ".css":
		w.Header().Set("Content-Type", "text/css")
	case ".json":
		w.Header().Set("Content-Type", "application/json")
	case ".svg":
		w.Header().Set("Content-Type", "image/svg+xml")
	case ".png":
		w.Header().Set("Content-Type", "image/png")
	case ".ico":
		w.Header().Set("Content-Type", "image/x-icon")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}
}

// ---------------------------------------------------------------------------
// REST: agents
// ---------------------------------------------------------------------------

func (s *Server) apiAgents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		teams, _ := s.store.ListTeams()
		jsonResponse(w, teams)
	case http.MethodPost:
		s.apiAgentCreate(w, r)
	default:
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) apiAgentCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Team      string `json:"team"`
		Name      string `json:"name"`
		Role      string `json:"role"`
		Profile   string `json:"profile"`
		CloneFrom string `json:"clone_from,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	t, err := s.store.GetTeam(req.Team)
	if err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	if t.Agents == nil {
		t.Agents = map[string]team.AgentRef{}
	}
	t.Agents[req.Name] = team.AgentRef{
		Profile:   req.Profile,
		Role:      req.Role,
		CloneFrom: req.CloneFrom,
	}
	if err := s.store.SaveTeam(t); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if s.db != nil {
		_ = s.db.SyncTeamFile(s.store, t.ID+".json")
	}
	s.broadcast("team_update")
	w.WriteHeader(http.StatusCreated)
	jsonResponse(w, t)
}

func (s *Server) apiAgentDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	teamID := r.PathValue("team")
	name := r.PathValue("name")
	t, err := s.store.GetTeam(teamID)
	if err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	delete(t.Agents, name)
	if err := s.store.SaveTeam(t); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if s.db != nil {
		_ = s.db.SyncTeamFile(s.store, t.ID+".json")
	}
	s.broadcast("team_update")
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// REST: projects
// ---------------------------------------------------------------------------

func (s *Server) apiProjects(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		projects, _ := s.store.ListProjects()
		jsonResponse(w, projects)
	case http.MethodPost:
		s.apiProjectCreate(w, r)
	default:
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) apiProjectCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		RepoPath    string `json:"repo_path"`
		DefaultTeam string `json:"default_team"`
		TestCommand string `json:"test_command,omitempty"`
		LintCommand string `json:"lint_command,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	p := &project.Project{
		ID:          req.ID,
		Title:       req.Title,
		RepoPath:    req.RepoPath,
		DefaultTeam: req.DefaultTeam,
		TestCommand: req.TestCommand,
		LintCommand: req.LintCommand,
	}
	if err := s.store.CreateProject(p); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if s.db != nil {
		_ = s.db.SyncProjectFile(s.store, p.ID+".json")
	}
	s.broadcast("project_update")
	w.WriteHeader(http.StatusCreated)
	jsonResponse(w, p)
}

func (s *Server) apiProjectDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := s.store.GetProject(id)
	if err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	jsonResponse(w, p)
}

func (s *Server) apiProjectDoc(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectID := r.PathValue("id")
	p, err := s.store.GetProject(projectID)
	if err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("doc")
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()
	destDir := p.DocsDir
	if destDir == "" {
		destDir = filepath.Join(s.store.Root, "docs", projectID)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	destPath := filepath.Join(destDir, header.Filename)
	f, err := os.Create(destPath)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	if _, err := io.Copy(f, file); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.store.AddProjectDoc(projectID, destPath); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if s.db != nil {
		_ = s.db.SyncProjectFile(s.store, p.ID+".json")
	}
	s.broadcast("project_update")
	jsonResponse(w, map[string]string{"path": destPath})
}

// ---------------------------------------------------------------------------
// REST: epics
// ---------------------------------------------------------------------------

func (s *Server) apiEpics(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		epics, _ := s.store.ListEpics()
		jsonResponse(w, epics)
	case http.MethodPost:
		s.apiEpicCreate(w, r)
	default:
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) apiEpicCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		ProjectID   string `json:"project_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	e := &epic.Epic{
		Title:       req.Title,
		Description: req.Description,
		ProjectID:   req.ProjectID,
		Status:      "open",
	}
	e, err := epic.Create(s.store.Store, e)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if s.db != nil {
		_ = s.db.SyncEpicFile(s.store, e.ID+".json")
	}
	s.broadcast("epic_update")
	w.WriteHeader(http.StatusCreated)
	jsonResponse(w, e)
}

func (s *Server) apiEpicComments(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	switch r.Method {
	case http.MethodGet:
		comments, _ := s.store.Comments("epic", id)
		jsonResponse(w, comments)
	case http.MethodPost:
		var req struct {
			Author string `json:"author"`
			Body   string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Body == "" {
			jsonError(w, "body is required", http.StatusBadRequest)
			return
		}
		c, err := s.store.AddComment("epic", id, req.Author, req.Body)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, c)
	default:
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ---------------------------------------------------------------------------
// REST: tasks
// ---------------------------------------------------------------------------

func (s *Server) apiTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		tasks, _ := s.store.ListTasks()
		// Preserve the old query-param filtering for backwards compatibility.
		filterProject := r.URL.Query().Get("project")
		filterAgent := r.URL.Query().Get("agent")
		filterStatus := r.URL.Query().Get("status")
		filterType := r.URL.Query().Get("type")
		var filtered []task.Task
		for _, t := range tasks {
			if filterProject != "" && t.ProjectID != filterProject {
				continue
			}
			if filterAgent != "" && t.AssignedProfile != filterAgent && t.TeamID != filterAgent {
				continue
			}
			if filterStatus != "" && string(t.Status) != filterStatus {
				continue
			}
			if filterType != "" && string(t.Type) != filterType {
				continue
			}
			filtered = append(filtered, t)
		}
		sort.Slice(filtered, func(i, j int) bool { return filtered[i].CreatedAt > filtered[j].CreatedAt })
		jsonResponse(w, filtered)
	case http.MethodPost:
		s.apiTaskCreate(w, r)
	default:
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) apiTaskCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID      string `json:"project_id"`
		TeamID         string `json:"team_id"`
		Title          string `json:"title"`
		Description    string `json:"description,omitempty"`
		Type           string `json:"type"`
		EpicID         string `json:"epic_id,omitempty"`
		Specialization string `json:"specialization,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	t := &task.Task{
		ProjectID:      req.ProjectID,
		TeamID:         req.TeamID,
		Title:          req.Title,
		Description:    req.Description,
		Type:           task.TaskType(req.Type),
		EpicID:         req.EpicID,
		Specialization: req.Specialization,
	}
	created, err := s.store.CreateTask(t)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if s.db != nil {
		_ = s.db.SyncTaskFile(s.store, created.ID+".json")
	}
	s.broadcast("task_update")
	w.WriteHeader(http.StatusCreated)
	jsonResponse(w, created)
}

func (s *Server) apiTaskUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, err := s.store.GetTask(id)
	if err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		jsonResponse(w, t)
	case http.MethodPatch:
		var req struct {
			Status string `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Status != "" {
			t.Status = task.TaskStatus(req.Status)
		}
		if err := s.store.UpdateTask(t); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if s.db != nil {
			_ = s.db.SyncTaskFile(s.store, t.ID+".json")
		}
		s.broadcast("task_update")
		jsonResponse(w, t)
	default:
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) apiTaskComments(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	switch r.Method {
	case http.MethodGet:
		comments, _ := s.store.Comments("task", id)
		jsonResponse(w, comments)
	case http.MethodPost:
		var req struct {
			Author string `json:"author"`
			Body   string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Body == "" {
			jsonError(w, "body is required", http.StatusBadRequest)
			return
		}
		c, err := s.store.AddComment("task", id, req.Author, req.Body)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, c)
	default:
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) apiTaskStats(w http.ResponseWriter, r *http.Request) {
	if s.db != nil {
		stats, err := s.db.GetTaskStats()
		if err == nil {
			jsonResponse(w, stats)
			return
		}
	}
	// Fallback: compute from the filesystem store if SQLite is unavailable.
	tasks, _ := s.store.ListTasks()
	counts := map[string]int{}
	for _, t := range tasks {
		counts[string(t.Status)]++
	}
	var stats []TaskStat
	for st, n := range counts {
		stats = append(stats, TaskStat{Status: st, Count: n})
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].Status < stats[j].Status })
	jsonResponse(w, stats)
}

// ---------------------------------------------------------------------------
// REST: runs
// ---------------------------------------------------------------------------

func (s *Server) apiRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.db != nil {
		runs, err := s.db.GetAllRuns()
		if err == nil {
			jsonResponse(w, runs)
			return
		}
	}
	runs, _ := s.store.ListRuns()
	jsonResponse(w, runs)
}

func (s *Server) apiActiveRuns(w http.ResponseWriter, r *http.Request) {
	if s.db != nil {
		active, err := s.db.GetActiveRuns()
		if err == nil {
			jsonResponse(w, active)
			return
		}
	}
	active, _ := s.store.ActiveRuns()
	jsonResponse(w, active)
}

func (s *Server) apiRunDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rn, err := s.store.GetRun(id)
	if err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	events, _ := s.store.RunLogs(id)
	jsonResponse(w, map[string]any{"run": rn, "events": events})
}

func (s *Server) apiRunEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	limit := 100
	if n := r.URL.Query().Get("limit"); n != "" {
		if parsed, err := strconv.Atoi(n); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if s.db != nil {
		events, err := s.db.GetRunEvents(id, limit)
		if err == nil {
			jsonResponse(w, events)
			return
		}
	}
	// Fallback to filesystem.
	events, _ := s.store.RecentRunEvents(id, limit)
	jsonResponse(w, events)
}

func (s *Server) apiAgentLog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	last := 50
	if n := r.URL.Query().Get("n"); n != "" {
		if parsed, err := strconv.Atoi(n); err == nil {
			last = parsed
		}
	}
	lines, _ := s.store.AgentLog(id, last)
	jsonResponse(w, lines)
}

// apiEventsLegacy is a backwards-compatible SSE shim for old clients that
// polled /api/v1/events. It sends a single snapshot and closes, which is enough
// for the old curl-based monitors; live updates should use /ws instead.
func (s *Server) apiEventsLegacy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	var active any
	if s.db != nil {
		a, err := s.db.GetActiveRuns()
		if err == nil {
			active = a
		}
	} else {
		a, _ := s.store.ActiveRuns()
		active = a
	}
	data, _ := json.Marshal(map[string]any{"active_runs": active})
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

// keepImports is a no-op that prevents goimports from dropping unused imports
// during refactoring. It compiles to nothing.
var _ = run.List

// apiTaskRuns returns all runs for a given task.
func (s *Server) apiTaskRuns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	runs, err := run.ListByTask(s.store.Store, id)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, runs)
}

// apiRunComments handles comments on a run (chat-like timeline).
func (s *Server) apiRunComments(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	switch r.Method {
	case http.MethodGet:
		comments, _ := s.store.Comments("run", id)
		jsonResponse(w, comments)
	case http.MethodPost:
		var req struct {
			Author string `json:"author"`
			Body   string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Body == "" {
			jsonError(w, "body is required", http.StatusBadRequest)
			return
		}
		if req.Author == "" {
			req.Author = "human"
		}
		c, err := s.store.AddComment("run", id, req.Author, req.Body)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.hub.BroadcastMessage("run:"+id, map[string]any{
			"type": "comment",
			"data": c,
		})
		jsonResponse(w, c)
	default:
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// apiRunRedo manually triggers a redo for a run's task.
func (s *Server) apiRunRedo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.PathValue("id")

	// Get the run to find its task
	rn, err := s.store.GetRun(id)
	if err != nil {
		jsonError(w, "run not found", http.StatusNotFound)
		return
	}

	// Get the task
	t, err := s.store.GetTask(rn.TaskID)
	if err != nil {
		jsonError(w, "task not found", http.StatusNotFound)
		return
	}

	// Find the cr binary
	crPath := "cr"
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "cr")
		if _, err := os.Stat(candidate); err == nil {
			crPath = candidate
		}
	}

	// Launch orchestrator for the same epic
	epicID := t.EpicID
	if epicID == "" {
		jsonError(w, "task has no epic — cannot redo", http.StatusBadRequest)
		return
	}

	cmd := exec.Command(crPath, "orchestrate", "run", "--epic", epicID)
	cmd.Dir = filepath.Dir(crPath)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		jsonError(w, "failed to launch orchestrator: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Add a comment about the redo
	_, _ = s.store.AddComment("run", id, "system", "Manual redo triggered — orchestrator launched for epic "+epicID)

	s.hub.BroadcastMessage("run:"+id, map[string]any{
		"type": "redo_triggered",
		"data": map[string]any{"epic_id": epicID, "pid": cmd.Process.Pid},
	})

	jsonResponse(w, map[string]any{
		"status":  "redo_started",
		"epic_id": epicID,
		"pid":     cmd.Process.Pid,
	})
}

// apiOrchestrate launches the orchestrator for a given epic.
// POST /api/v1/orchestrate { "epic_id": "epic_xxx" }
func (s *Server) apiOrchestrate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		EpicID string `json:"epic_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.EpicID == "" {
		jsonError(w, "epic_id is required", http.StatusBadRequest)
		return
	}

	// Find the cr binary relative to the dashboard executable or in PATH.
	crPath := "cr"
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "cr")
		if _, err := os.Stat(candidate); err == nil {
			crPath = candidate
		}
	}

	// Launch orchestrator in background.
	cmd := exec.Command(crPath, "orchestrate", "run", "--epic", req.EpicID)
	cmd.Dir = filepath.Dir(crPath)
	// Detach stdout/stderr so the process doesn't hold the pipe.
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		jsonError(w, "failed to launch orchestrator: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.hub.BroadcastMessage("board", map[string]any{
		"type": "orchestrate_started",
		"data": map[string]any{"epic_id": req.EpicID, "pid": cmd.Process.Pid},
	})

	jsonResponse(w, map[string]any{
		"status":  "started",
		"epic_id": req.EpicID,
		"pid":     cmd.Process.Pid,
	})
}