package node

// Node represents a worker node registered with the control plane.
type Node struct {
	ID              string `json:"id" yaml:"id"`
	Name            string `json:"name" yaml:"name"`
	Host            string `json:"host" yaml:"host"`
	User            string `json:"user" yaml:"user"`
	Workspace       string `json:"workspace" yaml:"workspace"`
	MaxConcurrent   int    `json:"max_concurrent" yaml:"max_concurrent"`
	SSHKey          string `json:"ssh_key,omitempty" yaml:"ssh_key,omitempty"`
	HermesUser      string `json:"hermes_user" yaml:"hermes_user"`
	HermesSource    string `json:"hermes_source" yaml:"hermes_source"`
	Status          string `json:"status" yaml:"status"`
	LastHeartbeatAt string `json:"last_heartbeat_at,omitempty" yaml:"last_heartbeat_at,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
}

// NodeStore is an interface for registering and listing nodes.
// For the MVP this is backed by the same filesystem store as other metadata;
// later it may move to a database.
type NodeStore interface {
	Save(n *Node) error
	Get(id string) (*Node, error)
	List() ([]Node, error)
	Delete(id string) error
}

// NodeHealth describes the current capacity and status reported by a worker.
type NodeHealth struct {
	NodeID           string `json:"node_id"`
	Status           string `json:"status"`
	RunningRuns      int    `json:"running_runs"`
	AvailableSlots   int    `json:"available_slots"`
	TotalSlots       int    `json:"total_slots"`
	MemoryMiB        int    `json:"memory_mib"`
	LoadAvg1         float64 `json:"load_avg_1"`
	Timestamp        string `json:"timestamp"`
}

// AgentRemoteClient is the interface the control plane uses to dispatch work.
// Initial implementation is SSH-based: the control plane runs cr-worker commands
// on the remote node over SSH. Later versions can switch to HTTP/gRPC.
type AgentRemoteClient interface {
	StartRun(req DispatchRequest) (string, error)
	CancelRun(runID string) error
	GetRunStatus(runID string) (*RunStatus, error)
	StreamLogs(runID string, follow bool) ([]Event, error)
}

// DispatchRequest is sent to a worker to start a run.
type DispatchRequest struct {
	RunID       string `json:"run_id"`
	TaskID      string `json:"task_id"`
	ProjectID   string `json:"project_id"`
	TeamID      string `json:"team_id"`
	TaskTitle   string `json:"task_title"`
	TaskDescription string `json:"task_description,omitempty"`
	RepoPath    string `json:"repo_path,omitempty"`
	Docs        map[string]string `json:"docs,omitempty"`
	TeamWorkflow []string `json:"team_workflow,omitempty"`
	MaxConcurrent int `json:"max_concurrent,omitempty"`
}

// RunStatus is returned by a worker for a dispatched run.
type RunStatus struct {
	RunID     string `json:"run_id"`
	Status    string `json:"status"`
	Step      string `json:"step,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Worktree  string `json:"worktree,omitempty"`
	Errors    int    `json:"errors"`
	StartedAt string `json:"started_at,omitempty"`
	EndedAt   string `json:"ended_at,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

// Event mirrors the run.Event type used by worker logs.
type Event struct {
	Timestamp string `json:"timestamp"`
	RunID     string `json:"run_id"`
	Agent     string `json:"agent"`
	Type      string `json:"type"`
	Step      string `json:"step,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Payload   string `json:"payload,omitempty"`
}
