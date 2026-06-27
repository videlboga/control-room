package dashboard

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"control-room/internal/epic"
	"control-room/internal/project"
	"control-room/internal/run"
	"control-room/internal/task"
	"control-room/internal/team"
)

// DB is the SQLite-backed dashboard database. It mirrors the filesystem JSON
// store into queryable tables so the HTTP handlers never have to scan every
// file on every request (the old SSR dashboard did, which leaked 22GB RSS).
type DB struct {
	db *sql.DB
	mu sync.Mutex
}

// InitDB opens (or creates) the SQLite database at path and applies the schema.
// It returns a ready *DB. The caller is responsible for Close().
func InitDB(path string) (*DB, error) {
	// Ensure the parent directory exists so modernc can create the file.
	if dir := filepath.Dir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	d, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// SQLite is single-writer; one connection is enough and avoids lock churn.
	d.SetMaxOpenConns(1)
	if err := d.Ping(); err != nil {
		d.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	wdb := &DB{db: d}
	if err := wdb.createSchema(); err != nil {
		d.Close()
		return nil, err
	}
	return wdb, nil
}

// Close releases the underlying database connection.
func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) createSchema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS projects (
			id            TEXT PRIMARY KEY,
			title         TEXT NOT NULL DEFAULT '',
			repo_path     TEXT NOT NULL DEFAULT '',
			docs_dir      TEXT NOT NULL DEFAULT '',
			docs          TEXT NOT NULL DEFAULT '[]',
			default_team  TEXT NOT NULL DEFAULT '',
			rules         TEXT NOT NULL DEFAULT '[]',
			test_command  TEXT NOT NULL DEFAULT '',
			lint_command  TEXT NOT NULL DEFAULT '',
			base_commit   TEXT NOT NULL DEFAULT '',
			created_at    TEXT NOT NULL DEFAULT '',
			updated_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS epics (
			id          TEXT PRIMARY KEY,
			display_id  TEXT NOT NULL DEFAULT '',
			title       TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			project_id  TEXT NOT NULL DEFAULT '',
			team_id     TEXT NOT NULL DEFAULT '',
			status      TEXT NOT NULL DEFAULT '',
			created_at  TEXT NOT NULL DEFAULT '',
			updated_at  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS tasks (
			id                  TEXT PRIMARY KEY,
			display_id          TEXT NOT NULL DEFAULT '',
			title               TEXT NOT NULL DEFAULT '',
			description         TEXT NOT NULL DEFAULT '',
			type                TEXT NOT NULL DEFAULT '',
			status              TEXT NOT NULL DEFAULT '',
			project_id          TEXT NOT NULL DEFAULT '',
			epic_id             TEXT NOT NULL DEFAULT '',
			team_id             TEXT NOT NULL DEFAULT '',
			parent_id           TEXT NOT NULL DEFAULT '',
			specialization      TEXT NOT NULL DEFAULT '',
			assigned_profile    TEXT NOT NULL DEFAULT '',
			assigned_agent_name TEXT NOT NULL DEFAULT '',
			verdict             TEXT NOT NULL DEFAULT '',
			verdict_reason      TEXT NOT NULL DEFAULT '',
			"group"             TEXT NOT NULL DEFAULT '',
			redo_index          INTEGER NOT NULL DEFAULT 0,
			created_at          TEXT NOT NULL DEFAULT '',
			started_at          TEXT NOT NULL DEFAULT '',
			ended_at            TEXT NOT NULL DEFAULT '',
			updated_at          TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS runs (
			id          TEXT PRIMARY KEY,
			display_id  TEXT NOT NULL DEFAULT '',
			task_id     TEXT NOT NULL DEFAULT '',
			project_id  TEXT NOT NULL DEFAULT '',
			team_id     TEXT NOT NULL DEFAULT '',
			status      TEXT NOT NULL DEFAULT '',
			branch      TEXT NOT NULL DEFAULT '',
			worktree    TEXT NOT NULL DEFAULT '',
			agent       TEXT NOT NULL DEFAULT '',
			step        TEXT NOT NULL DEFAULT '',
			errors      INTEGER NOT NULL DEFAULT 0,
			summary     TEXT NOT NULL DEFAULT '',
			started_at  TEXT NOT NULL DEFAULT '',
			ended_at    TEXT NOT NULL DEFAULT '',
			tool_use_count INTEGER NOT NULL DEFAULT 0,
			updated_at  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS run_events (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id      TEXT NOT NULL DEFAULT '',
			timestamp   TEXT NOT NULL DEFAULT '',
			agent       TEXT NOT NULL DEFAULT '',
			type        TEXT NOT NULL DEFAULT '',
			step        TEXT NOT NULL DEFAULT '',
			tool        TEXT NOT NULL DEFAULT '',
			payload     TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS teams (
			id         TEXT PRIMARY KEY,
			name       TEXT NOT NULL DEFAULT '',
			workflow   TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS agents (
			id          TEXT PRIMARY KEY,
			team_id     TEXT NOT NULL DEFAULT '',
			name        TEXT NOT NULL DEFAULT '',
			profile     TEXT NOT NULL DEFAULT '',
			role        TEXT NOT NULL DEFAULT '',
			parent      TEXT NOT NULL DEFAULT '',
			clone_from  TEXT NOT NULL DEFAULT '',
			avatar00    TEXT NOT NULL DEFAULT '',
			avatar01    TEXT NOT NULL DEFAULT '',
			avatar02    TEXT NOT NULL DEFAULT '',
			avatar10    TEXT NOT NULL DEFAULT '',
			avatar11    TEXT NOT NULL DEFAULT '',
			avatar12    TEXT NOT NULL DEFAULT '',
			avatar20    TEXT NOT NULL DEFAULT '',
			avatar21    TEXT NOT NULL DEFAULT '',
			avatar22    TEXT NOT NULL DEFAULT '',
			updated_at  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT ''
		)`,
		// Memory layers — stores raw, narrative, and policy entries per node.
		`CREATE TABLE IF NOT EXISTS memory_entries (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			node_type  TEXT NOT NULL,             -- 'workspace' | 'project' | 'task' | 'run'
			node_id    TEXT NOT NULL,             -- entity ID (or 'workspace' for workspace)
			layer      TEXT NOT NULL,             -- 'raw' | 'narrative' | 'policy'
			content    TEXT NOT NULL,             -- the memory content (text or JSON)
			source     TEXT NOT NULL DEFAULT '',  -- who wrote it: 'system' | 'agent' | 'human'
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_memory_node ON memory_entries(node_type, node_id)`,
		`CREATE INDEX IF NOT EXISTS idx_memory_layer ON memory_entries(layer)`,
		// Indexes — the hot dashboard queries filter on status/project/run.
		`CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_project ON tasks(project_id)`,
		`CREATE INDEX IF NOT EXISTS idx_runs_status ON runs(status)`,
		`CREATE INDEX IF NOT EXISTS idx_runs_task ON runs(task_id)`,
		`CREATE INDEX IF NOT EXISTS idx_run_events_run ON run_events(run_id)`,
		`CREATE INDEX IF NOT EXISTS idx_run_events_type ON run_events(type)`,
		`CREATE INDEX IF NOT EXISTS idx_epics_project ON epics(project_id)`,
		`CREATE INDEX IF NOT EXISTS idx_agents_team ON agents(team_id)`,
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range stmts {
		if _, err := d.db.Exec(s); err != nil {
			return fmt.Errorf("schema: %w (stmt=%s)", err, firstLine(s))
		}
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// ---------------------------------------------------------------------------
// SyncAll: read every JSON file from the store and upsert it into SQLite.
// ---------------------------------------------------------------------------

// SyncAll reads all JSON files from the control-room filesystem store and
// populates the SQLite tables. It is safe to call on startup and on demand.
func (d *DB) SyncAll(ds *DashboardStore) error {
	if ds == nil {
		return fmt.Errorf("nil store")
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("begin sync tx: %w", err)
	}
	defer tx.Rollback()

	// Wipe the mirror tables so deleted files don't linger. Events are
	// regenerated from events.jsonl so they can be cleared too.
	for _, t := range []string{"run_events", "runs", "tasks", "epics", "projects", "teams", "agents"} {
		if _, err := tx.Exec("DELETE FROM " + t); err != nil {
			return fmt.Errorf("clear %s: %w", t, err)
		}
	}

	// projects
	projects, err := ds.ListProjects()
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}
	for i := range projects {
		if err := upsertProject(tx, &projects[i]); err != nil {
			return err
		}
	}

	// epics
	epics, err := ds.ListEpics()
	if err != nil {
		return fmt.Errorf("list epics: %w", err)
	}
	for i := range epics {
		if err := upsertEpic(tx, &epics[i]); err != nil {
			return err
		}
	}

	// tasks
	tasks, err := ds.ListTasks()
	if err != nil {
		return fmt.Errorf("list tasks: %w", err)
	}
	for i := range tasks {
		if err := upsertTask(tx, &tasks[i]); err != nil {
			return err
		}
	}

	// runs (+ events)
	runs, err := ds.ListRuns()
	if err != nil {
		return fmt.Errorf("list runs: %w", err)
	}
	for i := range runs {
		if err := upsertRunTx(tx, &runs[i]); err != nil {
			return err
		}
		events, err := ds.RunLogs(runs[i].ID)
		if err != nil {
			continue
		}
		for _, ev := range events {
			if err := insertEventTx(tx, &ev); err != nil {
				return err
			}
		}
	}

	// teams + agents
	teams, err := ds.ListTeams()
	if err != nil {
		return fmt.Errorf("list teams: %w", err)
	}
	for i := range teams {
		if err := upsertTeam(tx, &teams[i]); err != nil {
			return err
		}
		for name, ref := range teams[i].Agents {
			if err := upsertAgent(tx, teams[i].ID, name, &ref); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

// ---------------------------------------------------------------------------
// Upsert helpers (transaction-aware).
// ---------------------------------------------------------------------------

func upsertProject(tx *sql.Tx, p *project.Project) error {
	docs, _ := json.Marshal(p.Docs)
	rules, _ := json.Marshal(p.Rules)
	_, err := tx.Exec(`INSERT INTO projects
		(id,title,repo_path,docs_dir,docs,default_team,rules,test_command,lint_command,base_commit,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			title=excluded.title, repo_path=excluded.repo_path, docs_dir=excluded.docs_dir,
			docs=excluded.docs, default_team=excluded.default_team, rules=excluded.rules,
			test_command=excluded.test_command, lint_command=excluded.lint_command,
			base_commit=excluded.base_commit, created_at=excluded.created_at,
			updated_at=CURRENT_TIMESTAMP`,
		p.ID, p.Title, p.RepoPath, p.DocsDir, string(docs), p.DefaultTeam,
		string(rules), p.TestCommand, p.LintCommand, p.BaseCommit, p.CreatedAt)
	return err
}

func upsertEpic(tx *sql.Tx, e *epic.Epic) error {
	_, err := tx.Exec(`INSERT INTO epics
		(id,display_id,title,description,project_id,team_id,status,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			display_id=excluded.display_id, title=excluded.title, description=excluded.description,
			project_id=excluded.project_id, team_id=excluded.team_id, status=excluded.status,
			created_at=excluded.created_at, updated_at=CURRENT_TIMESTAMP`,
		e.ID, e.DisplayID, e.Title, e.Description, e.ProjectID, e.TeamID, e.Status, e.CreatedAt)
	return err
}

func upsertTask(tx *sql.Tx, t *task.Task) error {
	_, err := tx.Exec(`INSERT INTO tasks
		(id,display_id,title,description,type,status,project_id,epic_id,team_id,parent_id,
		specialization,assigned_profile,assigned_agent_name,verdict,verdict_reason,"group",redo_index,
		created_at,started_at,ended_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			display_id=excluded.display_id, title=excluded.title, description=excluded.description,
			type=excluded.type, status=excluded.status, project_id=excluded.project_id, epic_id=excluded.epic_id,
			team_id=excluded.team_id, parent_id=excluded.parent_id, specialization=excluded.specialization,
			assigned_profile=excluded.assigned_profile, assigned_agent_name=excluded.assigned_agent_name,
			verdict=excluded.verdict, verdict_reason=excluded.verdict_reason, "group"=excluded."group",
			redo_index=excluded.redo_index, created_at=excluded.created_at, started_at=excluded.started_at,
			ended_at=excluded.ended_at, updated_at=CURRENT_TIMESTAMP`,
		t.ID, t.DisplayID, t.Title, t.Description, string(t.Type), string(t.Status),
		t.ProjectID, t.EpicID, t.TeamID, t.ParentID, t.Specialization, t.AssignedProfile,
		t.AssignedAgentName, t.Verdict, t.VerdictReason, t.Group, t.RedoIndex,
		t.CreatedAt, t.StartedAt, t.EndedAt)
	return err
}

func upsertRunTx(tx *sql.Tx, r *run.Run) error {
	_, err := tx.Exec(`INSERT INTO runs
		(id,display_id,task_id,project_id,team_id,status,branch,worktree,agent,step,errors,summary,started_at,ended_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			display_id=excluded.display_id, task_id=excluded.task_id, project_id=excluded.project_id,
			team_id=excluded.team_id, status=excluded.status, branch=excluded.branch, worktree=excluded.worktree,
			agent=excluded.agent, step=excluded.step, errors=excluded.errors, summary=excluded.summary,
			started_at=excluded.started_at, ended_at=excluded.ended_at, updated_at=CURRENT_TIMESTAMP`,
		r.ID, r.DisplayID, r.TaskID, r.ProjectID, r.TeamID, r.Status, r.Branch, r.Worktree,
		r.Agent, r.Step, r.Errors, r.Summary, r.StartedAt, r.EndedAt)
	return err
}

func upsertTeam(tx *sql.Tx, te *team.Team) error {
	workflow, _ := json.Marshal(te.Workflow)
	_, err := tx.Exec(`INSERT INTO teams
		(id,name,workflow,created_at,updated_at)
		VALUES (?,?,?,?,CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, workflow=excluded.workflow, created_at=excluded.created_at,
			updated_at=CURRENT_TIMESTAMP`,
		te.ID, te.Name, string(workflow), te.CreatedAt)
	return err
}

func upsertAgent(tx *sql.Tx, teamID, name string, ref *team.AgentRef) error {
	id := teamID + "/" + name
	// The 9 avatar columns (3x3 grid: tool-use tier x redo tier) are empty by
	// default; the frontend fills them in from run statistics.
	_, err := tx.Exec(`INSERT INTO agents
		(id,team_id,name,profile,role,parent,clone_from,
		avatar00,avatar01,avatar02,avatar10,avatar11,avatar12,avatar20,avatar21,avatar22,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			team_id=excluded.team_id, name=excluded.name, profile=excluded.profile,
			role=excluded.role, parent=excluded.parent, clone_from=excluded.clone_from,
			updated_at=CURRENT_TIMESTAMP`,
		id, teamID, name, ref.Profile, ref.Role, ref.Parent, ref.CloneFrom,
		"", "", "", "", "", "", "", "", "")
	return err
}

func insertEventTx(tx *sql.Tx, ev *run.Event) error {
	_, err := tx.Exec(`INSERT INTO run_events
		(run_id,timestamp,agent,type,step,tool,payload)
		VALUES (?,?,?,?,?,?,?)`,
		ev.RunID, ev.Timestamp, ev.Agent, ev.Type, ev.Step, ev.Tool, ev.Payload)
	return err
}

// ---------------------------------------------------------------------------
// Public query API used by the HTTP handlers.
// ---------------------------------------------------------------------------

// RunRow is a run row from SQLite. It mirrors run.Run for JSON serialization.
type RunRow struct {
	ID           string `json:"id"`
	DisplayID    string `json:"display_id,omitempty"`
	TaskID       string `json:"task_id"`
	ProjectID    string `json:"project_id"`
	TeamID       string `json:"team_id"`
	Status       string `json:"status"`
	Branch       string `json:"branch"`
	Worktree     string `json:"worktree"`
	Agent        string `json:"agent"`
	Step         string `json:"step"`
	Errors       int    `json:"errors"`
	Summary      string `json:"summary,omitempty"`
	StartedAt    string `json:"started_at"`
	EndedAt      string `json:"ended_at,omitempty"`
	ToolUseCount int    `json:"tool_use_count"`
}

// EventRow is a run_event row from SQLite.
type EventRow struct {
	ID        int64  `json:"id"`
	RunID     string `json:"run_id"`
	Timestamp string `json:"timestamp"`
	Agent     string `json:"agent"`
	Type      string `json:"type"`
	Step      string `json:"step,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Payload   string `json:"payload,omitempty"`
}

// TaskStat is a single row of the GROUP BY status count.
type TaskStat struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

// GetActiveRuns returns runs whose status is 'running' or 'pending', newest
// first (by started_at).
func (d *DB) GetActiveRuns() ([]RunRow, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.db.Query(`SELECT id,display_id,task_id,project_id,team_id,status,branch,worktree,agent,step,errors,summary,started_at,ended_at,tool_use_count
		FROM runs WHERE status IN ('running','pending') ORDER BY started_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunRow
	for rows.Next() {
		var r RunRow
		if err := rows.Scan(&r.ID, &r.DisplayID, &r.TaskID, &r.ProjectID, &r.TeamID, &r.Status,
			&r.Branch, &r.Worktree, &r.Agent, &r.Step, &r.Errors, &r.Summary, &r.StartedAt, &r.EndedAt, &r.ToolUseCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetAllRuns returns every run row, newest first.
func (d *DB) GetAllRuns() ([]RunRow, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.db.Query(`SELECT id,display_id,task_id,project_id,team_id,status,branch,worktree,agent,step,errors,summary,started_at,ended_at
		FROM runs ORDER BY started_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunRow
	for rows.Next() {
		var r RunRow
		if err := rows.Scan(&r.ID, &r.DisplayID, &r.TaskID, &r.ProjectID, &r.TeamID, &r.Status,
			&r.Branch, &r.Worktree, &r.Agent, &r.Step, &r.Errors, &r.Summary, &r.StartedAt, &r.EndedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRun returns a single run row by id.
func (d *DB) GetRun(id string) (*RunRow, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var r RunRow
	err := d.db.QueryRow(`SELECT id,display_id,task_id,project_id,team_id,status,branch,worktree,agent,step,errors,summary,started_at,ended_at
		FROM runs WHERE id = ?`, id).
		Scan(&r.ID, &r.DisplayID, &r.TaskID, &r.ProjectID, &r.TeamID, &r.Status,
			&r.Branch, &r.Worktree, &r.Agent, &r.Step, &r.Errors, &r.Summary, &r.StartedAt, &r.EndedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// GetRunEvents returns the most recent `limit` events for a run, newest first.
// If limit <= 0 a default of 100 is used.
func (d *DB) GetRunEvents(runID string, limit int) ([]EventRow, error) {
	if limit <= 0 {
		limit = 100
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.db.Query(`SELECT id,run_id,timestamp,agent,type,step,tool,payload
		FROM run_events WHERE run_id = ? ORDER BY id DESC LIMIT ?`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRow
	for rows.Next() {
		var e EventRow
		if err := rows.Scan(&e.ID, &e.RunID, &e.Timestamp, &e.Agent, &e.Type, &e.Step, &e.Tool, &e.Payload); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetTaskStats returns task counts grouped by status.
func (d *DB) GetTaskStats() ([]TaskStat, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.db.Query(`SELECT status, COUNT(*) FROM tasks GROUP BY status ORDER BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskStat
	for rows.Next() {
		var s TaskStat
		if err := rows.Scan(&s.Status, &s.Count); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UpdateRun upserts a single run row. Used by the watcher when a run.json changes.
func (d *DB) UpdateRun(r *run.Run) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`INSERT INTO runs
		(id,display_id,task_id,project_id,team_id,status,branch,worktree,agent,step,errors,summary,started_at,ended_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			display_id=excluded.display_id, task_id=excluded.task_id, project_id=excluded.project_id,
			team_id=excluded.team_id, status=excluded.status, branch=excluded.branch, worktree=excluded.worktree,
			agent=excluded.agent, step=excluded.step, errors=excluded.errors, summary=excluded.summary,
			started_at=excluded.started_at, ended_at=excluded.ended_at, updated_at=CURRENT_TIMESTAMP`,
		r.ID, r.DisplayID, r.TaskID, r.ProjectID, r.TeamID, r.Status, r.Branch, r.Worktree,
		r.Agent, r.Step, r.Errors, r.Summary, r.StartedAt, r.EndedAt)
	return err
}

// AddRunEvent inserts a single run_event row.
func (d *DB) AddRunEvent(ev *run.Event) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`INSERT INTO run_events (run_id,timestamp,agent,type,step,tool,payload)
		VALUES (?,?,?,?,?,?,?)`,
		ev.RunID, ev.Timestamp, ev.Agent, ev.Type, ev.Step, ev.Tool, ev.Payload)
	return err
}

// CountRunToolUse returns the number of events with type='tool_call' for a run.
func (d *DB) CountRunToolUse(runID string) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM run_events WHERE run_id = ? AND type = 'tool_call'`, runID).Scan(&n)
	return n, err
}

// UpdateToolUseCount updates the tool_use_count column for a run.
func (d *DB) UpdateToolUseCount(runID string, count int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`UPDATE runs SET tool_use_count = ? WHERE id = ?`, count, runID)
	return err
}

// ---------------------------------------------------------------------------
// Sync helpers for single-file updates (used by the fsnotify watcher).
// ---------------------------------------------------------------------------

// SyncProjectFile reads a single projects/*.json file and upserts it.
func (d *DB) SyncProjectFile(ds *DashboardStore, name string) error {
	id := strings.TrimSuffix(name, ".json")
	p, err := ds.GetProject(id)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err = d.db.Exec(`INSERT INTO projects
		(id,title,repo_path,docs_dir,docs,default_team,rules,test_command,lint_command,base_commit,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			title=excluded.title, repo_path=excluded.repo_path, docs_dir=excluded.docs_dir,
			docs=excluded.docs, default_team=excluded.default_team, rules=excluded.rules,
			test_command=excluded.test_command, lint_command=excluded.lint_command,
			base_commit=excluded.base_commit, created_at=excluded.created_at, updated_at=CURRENT_TIMESTAMP`,
		p.ID, p.Title, p.RepoPath, p.DocsDir, jsonString(p.Docs), p.DefaultTeam,
		jsonString(p.Rules), p.TestCommand, p.LintCommand, p.BaseCommit, p.CreatedAt)
	return err
}

// SyncEpicFile reads a single epics/*.json file and upserts it.
func (d *DB) SyncEpicFile(ds *DashboardStore, name string) error {
	id := strings.TrimSuffix(name, ".json")
	e, err := epic.Get(ds.Store, id)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err = d.db.Exec(`INSERT INTO epics
		(id,display_id,title,description,project_id,team_id,status,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			display_id=excluded.display_id, title=excluded.title, description=excluded.description,
			project_id=excluded.project_id, team_id=excluded.team_id, status=excluded.status,
			created_at=excluded.created_at, updated_at=CURRENT_TIMESTAMP`,
		e.ID, e.DisplayID, e.Title, e.Description, e.ProjectID, e.TeamID, e.Status, e.CreatedAt)
	return err
}

// SyncTaskFile reads a single tasks/*.json file and upserts it.
func (d *DB) SyncTaskFile(ds *DashboardStore, name string) error {
	id := strings.TrimSuffix(name, ".json")
	t, err := ds.GetTask(id)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err = d.db.Exec(`INSERT INTO tasks
		(id,display_id,title,description,type,status,project_id,epic_id,team_id,parent_id,
		specialization,assigned_profile,assigned_agent_name,verdict,verdict_reason,"group",redo_index,
		created_at,started_at,ended_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			display_id=excluded.display_id, title=excluded.title, description=excluded.description,
			type=excluded.type, status=excluded.status, project_id=excluded.project_id, epic_id=excluded.epic_id,
			team_id=excluded.team_id, parent_id=excluded.parent_id, specialization=excluded.specialization,
			assigned_profile=excluded.assigned_profile, assigned_agent_name=excluded.assigned_agent_name,
			verdict=excluded.verdict, verdict_reason=excluded.verdict_reason, "group"=excluded."group",
			redo_index=excluded.redo_index, created_at=excluded.created_at, started_at=excluded.started_at,
			ended_at=excluded.ended_at, updated_at=CURRENT_TIMESTAMP`,
		t.ID, t.DisplayID, t.Title, t.Description, string(t.Type), string(t.Status),
		t.ProjectID, t.EpicID, t.TeamID, t.ParentID, t.Specialization, t.AssignedProfile,
		t.AssignedAgentName, t.Verdict, t.VerdictReason, t.Group, t.RedoIndex,
		t.CreatedAt, t.StartedAt, t.EndedAt)
	return err
}

// SyncRunFile reads a single runs/run_*/run.json file, upserts the run row and
// reloads its events.jsonl into run_events.
func (d *DB) SyncRunFile(ds *DashboardStore, runID string) error {
	r, err := ds.GetRun(runID)
	if err != nil {
		return err
	}
	if err := d.UpdateRun(r); err != nil {
		return err
	}
	// Reload events for this run.
	events, err := ds.RunLogs(runID)
	if err != nil {
		return nil // events.jsonl may not exist yet
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.db.Exec("DELETE FROM run_events WHERE run_id = ?", runID); err != nil {
		return err
	}
	for _, ev := range events {
		if _, err := d.db.Exec(`INSERT INTO run_events (run_id,timestamp,agent,type,step,tool,payload)
			VALUES (?,?,?,?,?,?,?)`,
			ev.RunID, ev.Timestamp, ev.Agent, ev.Type, ev.Step, ev.Tool, ev.Payload); err != nil {
			return err
		}
	}
	return nil
}

// SyncTeamFile reads a single teams/*.json file and upserts the team + agents.
func (d *DB) SyncTeamFile(ds *DashboardStore, name string) error {
	id := strings.TrimSuffix(name, ".json")
	te, err := ds.GetTeam(id)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	workflow, _ := json.Marshal(te.Workflow)
	if _, err := d.db.Exec(`INSERT INTO teams
		(id,name,workflow,created_at,updated_at)
		VALUES (?,?,?,?,CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, workflow=excluded.workflow, created_at=excluded.created_at,
			updated_at=CURRENT_TIMESTAMP`,
		te.ID, te.Name, string(workflow), te.CreatedAt); err != nil {
		return err
	}
	// Replace agents for this team.
	if _, err := d.db.Exec("DELETE FROM agents WHERE team_id = ?", te.ID); err != nil {
		return err
	}
	// Sort agent names for deterministic order.
	names := make([]string, 0, len(te.Agents))
	for n := range te.Agents {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		ref := te.Agents[name]
		agentID := te.ID + "/" + name
		if _, err := d.db.Exec(`INSERT INTO agents
			(id,team_id,name,profile,role,parent,clone_from,
			avatar00,avatar01,avatar02,avatar10,avatar11,avatar12,avatar20,avatar21,avatar22,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)`,
			agentID, te.ID, name, ref.Profile, ref.Role, ref.Parent, ref.CloneFrom,
			"", "", "", "", "", "", "", "", ""); err != nil {
			return err
		}
	}
	return nil
}

// jsonString marshals v to JSON, returning "[]" / "{}" on nil.
func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// Memory layers
// ---------------------------------------------------------------------------

// MemoryEntry is a single memory record for a node.
type MemoryEntry struct {
	ID        int64  `json:"id"`
	NodeType  string `json:"node_type"`
	NodeID    string `json:"node_id"`
	Layer     string `json:"layer"` // 'raw' | 'narrative' | 'policy'
	Content   string `json:"content"`
	Source    string `json:"source"`
	CreatedAt string `json:"created_at"`
}

// AddMemory inserts a memory entry.
func (d *DB) AddMemory(nodeType, nodeID, layer, content, source string) (MemoryEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if source == "" {
		source = "system"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := d.db.Exec(
		`INSERT INTO memory_entries (node_type, node_id, layer, content, source, created_at)
		 VALUES (?,?,?,?,?,?)`,
		nodeType, nodeID, layer, content, source, now)
	if err != nil {
		return MemoryEntry{}, err
	}
	id, _ := res.LastInsertId()
	return MemoryEntry{
		ID:        id,
		NodeType:  nodeType,
		NodeID:    nodeID,
		Layer:     layer,
		Content:   content,
		Source:    source,
		CreatedAt: now,
	}, nil
}

// GetMemory returns memory entries for a node, optionally filtered by layer.
// If layer is empty, returns all layers. Newest first.
func (d *DB) GetMemory(nodeType, nodeID, layer string, limit int) ([]MemoryEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := `SELECT id, node_type, node_id, layer, content, source, created_at
	      FROM memory_entries WHERE node_type = ? AND node_id = ?`
	args := []any{nodeType, nodeID}
	if layer != "" {
		q += " AND layer = ?"
		args = append(args, layer)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemoryEntry
	for rows.Next() {
		var e MemoryEntry
		if err := rows.Scan(&e.ID, &e.NodeType, &e.NodeID, &e.Layer, &e.Content, &e.Source, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// GetLatestNarrative returns the most recent narrative entry for a node,
// or empty string if none exists.
func (d *DB) GetLatestNarrative(nodeType, nodeID string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var content string
	err := d.db.QueryRow(
		`SELECT content FROM memory_entries
		 WHERE node_type = ? AND node_id = ? AND layer = 'narrative'
		 ORDER BY id DESC LIMIT 1`, nodeType, nodeID).Scan(&content)
	if err != nil {
		return "", nil // no narrative yet is not an error
	}
	return content, nil
}

// GetPolicy returns all policy entries for a node (decisions, constraints).
func (d *DB) GetPolicy(nodeType, nodeID string) ([]MemoryEntry, error) {
	return d.GetMemory(nodeType, nodeID, "policy", 100)
}