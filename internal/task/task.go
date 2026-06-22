package task

import (
	"errors"
	"os"
	"time"

	"github.com/google/uuid"
	"control-room/internal/store"
)

// TaskType enumerates the workflow steps that the orchestrator understands.
// The type determines the agent role and the allowed next states.
type TaskType string

const (
	TypeResearch       TaskType = "research"
	TypeQAReview       TaskType = "qa_review"
	TypePMPlan         TaskType = "pm_plan"
	TypeEngineering    TaskType = "engineering"
	TypeQAVerify       TaskType = "qa_verify"
	TypePMConsistency  TaskType = "pm_consistency"
)

// Valid task types.
var TaskTypes = []TaskType{TypeResearch, TypeQAReview, TypePMPlan, TypeEngineering, TypeQAVerify, TypePMConsistency}

// TaskStatus is the lifecycle of a single task.
type TaskStatus string

const (
	StatusOpen           TaskStatus = "open"
	StatusInProgress     TaskStatus = "in_progress"
	StatusPendingReview  TaskStatus = "pending_review"
	StatusApproved       TaskStatus = "approved"
	StatusRejected       TaskStatus = "rejected"
	StatusDone           TaskStatus = "done"
)

// Task is a concrete workflow step.
type Task struct {
	ID                string     `json:"id" yaml:"id"`
	Title             string     `json:"title" yaml:"title"`
	Description       string     `json:"description,omitempty" yaml:"description,omitempty"`
	Type              TaskType   `json:"type" yaml:"type"`
	Status            TaskStatus `json:"status" yaml:"status"`
	ProjectID         string     `json:"project_id" yaml:"project_id"`
	EpicID            string     `json:"epic_id,omitempty" yaml:"epic_id,omitempty"`
	TeamID            string     `json:"team_id,omitempty" yaml:"team_id,omitempty"`
	ParentID          string     `json:"parent_id,omitempty" yaml:"parent_id,omitempty"`
	Dependencies      []string   `json:"dependencies,omitempty" yaml:"dependencies,omitempty"`
	Specialization    string     `json:"specialization,omitempty" yaml:"specialization,omitempty"`
	AssignedProfile   string     `json:"assigned_profile,omitempty" yaml:"assigned_profile,omitempty"`
	AssignedAgentName string     `json:"assigned_agent_name,omitempty" yaml:"assigned_agent_name,omitempty"`
	Verdict           string     `json:"verdict,omitempty" yaml:"verdict,omitempty"`
	VerdictReason     string     `json:"verdict_reason,omitempty" yaml:"verdict_reason,omitempty"`
	Group             string     `json:"group,omitempty" yaml:"group,omitempty"`
	RedoIndex         int        `json:"redo_index" yaml:"redo_index"`
	CreatedAt         string     `json:"created_at" yaml:"created_at"`
	StartedAt         string     `json:"started_at,omitempty" yaml:"started_at,omitempty"`
	EndedAt           string     `json:"ended_at,omitempty" yaml:"ended_at,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

func IsValidTaskType(t string) bool {
	for _, tt := range TaskTypes {
		if string(tt) == t {
			return true
		}
	}
	return false
}

func Create(st *store.Store, t *Task) (*Task, error) {
	if t.Title == "" || t.ProjectID == "" {
		return nil, errors.New("title and project_id are required")
	}
	if t.Type == "" {
		return nil, errors.New("task type is required")
	}
	if !IsValidTaskType(string(t.Type)) {
		return nil, errors.New("unknown task type: " + string(t.Type))
	}
	if t.ID == "" {
		t.ID = "task_" + uuid.New().String()[:8]
	}
	if t.Status == "" {
		t.Status = StatusOpen
	}
	if t.CreatedAt == "" {
		t.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if t.Group == "" {
		t.Group = t.ID
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

// ListByEpic returns tasks belonging to an epic.
func ListByEpic(st *store.Store, epicID string) ([]Task, error) {
	tasks, err := List(st)
	if err != nil {
		return nil, err
	}
	var out []Task
	for _, t := range tasks {
		if t.EpicID == epicID {
			out = append(out, t)
		}
	}
	return out, nil
}

func Update(st *store.Store, t *Task) error {
	return st.WriteJSON([]string{"tasks", t.ID + ".json"}, t)
}

// Redo creates a new task in the same group, advancing redo_index.
func Redo(st *store.Store, base *Task, reason string) (*Task, error) {
	maxIdx := base.RedoIndex
	all, err := List(st)
	if err != nil {
		return nil, err
	}
	for _, tt := range all {
		if tt.Group == base.Group && tt.RedoIndex > maxIdx {
			maxIdx = tt.RedoIndex
		}
	}
	next := *base
	next.ID = ""
	next.Status = StatusOpen
	next.Verdict = ""
	next.VerdictReason = reason
	next.RedoIndex = maxIdx + 1
	next.ParentID = base.ID
	next.Metadata = map[string]string{"rejected_task": base.ID}
	if next.Dependencies != nil {
		next.Dependencies = append([]string(nil), next.Dependencies...)
	}
	return Create(st, &next)
}
