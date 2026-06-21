package control

import (
	"control-room/internal/node"
	"control-room/internal/run"
	"control-room/internal/store"
)

// Scheduler picks a worker for a run and dispatches it.
//
// For the MVP the scheduler is simple:
//   1. Collect nodes whose last heartbeat is recent (status == "online").
//   2. Pick the node with the most available slots (round-robin tiebreak by ID).
//   3. If no worker is available, fall back to local execution on control-plane
//      subject to its own MaxConcurrentRuns limit.
//
// Future extensions:
//   - per-project node pinning
//   - GPU/CPU capability labels
//   - queue with priority and deadlines
//   - auto-scaling workers

type Scheduler struct {
	Store      *store.Store
	Dispatcher node.AgentRemoteClient
}

// Schedule creates a run record, chooses a node, and dispatches it.
// It returns the run and the selected node ID ("local" for local execution).
func (s *Scheduler) Schedule(taskID string) (*run.Run, string, error) {
	// TODO: implement node selection and dispatch.
	// For now the local fallback path is wired through run.Start.
	return nil, "local", nil
}

// Reconcile is a background loop that checks remote runs whose status is
// "running" and pulls updates from workers. It also retries failed dispatches.
//
// TODO: implement polling-based status sync.
func (s *Scheduler) Reconcile() error {
	return nil
}
