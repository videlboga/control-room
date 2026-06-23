package dashboard

import (
	"sort"

	"control-room/internal/epic"
	"control-room/internal/project"
	"control-room/internal/run"
	"control-room/internal/store"
	"control-room/internal/task"
	"control-room/internal/team"
)

// DashboardStore wraps the control-room filesystem store for dashboard needs.
type DashboardStore struct {
	*store.Store
}

func NewDashboardStore(st *store.Store) *DashboardStore {
	return &DashboardStore{Store: st}
}

func (ds *DashboardStore) ListProjects() ([]project.Project, error) {
	return project.List(ds.Store)
}

func (ds *DashboardStore) GetProject(id string) (*project.Project, error) {
	return project.Get(ds.Store, id)
}

func (ds *DashboardStore) CreateProject(p *project.Project) error {
	return project.Create(ds.Store, p)
}

func (ds *DashboardStore) AddProjectDoc(projectID, docPath string) error {
	return project.AddDoc(ds.Store, projectID, docPath)
}

func (ds *DashboardStore) ListTeams() ([]team.Team, error) {
	return team.List(ds.Store)
}

func (ds *DashboardStore) GetTeam(id string) (*team.Team, error) {
	return team.Get(ds.Store, id)
}

func (ds *DashboardStore) SaveTeam(t *team.Team) error {
	return ds.Store.WriteJSON([]string{"teams", t.ID + ".json"}, t)
}

func (ds *DashboardStore) ListTasks() ([]task.Task, error) {
	return task.List(ds.Store)
}

func (ds *DashboardStore) GetTask(id string) (*task.Task, error) {
	return task.Get(ds.Store, id)
}

func (ds *DashboardStore) CreateTask(t *task.Task) (*task.Task, error) {
	return task.Create(ds.Store, t)
}

func (ds *DashboardStore) ListRuns() ([]run.Run, error) {
	return run.List(ds.Store)
}

func (ds *DashboardStore) GetRun(id string) (*run.Run, error) {
	return run.Get(ds.Store, id)
}

func (ds *DashboardStore) RunLogs(id string) ([]run.Event, error) {
	return run.Logs(ds.Store, id)
}

func (ds *DashboardStore) ListEpics() ([]epic.Epic, error) {
	return epic.List(ds.Store)
}

func (ds *DashboardStore) ActiveRuns() ([]run.Run, error) {
	runs, err := run.List(ds.Store)
	if err != nil {
		return nil, err
	}
	var active []run.Run
	for _, r := range runs {
		if r.Status == "running" || r.Status == "pending" {
			active = append(active, r)
		}
	}
	sort.Slice(active, func(i, j int) bool {
		return active[i].StartedAt > active[j].StartedAt
	})
	return active, nil
}

func (ds *DashboardStore) RecentRunEvents(runID string, n int) ([]run.Event, error) {
	events, err := run.Logs(ds.Store, runID)
	if err != nil {
		return nil, err
	}
	if len(events) > n {
		return events[len(events)-n:], nil
	}
	return events, nil
}
