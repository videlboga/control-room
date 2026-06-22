package epic

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"control-room/internal/store"
)

// Epic is a top-level feature request. It is never executed directly; the
// orchestrator expands it into a tree of workflow tasks.
type Epic struct {
	ID          string `json:"id" yaml:"id"`
	Title       string `json:"title" yaml:"title"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	ProjectID   string `json:"project_id" yaml:"project_id"`
	TeamID      string `json:"team_id,omitempty" yaml:"team_id,omitempty"`
	Status      string `json:"status" yaml:"status"`
	CreatedAt   string `json:"created_at" yaml:"created_at"`
}

func Create(st *store.Store, e *Epic) (*Epic, error) {
	if e.Title == "" || e.ProjectID == "" {
		return nil, errors.New("title and project_id are required")
	}
	if e.ID == "" {
		e.ID = "epic_" + uuid.New().String()[:8]
	}
	if e.Status == "" {
		e.Status = "open"
	}
	if e.CreatedAt == "" {
		e.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return e, st.WriteJSON([]string{"epics", e.ID + ".json"}, e)
}

func Get(st *store.Store, id string) (*Epic, error) {
	var e Epic
	err := st.ReadJSON([]string{"epics", id + ".json"}, &e)
	return &e, err
}

func List(st *store.Store) ([]Epic, error) {
	names, err := st.ListJSON([]string{"epics"})
	if err != nil {
		return nil, err
	}
	var out []Epic
	for _, n := range names {
		var e Epic
		if err := st.ReadJSON([]string{"epics", n}, &e); err == nil {
			out = append(out, e)
		}
	}
	return out, nil
}

func Update(st *store.Store, e *Epic) error {
	return st.WriteJSON([]string{"epics", e.ID + ".json"}, e)
}
