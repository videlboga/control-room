package api

// ControlServer is the HTTP API surface exposed by the control plane.
//
// Endpoints (planned):
//   POST /api/v1/runs            -> create and schedule a run
//   GET  /api/v1/runs            -> list runs
//   GET  /api/v1/runs/:id        -> run status
//   GET  /api/v1/runs/:id/logs   -> run logs
//   POST /api/v1/runs/:id/cancel -> cancel a run
//
//   POST /api/v1/nodes           -> register a worker node
//   GET  /api/v1/nodes           -> list nodes
//   GET  /api/v1/nodes/:id       -> node status
//   POST /api/v1/nodes/:id/heartbeat -> heartbeat from worker
//
//   POST /api/v1/nodes/:id/runs/:run_id/events -> worker pushes events
//
// For the MVP the API is not fully wired; the CLI talks to the local store.
// The interface below documents the contract for the worker implementation.

type ControlServer struct {
	// Addr string
	// TODO: add store, scheduler, dispatcher dependencies
}

// RunRequest is accepted by POST /api/v1/runs.
type RunRequest struct {
	TaskID string `json:"task_id"`
	NodeID string `json:"node_id,omitempty"`
}

// RunResponse is returned by POST /api/v1/runs.
type RunResponse struct {
	RunID  string `json:"run_id"`
	NodeID string `json:"node_id"`
	Status string `json:"status"`
}

// NodeRegisterRequest is accepted by POST /api/v1/nodes.
type NodeRegisterRequest struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Host          string   `json:"host"`
	User          string   `json:"user"`
	Workspace     string   `json:"workspace"`
	MaxConcurrent int      `json:"max_concurrent"`
	SSHKey        string   `json:"ssh_key,omitempty"`
	HermesUser    string   `json:"hermes_user"`
	HermesSource  string   `json:"hermes_source"`
	Capabilities  []string `json:"capabilities,omitempty"`
}

// HeartbeatRequest is accepted by POST /api/v1/nodes/:id/heartbeat.
type HeartbeatRequest struct {
	RunningRuns    int     `json:"running_runs"`
	AvailableSlots int     `json:"available_slots"`
	TotalSlots     int     `json:"total_slots"`
	MemoryMiB      int     `json:"memory_mib"`
	LoadAvg1       float64 `json:"load_avg_1"`
}

// EventsPushRequest is accepted by POST /api/v1/nodes/:id/runs/:run_id/events.
type EventsPushRequest struct {
	Events []Event `json:"events"`
}

// Event mirrors the run.Event type used in logs.
type Event struct {
	Timestamp string `json:"timestamp"`
	RunID     string `json:"run_id"`
	Agent     string `json:"agent"`
	Type      string `json:"type"`
	Step      string `json:"step,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Payload   string `json:"payload,omitempty"`
}
