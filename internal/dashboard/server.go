package dashboard

import (
	"encoding/json"
	"net/http"

	"control-room/internal/epic"
	"control-room/internal/project"
	"control-room/internal/run"
	"control-room/internal/store"
	"control-room/internal/task"
	"control-room/internal/team"
)

type Server struct {
	store *store.Store
}

func New(st *store.Store) http.Handler {
	s := &Server{store: st}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.index)
	mux.HandleFunc("/epics/{id}", s.epicDetail)
	mux.HandleFunc("/projects/{id}", s.projectDetail)
	mux.HandleFunc("/tasks/{id}", s.taskDetail)
	mux.HandleFunc("/runs/{id}", s.runDetail)
	mux.HandleFunc("/api/tasks/{id}/run", s.startRun)
	mux.HandleFunc("/api/runs/{id}/cancel", s.cancelRun)
	mux.HandleFunc("/api/runs/{id}/logs", s.runLogs)
	mux.HandleFunc("/api/projects/{id}/tasks", s.apiTasks)
	mux.HandleFunc("/api/projects/{id}/runs", s.apiRuns)
	return mux
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	projects, _ := project.List(s.store)
	teams, _ := team.List(s.store)
	epics, _ := epic.List(s.store)
	tasks, _ := task.List(s.store)
	runs, _ := run.List(s.store)
	render(w, "index", map[string]any{
		"Projects": projects,
		"Teams":    teams,
		"Epics":    epics,
		"Tasks":    tasks,
		"Runs":     runs,
	})
}

func (s *Server) epicDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	e, err := epic.Get(s.store, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	tasks, _ := task.List(s.store)
	var epicTasks []task.Task
	for _, t := range tasks {
		if t.EpicID == id {
			epicTasks = append(epicTasks, t)
		}
	}
	render(w, "epic", map[string]any{"Epic": e, "Tasks": epicTasks})
}

func (s *Server) projectDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := project.Get(s.store, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	tasks, _ := task.List(s.store)
	var projectTasks []task.Task
	for _, t := range tasks {
		if t.ProjectID == id {
			projectTasks = append(projectTasks, t)
		}
	}
	render(w, "project", map[string]any{"Project": p, "Tasks": projectTasks})
}

func (s *Server) taskDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, err := task.Get(s.store, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	runs, _ := run.ListByTask(s.store, id)
	render(w, "task", map[string]any{"Task": t, "Runs": runs})
}

func (s *Server) runDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rn, err := run.Get(s.store, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	events, _ := run.Logs(s.store, id)
	render(w, "run", map[string]any{"Run": rn, "Events": events})
}

func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	taskID := r.PathValue("id")
	rn, err := run.Start(s.store, taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rn)
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	runID := r.PathValue("id")
	if err := run.Cancel(s.store, runID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) runLogs(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	events, err := run.Logs(s.store, runID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(events)
}

func (s *Server) apiTasks(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	tasks, _ := task.List(s.store)
	var out []task.Task
	for _, t := range tasks {
		if t.ProjectID == projectID {
			out = append(out, t)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (s *Server) apiRuns(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	runs, _ := run.List(s.store)
	var out []run.Run
	for _, r := range runs {
		if r.ProjectID == projectID {
			out = append(out, r)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}
