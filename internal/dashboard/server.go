package dashboard

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"strconv"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"control-room/internal/epic"
	"control-room/internal/project"
	"control-room/internal/run"
	"control-room/internal/store"
	"control-room/internal/task"
	"control-room/internal/team"
)

//go:embed templates/*.html
var templatesFS embed.FS

var pageTemplates map[string]*template.Template

func LoadTemplates() error {
	funcMap := template.FuncMap{
		"formatTime": func(t string) string {
			pt, err := time.Parse(time.RFC3339, t)
			if err != nil {
				return t
			}
			return pt.Format("2006-01-02 15:04")
		},
		"dict": func(values ...any) (map[string]any, error) {
			if len(values)%2 != 0 {
				return nil, fmt.Errorf("invalid dict call")
			}
			m := make(map[string]any, len(values)/2)
			for i := 0; i < len(values); i += 2 {
				k, ok := values[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict keys must be strings")
				}
				m[k] = values[i+1]
			}
			return m, nil
		},
	}
	pageTemplates = map[string]*template.Template{}
	pages := []string{"index", "agents", "projects", "project", "epics", "tasks", "task", "run"}
	for _, page := range pages {
		t, err := template.New("layout").Funcs(funcMap).ParseFS(templatesFS,
			"templates/layout.html",
			"templates/"+page+".html",
		)
		if err != nil {
			return fmt.Errorf("parse %s: %w", page, err)
		}
		pageTemplates[page] = t
	}
	return nil
}

type Server struct {
	store             *DashboardStore
	eventsBroadcaster *Broadcaster
}

func New(st *store.Store) http.Handler {
	ds := NewDashboardStore(st)
	s := &Server{store: ds, eventsBroadcaster: NewBroadcaster()}
	mux := http.NewServeMux()

	mux.HandleFunc("/", s.index)
	mux.HandleFunc("/agents", s.agentsPage)
	mux.HandleFunc("/projects", s.projectsPage)
	mux.HandleFunc("/projects/{id}", s.projectDetailPage)
	mux.HandleFunc("/epics", s.epicsPage)
	mux.HandleFunc("/tasks", s.tasksPage)
	mux.HandleFunc("/tasks/{id}", s.taskDetailPage)
	mux.HandleFunc("/runs/{id}", s.runDetailPage)

	mux.HandleFunc("/api/v1/agents", s.apiAgents)
	mux.HandleFunc("/api/v1/agents/{team}/{name}", s.apiAgentDelete)
	mux.HandleFunc("/api/v1/projects", s.apiProjects)
	mux.HandleFunc("/api/v1/projects/{id}", s.apiProjectDetail)
	mux.HandleFunc("/api/v1/projects/{id}/docs", s.apiProjectDoc)
	mux.HandleFunc("/api/v1/epics", s.apiEpics)
	mux.HandleFunc("/api/v1/epics/{id}/comments", s.apiEpicComments)
	mux.HandleFunc("/api/v1/tasks", s.apiTasks)
	mux.HandleFunc("/api/v1/tasks/{id}/comments", s.apiTaskComments)
	mux.HandleFunc("/api/v1/runs/active", s.apiActiveRuns)
	mux.HandleFunc("/api/v1/runs/{id}", s.apiRunDetail)
	mux.HandleFunc("/api/v1/runs/{id}/agent-log", s.apiAgentLog)
	mux.HandleFunc("/api/v1/events", s.eventsSSE)

	return mux
}

func render(w http.ResponseWriter, page string, data any) {
	t := pageTemplates[page]
	if t == nil {
		http.Error(w, "template not found: "+page, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func jsonResponse(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	active, _ := s.store.ActiveRuns()
	tasks, _ := s.store.ListTasks()
	projects, _ := s.store.ListProjects()
	render(w, "index", map[string]any{
		"ActiveRuns": active,
		"Tasks":      tasks,
		"Projects":   projects,
	})
}

func (s *Server) agentsPage(w http.ResponseWriter, r *http.Request) {
	teams, _ := s.store.ListTeams()
	render(w, "agents", map[string]any{"Teams": teams})
}

func (s *Server) projectsPage(w http.ResponseWriter, r *http.Request) {
	projects, _ := s.store.ListProjects()
	teams, _ := s.store.ListTeams()
	render(w, "projects", map[string]any{"Projects": projects, "Teams": teams})
}

func (s *Server) projectDetailPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := s.store.GetProject(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	tasks, _ := s.store.ListTasks()
	var projectTasks []task.Task
	for _, t := range tasks {
		if t.ProjectID == id {
			projectTasks = append(projectTasks, t)
		}
	}
	render(w, "project", map[string]any{"Project": p, "Tasks": projectTasks})
}

func (s *Server) epicsPage(w http.ResponseWriter, r *http.Request) {
	projects, _ := s.store.ListProjects()
	epics, _ := s.store.ListEpics()
	render(w, "epics", map[string]any{"Projects": projects, "Epics": epics})
}

func (s *Server) tasksPage(w http.ResponseWriter, r *http.Request) {
	filterProject := r.URL.Query().Get("project")
	filterAgent := r.URL.Query().Get("agent")
	filterStatus := r.URL.Query().Get("status")
	filterType := r.URL.Query().Get("type")

	tasks, _ := s.store.ListTasks()
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

	projects, _ := s.store.ListProjects()
	teams, _ := s.store.ListTeams()
	statuses := []string{"open", "in_progress", "pending_review", "approved", "rejected", "done"}
	types := []string{"research", "qa_review", "pm_plan", "engineering", "qa_verify", "pm_consistency"}

	render(w, "tasks", map[string]any{
		"Tasks":        filtered,
		"Projects":     projects,
		"Teams":        teams,
		"Statuses":     statuses,
		"Types":        types,
		"FilterProj":   filterProject,
		"FilterAgent":  filterAgent,
		"FilterStatus": filterStatus,
		"FilterType":   filterType,
	})
}

func (s *Server) taskDetailPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, err := s.store.GetTask(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	runs, _ := run.ListByTask(s.store.Store, id)
	comments, _ := s.store.Comments("task", t.ID)
	render(w, "task", map[string]any{"Task": t, "Runs": runs, "Comments": comments})
}

func (s *Server) runDetailPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rn, err := s.store.GetRun(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	events, _ := s.store.RunLogs(id)
	render(w, "run", map[string]any{"Run": rn, "Events": events})
}

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
	s.eventsBroadcaster.Notify()
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
	s.eventsBroadcaster.Notify()
	w.WriteHeader(http.StatusNoContent)
}

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
	s.eventsBroadcaster.Notify()
	w.WriteHeader(http.StatusCreated)
	jsonResponse(w, e)
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
	s.eventsBroadcaster.Notify()
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
	s.eventsBroadcaster.Notify()
	jsonResponse(w, map[string]string{"path": destPath})
}

func (s *Server) apiTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		tasks, _ := s.store.ListTasks()
		jsonResponse(w, tasks)
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
	s.eventsBroadcaster.Notify()
	w.WriteHeader(http.StatusCreated)
	jsonResponse(w, created)
}

func (s *Server) apiActiveRuns(w http.ResponseWriter, r *http.Request) {
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

type sseClient chan string

type Broadcaster struct {
	clients map[sseClient]bool
	mu      sync.RWMutex
}

func NewBroadcaster() *Broadcaster {
	b := &Broadcaster{clients: map[sseClient]bool{}}
	go b.loop()
	return b
}

func (b *Broadcaster) loop() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		b.broadcast("tick")
	}
}

func (b *Broadcaster) broadcast(msg string) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for c := range b.clients {
		select {
		case c <- msg:
		default:
		}
	}
}

func (b *Broadcaster) Notify() {
	b.broadcast("update")
}

func (b *Broadcaster) Subscribe() sseClient {
	c := make(sseClient, 8)
	b.mu.Lock()
	b.clients[c] = true
	b.mu.Unlock()
	return c
}

func (b *Broadcaster) Unsubscribe(c sseClient) {
	b.mu.Lock()
	delete(b.clients, c)
	b.mu.Unlock()
	close(c)
}

func (s *Server) eventsSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	client := s.eventsBroadcaster.Subscribe()
	defer s.eventsBroadcaster.Unsubscribe(client)

	s.sendEventsSnapshot(w, flusher)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-client:
			s.sendEventsSnapshot(w, flusher)
		}
	}
}

func (s *Server) sendEventsSnapshot(w http.ResponseWriter, flusher http.Flusher) {
	active, _ := s.store.ActiveRuns()
	tasks, _ := s.store.ListTasks()
	projects, _ := s.store.ListProjects()
	projectMap := make(map[string]string, len(projects))
	for _, p := range projects {
		projectMap[p.ID] = p.Title
	}
	taskMap := make(map[string]string, len(tasks))
	for _, t := range tasks {
		taskMap[t.ID] = t.Title
	}

	type card struct {
		RunID    string      `json:"run_id"`
		Project  string      `json:"project"`
		Task     string      `json:"task"`
		Status   string      `json:"status"`
		Agent    string      `json:"agent"`
		Step     string      `json:"step"`
		Events   []run.Event `json:"events"`
		AgentLog []string    `json:"agent_log"`
	}
	var cards []card
	for _, r := range active {
		events, _ := s.store.RecentRunEvents(r.ID, 20)
		agentLog, _ := s.store.AgentLog(r.ID, 20)
		cards = append(cards, card{
			RunID:    r.ID,
			Project:  projectMap[r.ProjectID],
			Task:     taskMap[r.TaskID],
			Status:   r.Status,
			Agent:    r.Agent,
			Step:     r.Step,
			Events:   events,
			AgentLog: agentLog,
		})
	}
	data, _ := json.Marshal(map[string]any{"active_runs": cards})
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}
