package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	_ "github.com/lib/pq"
	"gopkg.in/yaml.v3"
)

// Backend abstracts persistence so the dashboard and orchestrator can run on
// either JSON files or Postgres without changing callers.
type Backend interface {
	Root() string
	EnsureDir(parts ...string) error
	WriteJSON(parts []string, v any) error
	ReadJSON(parts []string, v any) error
	WriteYAML(parts []string, v any) error
	ReadYAML(parts []string, v any) error
	AppendJSONL(parts []string, v any) error
	ListJSON(parts []string) ([]string, error)
	AgentLog(runID string, n int) ([]string, error)
}

// JSONBackend is the default filesystem implementation.
type JSONBackend struct {
	root string
}

func NewJSONBackend(root string) *JSONBackend {
	return &JSONBackend{root: root}
}

func (j *JSONBackend) Root() string { return j.root }

func (j *JSONBackend) dir(parts ...string) string {
	return filepath.Join(append([]string{j.root}, parts...)...)
}

func (j *JSONBackend) EnsureDir(parts ...string) error {
	return os.MkdirAll(j.dir(parts...), 0o755)
}

func (j *JSONBackend) WriteJSON(parts []string, v any) error {
	path := j.dir(parts...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (j *JSONBackend) ReadJSON(parts []string, v any) error {
	path := j.dir(parts...)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func (j *JSONBackend) WriteYAML(parts []string, v any) error {
	path := j.dir(parts...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (j *JSONBackend) ReadYAML(parts []string, v any) error {
	path := j.dir(parts...)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, v)
}

func (j *JSONBackend) AppendJSONL(parts []string, v any) error {
	path := j.dir(parts...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, string(data))
	return err
}

func (j *JSONBackend) ListJSON(parts []string) ([]string, error) {
	entries, err := os.ReadDir(j.dir(parts...))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func (j *JSONBackend) AgentLog(runID string, n int) ([]string, error) {
	logPath := filepath.Join(j.root, "runs", runID, "agent.log")
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	parts := splitLines(string(data))
	if len(parts) > n {
		return parts[len(parts)-n:], nil
	}
	return parts, nil
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// PostgresBackend stores all objects in a single Postgres database.
// Agent logs and JSONL streams remain on disk because they can be large.
type PostgresBackend struct {
	root string
	db   *sql.DB
}

// NewPostgresBackend opens a Postgres-backed store. The root directory is still
// used for workspace metadata, agent logs, and comments JSONL streams.
func NewPostgresBackend(root, dsn string) (*PostgresBackend, error) {
	if dsn == "" {
		return nil, fmt.Errorf("postgres dsn is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	p := &PostgresBackend{root: root, db: db}
	if err := p.migrate(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *PostgresBackend) Root() string { return p.root }

func (p *PostgresBackend) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS store_objects (
    kind TEXT NOT NULL,
    id   TEXT NOT NULL,
    data JSONB NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (kind, id)
);
CREATE INDEX IF NOT EXISTS idx_store_objects_kind ON store_objects(kind);
`
	_, err := p.db.Exec(schema)
	return err
}

func (p *PostgresBackend) EnsureDir(parts ...string) error {
	return os.MkdirAll(filepath.Join(append([]string{p.root}, parts...)...), 0o755)
}

func (p *PostgresBackend) WriteJSON(parts []string, v any) error {
	kind, id := objectKindID(parts)
	if kind == "" || id == "" {
		return fmt.Errorf("unsupported postgres path: %v", parts)
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = p.db.Exec(`
		INSERT INTO store_objects(kind, id, data) VALUES ($1, $2, $3)
		ON CONFLICT (kind, id) DO UPDATE SET data = EXCLUDED.data, updated_at = CURRENT_TIMESTAMP`,
		kind, id, string(data))
	return err
}

func (p *PostgresBackend) ReadJSON(parts []string, v any) error {
	kind, id := objectKindID(parts)
	if kind == "" || id == "" {
		return fmt.Errorf("unsupported postgres path: %v", parts)
	}
	var data string
	if err := p.db.QueryRow(`SELECT data FROM store_objects WHERE kind = $1 AND id = $2`, kind, id).Scan(&data); err != nil {
		if err == sql.ErrNoRows {
			return os.ErrNotExist
		}
		return err
	}
	return json.Unmarshal([]byte(data), v)
}

func (p *PostgresBackend) WriteYAML(parts []string, v any) error {
	return fmt.Errorf("WriteYAML not supported on postgres backend")
}

func (p *PostgresBackend) ReadYAML(parts []string, v any) error {
	return fmt.Errorf("ReadYAML not supported on postgres backend")
}

func (p *PostgresBackend) AppendJSONL(parts []string, v any) error {
	// JSONL streams (comments, run events) stay on disk even with Postgres.
	b := NewJSONBackend(p.root)
	return b.AppendJSONL(parts, v)
}

func (p *PostgresBackend) ListJSON(parts []string) ([]string, error) {
	kind := objectKind(parts)
	if kind == "" {
		return nil, fmt.Errorf("unsupported postgres path: %v", parts)
	}
	rows, err := p.db.Query(`SELECT id FROM store_objects WHERE kind = $1`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		out = append(out, id+".json")
	}
	return out, rows.Err()
}

func (p *PostgresBackend) AgentLog(runID string, n int) ([]string, error) {
	return NewJSONBackend(p.root).AgentLog(runID, n)
}

// objectKindID extracts "kind" and "id" from parts like ["tasks", "task_abc.json"].
func objectKindID(parts []string) (string, string) {
	if len(parts) != 2 {
		return "", ""
	}
	kind := parts[0]
	if len(kind) > 0 && kind[len(kind)-1] == 's' {
		kind = kind[:len(kind)-1]
	}
	id := parts[1]
	if filepath.Ext(id) == ".json" {
		id = id[:len(id)-5]
	}
	return kind, id
}

// objectKind extracts "kind" from parts like ["tasks"].
func objectKind(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	kind := parts[len(parts)-1]
	if len(kind) > 0 && kind[len(kind)-1] == 's' {
		kind = kind[:len(kind)-1]
	}
	return kind
}

// jsonlKindParent extracts kind/parent from parts like ["comments", "tasks", "task_abc.jsonl"].
func jsonlKindParent(parts []string) (string, string) {
	if len(parts) < 2 {
		return "", ""
	}
	kind := parts[len(parts)-2]
	if len(kind) > 0 && kind[len(kind)-1] == 's' {
		kind = kind[:len(kind)-1]
	}
	parentID := parts[len(parts)-1]
	if ext := filepath.Ext(parentID); ext != "" {
		parentID = parentID[:len(parentID)-len(ext)]
	}
	return kind, parentID
}
