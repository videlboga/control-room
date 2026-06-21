package task

import (
	"errors"
	"os"
	"time"

	"github.com/google/uuid"
	"control-room/internal/store"
)

// Task is a user-defined task.
type Task struct {
	ID          string `json:"id" yaml:"id"`
	Title       string `json:"title" yaml:"title"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	ProjectID   string `json:"project_id" yaml:"project_id"`
	TeamID      string `json:"team_id" yaml:"team_id"`
	Priority    string `json:"priority" yaml:"priority"`
	Status      string `json:"status" yaml:"status"`
	CreatedAt   string `json:"created_at" yaml:"created_at"`
}

func Create(st *store.Store, t *Task) (*Task, error) {
	if t.Title == "" || t.ProjectID == "" || t.TeamID == "" {
		return nil, errors.New("title, project_id and team_id are required")
	}
	if t.ID == "" {
		t.ID = "task_" + uuid.New().String()[:8]
	}
	if t.Priority == "" {
		t.Priority = "normal"
	}
	if t.Status == "" {
		t.Status = "open"
	}
	if t.CreatedAt == "" {
		t.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return t, st.WriteJSON([]string{"tasks", t.ID + ".json"}, t)
}

func Get(st *store.Store, id string) (*Task, error) {
	var t Task
	err := st.ReadJSON([]string{"tasks", id + ".json"}, &t)
	return &t, err
}

func List(st *store.Store) ([]Task, error) {
	names, err := st.ListJSON([]string{"tasks"})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Task
	for _, n := range names {
		var t Task
		if err := st.ReadJSON([]string{"tasks", n}, &t); err == nil {
			out = append(out, t)
		}
	}
	return out, nil
}

// ListByProject returns tasks filtered by project. Empty projectID returns all tasks.
func ListByProject(st *store.Store, projectID string) ([]Task, error) {
	tasks, err := List(st)
	if err != nil {
		return nil, err
	}
	if projectID == "" {
		return tasks, nil
	}
	var out []Task
	for _, t := range tasks {
		if t.ProjectID == projectID {
			out = append(out, t)
		}
	}
	return out, nil
}

func Update(st *store.Store, t *Task) error {
	return st.WriteJSON([]string{"tasks", t.ID + ".json"}, t)
}
