package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"control-room/internal/epic"
	"control-room/internal/gate"
	"control-room/internal/project"
	"control-room/internal/run"
	"control-room/internal/store"
	"control-room/internal/task"
)

// Orchestrator drives a deterministic workflow state machine for an Epic.
// It is intentionally small and hard to fool: transitions are hard-coded,
// LLM agents only generate content inside each step.
type Orchestrator struct {
	Store         *store.Store
	ManualApprove bool
	// Prompt is called when ManualApprove is true and a QA verify task finishes.
	// It should return "approve" or "reject" and an optional reason.
	Prompt func(taskID string) (verdict string, reason string)
}

// Plan is the decomposition produced by the PM agent.
type Plan struct {
	Tasks []PlanTask `json:"tasks"`
}

type PlanTask struct {
	ID             string   `json:"id"`
	Type           string   `json:"type"`
	Specialization string   `json:"specialization"`
	Title          string   `json:"title"`
	Description    string   `json:"description,omitempty"`
	Dependencies   []string `json:"dependencies,omitempty"`
}

// transitions defines the allowed next states from a completed task.
var transitions = map[task.TaskType]map[string]task.TaskType{
	task.TypeResearch:      {"approved": task.TypeQAReview, "rejected": task.TypeResearch},
	task.TypeQAReview:      {"approved": task.TypePMPlan, "rejected": task.TypeResearch},
	task.TypePMPlan:        {"approved": task.TypeEngineering}, // expansion handled specially
	task.TypeEngineering:   {"approved": task.TypeQAVerify, "rejected": task.TypeEngineering},
	task.TypeQAVerify:      {"approved": task.TypePMConsistency, "rejected": task.TypeQAVerify},
	task.TypePMConsistency: {"approved": task.TypeEngineering, "rejected": task.TypeQAVerify},
}

// RunEpic expands an Epic into workflow tasks and drives them to completion.
func (o *Orchestrator) RunEpic(epicID string, cb func(string, ...interface{})) error {
	e, err := epic.Get(o.Store, epicID)
	if err != nil {
		return err
	}
	if e.Status == "done" {
		return errors.New("epic already done")
	}
	e.Status = "in_progress"
	_ = epic.Update(o.Store, e)

	proj, err := project.Get(o.Store, e.ProjectID)
	if err != nil {
		return err
	}
	if proj.DefaultTeam == "" {
		return errors.New("project has no default team for workflow tasks")
	}

	// Create initial research task if none exists.
	children, err := task.ListByEpic(o.Store, epicID)
	if err != nil {
		return err
	}
	var researchTask *task.Task
	for _, c := range children {
		if c.Type == task.TypeResearch {
			researchTask = &c
			break
		}
	}
	if researchTask == nil {
		researchTask, err = task.Create(o.Store, &task.Task{
			Title:       "Research: " + e.Title,
			Description: e.Description,
			Type:        task.TypeResearch,
			ProjectID:   e.ProjectID,
			EpicID:      e.ID,
			TeamID:      proj.DefaultTeam,
		})
		if err != nil {
			return err
		}
	}

	for {
		// 1. Pick a ready open task in this epic.
		ready, err := o.nextReadyTask(epicID)
		if err != nil {
			return err
		}
		if ready == nil {
			// Nothing open. Check if everything is done/approved.
			if o.epicFinished(epicID) {
				e.Status = "done"
				_ = epic.Update(o.Store, e)
				cb("epic_done", e.ID)
				return nil
			}
			// Deadlock or all remaining tasks pending review.
			cb("waiting_for_review", epicID)
			return errors.New("no ready tasks: remaining tasks are pending review or blocked")
		}

		cb("task_start", ready.ID, ready.Type)

		// 2. Run the task.
		r, err := run.Start(o.Store, ready.ID)
		if err != nil {
			return fmt.Errorf("failed to start run for task %s: %w", ready.ID, err)
		}

		// Wait for the run to finish. We tail events synchronously.
		var lastEvents []run.Event
		waitErr := run.WaitFor(o.Store, r.ID, func(ev run.Event) {
			lastEvents = append(lastEvents, ev)
			cb("event", ev)
		})
		if waitErr != nil {
			cb("wait_error", waitErr)
		}

		// 3. Read verdict from run metadata.
		verdict, reason, verdictErr := o.readVerdict(r.ID)
		if verdictErr != nil {
			cb("verdict_error", ready.ID, verdictErr)
			// Treat missing/invalid verdict as reject.
			verdict = "reject"
			reason = "no valid verdict in run metadata: " + verdictErr.Error()
		}

		// 3.5 Human-in-the-loop override for QA verify.
		if o.ManualApprove && ready.Type == task.TypeQAVerify && verdict == "approve" {
			if o.Prompt != nil {
				cb("awaiting_manual_approval", ready.ID)
				verdict, reason = o.Prompt(ready.ID)
				cb("manual_verdict", ready.ID, verdict, reason)
			}
		}

		ready.Status = task.StatusPendingReview
		ready.Verdict = verdict
		ready.VerdictReason = reason
		_ = task.Update(o.Store, ready)

		cb("task_verdict", ready.ID, verdict, reason)

		// 4. Hard gate checks override an LLM approve.
		if verdict == "approve" {
			proj, _ := project.Get(o.Store, e.ProjectID)
			g, err := gate.Run(o.Store, ready, r, proj)
			if err != nil {
				cb("gate_error", ready.ID, err)
				verdict = "reject"
				reason = "gate check error: " + err.Error()
			} else if !g.Passed {
				cb("gate_failed", ready.ID, g.Errors)
				verdict = "reject"
				reason = "gate checks failed: " + strings.Join(g.Errors, "; ")
			}
		}

		// 5. Apply deterministic transition.
		// 5. Apply deterministic transition.
		if verdict == "approve" {
			ready.Status = task.StatusApproved
			ready.EndedAt = time.Now().UTC().Format(time.RFC3339)
			_ = task.Update(o.Store, ready)

			if ready.Type == task.TypePMConsistency {
				e.Status = "done"
				_ = epic.Update(o.Store, e)
				cb("epic_done", e.ID)
				return nil
			}

			nextType := transitions[ready.Type]["approved"]
			if nextType == "" || nextType == task.TypeQAVerify {
				// Engineering approved: the orchestrator already created a single QA verify
				// task that depends on all engineering tasks. No new task is created here.
				cb("task_done", ready.ID)
				continue
			}
			if nextType == task.TypeEngineering {
				// PM plan expands to engineering tasks.
				if err := o.expandEngineering(ready); err != nil {
					return err
				}
			} else {
				if _, err := task.Create(o.Store, &task.Task{
					Title:       o.nextTitle(nextType, ready),
					Type:        nextType,
					ProjectID:   e.ProjectID,
					EpicID:      e.ID,
					ParentID:    ready.ID,
					TeamID:      proj.DefaultTeam,
					Description: ready.Description,
				}); err != nil {
					return err
				}
			}
		} else {
			// reject
			ready.Status = task.StatusRejected
			_ = task.Update(o.Store, ready)

			redoType := transitions[ready.Type]["rejected"]
			if redoType == "" {
				redoType = ready.Type
			}
			redo, err := task.Redo(o.Store, ready, reason)
			if err != nil {
				return err
			}
			redo.Type = redoType
			_ = task.Update(o.Store, redo)
			cb("redo_created", redo.ID, redoType, reason)
		}
	}
}

// WatchEpic runs a detached-style orchestration loop. It starts every ready task
// in parallel, waits for all of them, then applies transitions and repeats.
func (o *Orchestrator) WatchEpic(epicID string, cb func(string, ...interface{})) error {
	e, err := epic.Get(o.Store, epicID)
	if err != nil {
		return err
	}
	if e.Status == "done" {
		return errors.New("epic already done")
	}
	e.Status = "in_progress"
	_ = epic.Update(o.Store, e)

	proj, err := project.Get(o.Store, e.ProjectID)
	if err != nil {
		return err
	}
	if proj.DefaultTeam == "" {
		return errors.New("project has no default team for workflow tasks")
	}

	children, err := task.ListByEpic(o.Store, epicID)
	if err != nil {
		return err
	}
	var researchTask *task.Task
	for _, c := range children {
		if c.Type == task.TypeResearch {
			researchTask = &c
			break
		}
	}
	if researchTask == nil {
		researchTask, err = task.Create(o.Store, &task.Task{
			Title:       "Research: " + e.Title,
			Description: e.Description,
			Type:        task.TypeResearch,
			ProjectID:   e.ProjectID,
			EpicID:      e.ID,
			TeamID:      proj.DefaultTeam,
		})
		if err != nil {
			return err
		}
	}

	type taskResult struct {
		t       *task.Task
		r       *run.Run
		verdict string
		reason  string
	}

	for {
		ready, err := o.nextReadyTasks(epicID)
		if err != nil {
			return err
		}
		if len(ready) == 0 {
			if o.epicFinished(epicID) {
				e.Status = "done"
				_ = epic.Update(o.Store, e)
				cb("epic_done", e.ID)
				return nil
			}
			cb("waiting_for_review", epicID)
			return errors.New("no ready tasks: remaining tasks are pending review or blocked")
		}

		cb("batch_ready", len(ready))
		for _, t := range ready {
			cb("task_start", t.ID, t.Type)
		}

		var wg sync.WaitGroup
		mu := sync.Mutex{}
		results := make(map[string]*taskResult, len(ready))
		for i := range ready {
			t := &ready[i]
			r, err := run.Start(o.Store, t.ID)
			if err != nil {
				return fmt.Errorf("failed to start run for task %s: %w", t.ID, err)
			}
			wg.Add(1)
			go func(t *task.Task, r *run.Run) {
				defer wg.Done()
				_ = run.WaitFor(o.Store, r.ID, func(ev run.Event) {
					cb("event", ev)
				})
				verdict, reason, verdictErr := o.readVerdict(r.ID)
				if verdictErr != nil {
					cb("verdict_error", t.ID, verdictErr)
					verdict = "reject"
					reason = "no valid verdict in run metadata: " + verdictErr.Error()
				}
				if o.ManualApprove && t.Type == task.TypeQAVerify && verdict == "approve" {
					if o.Prompt != nil {
						cb("awaiting_manual_approval", t.ID)
						verdict, reason = o.Prompt(t.ID)
						cb("manual_verdict", t.ID, verdict, reason)
					}
				}
				if verdict == "approve" {
					proj, _ := project.Get(o.Store, e.ProjectID)
					g, err := gate.Run(o.Store, t, r, proj)
					if err != nil {
						cb("gate_error", t.ID, err)
						verdict = "reject"
						reason = "gate check error: " + err.Error()
					} else if !g.Passed {
						cb("gate_failed", t.ID, g.Errors)
						verdict = "reject"
						reason = "gate checks failed: " + strings.Join(g.Errors, "; ")
					}
				}
				mu.Lock()
				results[t.ID] = &taskResult{t: t, r: r, verdict: verdict, reason: reason}
				mu.Unlock()
			}(t, r)
		}
		wg.Wait()

		for _, res := range results {
			ready := res.t
			verdict := res.verdict
			reason := res.reason

			cb("task_verdict", ready.ID, verdict, reason)

			if verdict == "approve" {
				ready.Status = task.StatusApproved
				ready.EndedAt = time.Now().UTC().Format(time.RFC3339)
				_ = task.Update(o.Store, ready)

				if ready.Type == task.TypePMConsistency {
					e.Status = "done"
					_ = epic.Update(o.Store, e)
					cb("epic_done", e.ID)
					return nil
				}

				nextType := transitions[ready.Type]["approved"]
				if nextType == "" || nextType == task.TypeQAVerify {
					cb("task_done", ready.ID)
					continue
				}
				if nextType == task.TypeEngineering {
					if err := o.expandEngineering(ready); err != nil {
						return err
					}
				} else {
					if _, err := task.Create(o.Store, &task.Task{
						Title:       o.nextTitle(nextType, ready),
						Type:        nextType,
						ProjectID:   e.ProjectID,
						EpicID:      e.ID,
						ParentID:    ready.ID,
						TeamID:      proj.DefaultTeam,
						Description: ready.Description,
					}); err != nil {
						return err
					}
				}
			} else {
				ready.Status = task.StatusRejected
				_ = task.Update(o.Store, ready)

				redoType := transitions[ready.Type]["rejected"]
				if redoType == "" {
					redoType = ready.Type
				}
				redo, err := task.Redo(o.Store, ready, reason)
				if err != nil {
					return err
				}
				redo.Type = redoType
				_ = task.Update(o.Store, redo)
				cb("redo_created", redo.ID, redoType, reason)
			}
		}
	}
}

// nextReadyTasks returns all tasks that can start in parallel: one per group,
// with dependencies satisfied.
func (o *Orchestrator) nextReadyTasks(epicID string) ([]task.Task, error) {
	tasks, err := task.ListByEpic(o.Store, epicID)
	if err != nil {
		return nil, err
	}

	// group -> task with lowest redo_index that is open
	bestByGroup := make(map[string]task.Task)
	for _, t := range tasks {
		if t.Status != task.StatusOpen {
			continue
		}
		existing, ok := bestByGroup[t.Group]
		if !ok || t.RedoIndex < existing.RedoIndex {
			bestByGroup[t.Group] = t
		}
	}

	var ready []task.Task
	for _, t := range bestByGroup {
		depsOK := true
		for _, depID := range t.Dependencies {
			dep, err := task.Get(o.Store, depID)
			if err != nil || (dep.Status != task.StatusDone && dep.Status != task.StatusApproved) {
				depsOK = false
				break
			}
		}
		if depsOK {
			ready = append(ready, t)
		}
	}

	typeOrder := map[task.TaskType]int{
		task.TypeResearch:      0,
		task.TypeQAReview:      1,
		task.TypePMPlan:        2,
		task.TypeEngineering:   3,
		task.TypeQAVerify:      4,
		task.TypePMConsistency: 5,
	}
	sort.SliceStable(ready, func(i, j int) bool {
		if typeOrder[ready[i].Type] != typeOrder[ready[j].Type] {
			return typeOrder[ready[i].Type] < typeOrder[ready[j].Type]
		}
		return ready[i].CreatedAt < ready[j].CreatedAt
	})

	return ready, nil
}

// nextReadyTask returns the next task that can start.
func (o *Orchestrator) nextReadyTask(epicID string) (*task.Task, error) {
	ready, err := o.nextReadyTasks(epicID)
	if err != nil {
		return nil, err
	}
	if len(ready) == 0 {
		return nil, nil
	}
	return &ready[0], nil
}

// epicFinished returns true when there are no open/in_progress tasks and at
// least one pm_consistency task is approved.
func (o *Orchestrator) epicFinished(epicID string) bool {
	tasks, err := task.ListByEpic(o.Store, epicID)
	if err != nil {
		return false
	}
	var hasApprovedPMConsistency bool
	for _, t := range tasks {
		if t.Status == task.StatusOpen || t.Status == task.StatusInProgress {
			return false
		}
		if t.Type == task.TypePMConsistency && t.Status == task.StatusApproved {
			hasApprovedPMConsistency = true
		}
	}
	return hasApprovedPMConsistency
}

// readVerdict reads runs/<run-id>/metadata.json and returns verdict/reason.
func (o *Orchestrator) readVerdict(runID string) (string, string, error) {
	path := filepath.Join(o.Store.Root, "runs", runID, "metadata.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return "", "", err
	}
	verdict := m["verdict"]
	if verdict != "approve" && verdict != "reject" {
		return "", "", fmt.Errorf("invalid verdict %q", verdict)
	}
	return verdict, m["reason"], nil
}

// nextTitle gives a readable title for the next task in the chain.
func (o *Orchestrator) nextTitle(tt task.TaskType, prev *task.Task) string {
	switch tt {
	case task.TypeQAReview:
		return "QA review: " + prev.Title
	case task.TypePMPlan:
		return "PM plan: " + prev.Title
	case task.TypeEngineering:
		return "Engineering: " + prev.Title
	case task.TypeQAVerify:
		return "QA verify: " + prev.Title
	case task.TypePMConsistency:
		return "PM consistency: " + prev.Title
	default:
		return string(tt) + ": " + prev.Title
	}
}

// expandEngineering reads the PM plan output, validates it, and creates engineering tasks.
func (o *Orchestrator) expandEngineering(pmTask *task.Task) error {
	e, err := epic.Get(o.Store, pmTask.EpicID)
	if err != nil {
		return err
	}

	// Read the last run for this task and parse metadata "plan".
	runs, err := runsForTask(o.Store, pmTask.ID)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return errors.New("no runs found for PM plan task")
	}
	lastRun := runs[len(runs)-1]

	planData, err := readRunMetadata(o.Store, lastRun.ID, "plan")
	if err != nil {
		return err
	}
	var plan Plan
	if err := json.Unmarshal([]byte(planData), &plan); err != nil {
		return fmt.Errorf("invalid plan JSON: %w", err)
	}

	if err := validatePlan(&plan); err != nil {
		return err
	}

	createdIDs := make(map[string]string)
	for _, pt := range plan.Tasks {
		t := &task.Task{
			ID:             pt.ID,
			Title:          pt.Title,
			Description:    pt.Description,
			Type:           task.TypeEngineering,
			Specialization: pt.Specialization,
			ProjectID:      e.ProjectID,
			EpicID:         e.ID,
			ParentID:       pmTask.ID,
			TeamID:         pmTask.TeamID,
			Dependencies:   pt.Dependencies,
		}
		created, err := task.Create(o.Store, t)
		if err != nil {
			return err
		}
		createdIDs[pt.ID] = created.ID
	}

	// Rewrite dependencies from plan IDs to created IDs.
	created, err := task.ListByEpic(o.Store, e.ID)
	if err != nil {
		return err
	}
	for _, t := range created {
		if t.Type != task.TypeEngineering || t.ParentID != pmTask.ID {
			continue
		}
		var newDeps []string
		for _, d := range t.Dependencies {
			if mapped, ok := createdIDs[d]; ok {
				newDeps = append(newDeps, mapped)
			}
		}
		t.Dependencies = newDeps
		_ = task.Update(o.Store, &t)
	}

	// After all engineering tasks are defined, create a single QA verify task that
	// depends on every engineering task. This keeps the workflow deterministic and
	// avoids creating duplicate QA tasks when individual engineering tasks finish.
	var engIDs []string
	for _, t := range created {
		if t.Type == task.TypeEngineering && t.ParentID == pmTask.ID {
			engIDs = append(engIDs, t.ID)
		}
	}
	if len(engIDs) > 0 {
		_, err = task.Create(o.Store, &task.Task{
			Title:       "QA verify: " + e.Title,
			Type:        task.TypeQAVerify,
			ProjectID:   e.ProjectID,
			EpicID:      e.ID,
			ParentID:    pmTask.ID,
			TeamID:      pmTask.TeamID,
			Dependencies: engIDs,
		})
		if err != nil {
			return err
		}
	}

	return nil
}

// validatePlan checks that the PM plan is acyclic, uses only engineering tasks,
// and references known specializations.
func validatePlan(plan *Plan) error {
	ids := make(map[string]bool)
	for _, t := range plan.Tasks {
		if t.Type != "" && t.Type != "engineering" {
			return fmt.Errorf("plan task %s has invalid type %s (only engineering allowed)", t.ID, t.Type)
		}
		if t.ID == "" {
			return errors.New("plan task missing id")
		}
		if ids[t.ID] {
			return fmt.Errorf("duplicate plan task id %s", t.ID)
		}
		ids[t.ID] = true
	}

	// Topological sort to detect cycles.
	inDegree := make(map[string]int)
	graph := make(map[string][]string)
	for _, t := range plan.Tasks {
		inDegree[t.ID] = 0
	}
	for _, t := range plan.Tasks {
		for _, dep := range t.Dependencies {
			if !ids[dep] {
				return fmt.Errorf("plan task %s references unknown dependency %s", t.ID, dep)
			}
			graph[dep] = append(graph[dep], t.ID)
			inDegree[t.ID]++
		}
	}
	var queue []string
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, next := range graph[id] {
			inDegree[next]--
			if inDegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if visited != len(plan.Tasks) {
		return errors.New("plan contains cyclic dependencies")
	}
	return nil
}

// runsForTask returns all runs for a task, sorted by start time.
func runsForTask(st *store.Store, taskID string) ([]run.Run, error) {
	all, err := run.List(st)
	if err != nil {
		return nil, err
	}
	var filtered []run.Run
	for _, r := range all {
		if r.TaskID == taskID {
			filtered = append(filtered, r)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].StartedAt < filtered[j].StartedAt
	})
	return filtered, nil
}

// readRunMetadata extracts a string field from runs/<id>/metadata.json.
func readRunMetadata(st *store.Store, runID, key string) (string, error) {
	path := filepath.Join(st.Root, "runs", runID, "metadata.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return "", err
	}
	v, ok := m[key]
	if !ok {
		return "", fmt.Errorf("metadata key %s not found", key)
	}
	return v, nil
}

// Expand first-run helpers.

func ExpandEpic(st *store.Store, epicID string) error {
	o := &Orchestrator{Store: st}
	return o.RunEpic(epicID, func(event string, args ...interface{}) {})
}

