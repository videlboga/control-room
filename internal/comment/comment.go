package comment

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"control-room/internal/epic"
	"control-room/internal/store"
	"control-room/internal/task"
)

// Comment is a timeline entry attached to an epic or task.
type Comment struct {
	ID         string `json:"id"`
	EntityKind string `json:"entity_kind"`
	EntityID   string `json:"entity_id"`
	Author     string `json:"author"`
	Body       string `json:"body"`
	CreatedAt  string `json:"created_at"`
}

// resolveEntityID converts a DisplayID to an internal ID for tasks and epics.
func resolveEntityID(st *store.Store, kind, id string) (string, error) {
	if id == "" {
		return "", errors.New("entity id is required")
	}
	if strings.HasPrefix(id, kind+"_") || strings.HasPrefix(id, "comment_") {
		return id, nil
	}
	switch kind {
	case "task":
		all, err := task.List(st)
		if err != nil {
			return "", err
		}
		for _, t := range all {
			if t.ID == id || t.DisplayID == id {
				return t.ID, nil
			}
		}
	case "epic":
		all, err := epic.List(st)
		if err != nil {
			return "", err
		}
		for _, e := range all {
			if e.ID == id || e.DisplayID == id {
				return e.ID, nil
			}
		}
	}
	return "", fmt.Errorf("%s not found: %s", kind, id)
}

// List returns all comments for a given entity, oldest first.
func List(st *store.Store, kind, id string) ([]Comment, error) {
	id, err := resolveEntityID(st, kind, id)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(st.Root, "comments", kind, id+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []Comment
	dec := json.NewDecoder(f)
	for dec.More() {
		var c Comment
		if err := dec.Decode(&c); err != nil {
			break
		}
		out = append(out, c)
	}
	return out, nil
}

// Add appends a new comment to an entity timeline.
func Add(st *store.Store, kind, id, author, body string) (*Comment, error) {
	id, err := resolveEntityID(st, kind, id)
	if err != nil {
		return nil, err
	}
	if id == "" || body == "" {
		return nil, errors.New("entity id and body are required")
	}
	if author == "" {
		author = "system"
	}
	c := Comment{
		ID:         "comment_" + uuid.New().String()[:8],
		EntityKind: kind,
		EntityID:   id,
		Author:     author,
		Body:       body,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	dir := filepath.Join(st.Root, "comments", kind)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, id+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, _ := json.Marshal(c)
	if _, err := f.Write(append(b, "\n"...)); err != nil {
		return nil, err
	}
	return &c, nil
}
