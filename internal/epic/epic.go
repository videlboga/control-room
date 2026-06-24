package epic

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"control-room/internal/config"
	"control-room/internal/store"
)

// Epic is a top-level feature request. It is never executed directly; the
// orchestrator expands it into a tree of workflow tasks.
type Epic struct {
	ID          string `json:"id" yaml:"id"`
	DisplayID   string `json:"display_id,omitempty" yaml:"display_id,omitempty"`
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
	if e.DisplayID == "" {
		cfg, err := config.LoadOrCreate(st.Root)
		if err == nil {
			did, err := cfg.NextDisplayID("epic")
			if err != nil {
				return nil, err
			}
			e.DisplayID = did
		}
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
	if id != "" && !strings.HasPrefix(id, "epic_") {
		return Resolve(st, id)
	}
	var e Epic
	err := st.ReadJSON([]string{"epics", id + ".json"}, &e)
	if err != nil {
		return &e, err
	}
	ensureDisplayID(st, &e)
	return &e, nil
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
			ensureDisplayID(st, &e)
			out = append(out, e)
		}
	}
	return out, nil
}

func ensureDisplayID(st *store.Store, e *Epic) {
	if e.DisplayID == "" {
		e.DisplayID = st.DisplayIDFromInternal("epic", e.ID)
	}
}

// Resolve returns the epic identified by either its internal ID or DisplayID.
func Resolve(st *store.Store, id string) (*Epic, error) {
	if id == "" {
		return nil, errors.New("epic id is required")
	}
	if strings.HasPrefix(id, "epic_") {
		var e Epic
		if err := st.ReadJSON([]string{"epics", id + ".json"}, &e); err != nil {
			return &e, err
		}
		ensureDisplayID(st, &e)
		return &e, nil
	}
	all, err := List(st)
	if err != nil {
		return nil, err
	}
	for _, e := range all {
		if e.ID == id || e.DisplayID == id {
			return &e, nil
		}
	}
	return nil, fmt.Errorf("epic not found: %s", id)
}

func Update(st *store.Store, e *Epic) error {
	return st.WriteJSON([]string{"epics", e.ID + ".json"}, e)
}
