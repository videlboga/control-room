package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"control-room/internal/config"
	"control-room/internal/epic"
	"control-room/internal/gate"
	"control-room/internal/project"
	"control-room/internal/run"
	"control-room/internal/store"
	"control-room/internal/task"
	"control-room/internal/team"
)

// Orchestrator drives a deterministic workflow state machine for an Epic.
// It is intentionally small and hard to fool: transitions are hard-coded,
// LLM agents only generate content inside each step.
type Orchestrator struct {
	Store         *store.Store
	ManualApprove bool
	MaxRedo       int
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
	return o.runEpicLoop(epicID, false, cb)
}

// WatchEpic runs a detached-style orchestration loop. It starts every ready task
// in parallel, waits for all of them, then applies transitions and repeats.
func (o *Orchestrator) WatchEpic(epicID string, cb func(string, ...interface{})) error {
	// Acquire watch lock to prevent duplicate watch processes on the same epic.
	lockPath := filepath.Join(o.Store.Root, ".watch-lock", epicID+".lock")
	if isLocked(lockPath) {
		return fmt.Errorf("epic %s is already being watched (lock held by another process)", epicID)
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return fmt.Errorf("create watch-lock dir: %w", err)
	}
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0o644); err != nil {
		return fmt.Errorf("write watch-lock: %w", err)
	}
	defer o.cleanupWatchLock(epicID)
	return o.runEpicLoop(epicID, true, cb)
}

// runEpicLoop is the shared orchestration engine for both RunEpic (sequential)
// and WatchEpic (parallel batch) modes.
func (o *Orchestrator) runEpicLoop(epicID string, parallel bool, cb func(string, ...interface{})) error {
	resolved, err := epic.Resolve(o.Store, epicID)
	if err != nil {
		return err
	}
	epicID = resolved.ID
	e := resolved
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

	if _, err := o.ensureResearchTask(e, proj); err != nil {
		return err
	}

	for {
		// First, resolve any pending_review tasks that already have a valid verdict.
		// This prevents tasks from getting stuck between "agent finished" and
		// "resolution applied" when the orchestration loop restarts.
		pending, err := o.nextPendingReviewTasks(epicID)
		if err != nil {
			return err
		}
		for _, t := range pending {
			if err := o.applyTaskResolution(&t, t.Verdict, t.VerdictReason, e, proj, cb); err != nil {
				return err
			}
		}

		var ready []task.Task
		if parallel {
			ready, err = o.nextReadyTasks(epicID)
		} else {
			var single *task.Task
			single, err = o.nextReadyTask(epicID)
			if single != nil {
				ready = []task.Task{*single}
			}
		}
		if err != nil {
			return err
		}
		if len(ready) == 0 && len(pending) == 0 {
			if o.epicFinished(epicID) {
				e.Status = "done"
				_ = epic.Update(o.Store, e)
				cb("epic_done", e.ID)
				return nil
			}
			cb("waiting_for_review", epicID)
			if parallel {
				// Watch mode: absence of ready tasks is normal when the epic is blocked
				// waiting for review or external input. Return cleanly so the cronjob
				// does not spam error alerts.
				return nil
			}
			return errors.New("no ready tasks: remaining tasks are pending review or blocked")
		}

		if parallel {
			cb("batch_ready", len(ready))
		}
		for _, t := range ready {
			cb("task_start", t.ID, t.Type)
		}

		if parallel {
			var wg sync.WaitGroup
			mu := sync.Mutex{}
			results := make([]struct {
				t       *task.Task
				r       *run.Run
				verdict string
				reason  string
				err     error
			}, len(ready))
			for i := range ready {
				t := &ready[i]
				r, err := run.Start(o.Store, t.ID)
				if err != nil {
					return fmt.Errorf("failed to start run for task %s: %w", t.ID, err)
				}
				wg.Add(1)
				go func(idx int, t *task.Task, r *run.Run) {
					defer wg.Done()
					verdict, reason, err := o.runTaskToVerdict(t, r, cb)
					mu.Lock()
					results[idx] = struct {
						t       *task.Task
						r       *run.Run
						verdict string
						reason  string
						err     error
					}{t, r, verdict, reason, err}
					mu.Unlock()
				}(i, t, r)
			}
			wg.Wait()
			for _, res := range results {
				if res.err != nil {
					return res.err
				}
				if err := o.applyTaskResolution(res.t, res.verdict, res.reason, e, proj, cb); err != nil {
					return err
				}
			}
		} else {
			t := &ready[0]
			r, err := run.Start(o.Store, t.ID)
			if err != nil {
				return fmt.Errorf("failed to start run for task %s: %w", t.ID, err)
			}
			verdict, reason, err := o.runTaskToVerdict(t, r, cb)
			if err != nil {
				return err
			}
			if err := o.applyTaskResolution(t, verdict, reason, e, proj, cb); err != nil {
				return err
			}
		}
	}
}

func (o *Orchestrator) cleanupWatchLock(epicID string) {
	lockPath := filepath.Join(o.Store.Root, ".watch-lock", epicID+".lock")
	_ = os.Remove(lockPath)
}

func (o *Orchestrator) ensureResearchTask(e *epic.Epic, proj *project.Project) (*task.Task, error) {
	children, err := task.ListByEpic(o.Store, e.ID)
	if err != nil {
		return nil, err
	}
	for _, c := range children {
		if c.Type == task.TypeResearch {
			return &c, nil
		}
	}
	return task.Create(o.Store, &task.Task{
		Title:       "Research: " + e.Title,
		Description: e.Description,
		Type:        task.TypeResearch,
		ProjectID:   e.ProjectID,
		EpicID:      e.ID,
		TeamID:      proj.DefaultTeam,
	})
}

func (o *Orchestrator) runTaskToVerdict(t *task.Task, r *run.Run, cb func(string, ...interface{})) (string, string, error) {
	waitErr := run.WaitFor(o.Store, r.ID, func(ev run.Event) {
		cb("event", ev)
	})
	if waitErr != nil {
		cb("wait_error", waitErr)
	}

	verdict, reason, verdictErr := o.readVerdict(r.ID)
	if verdictErr != nil {
		cb("verdict_error", t.ID, verdictErr)
		verdict = "reject"
		reason = "no valid verdict in run metadata: " + verdictErr.Error()
	}

	// Policy: human-in-the-loop override for configured task types.
	if verdict == "approve" && o.policy().RequiresHumanOverride(string(t.Type)) {
		if o.Prompt != nil {
			cb("awaiting_manual_approval", t.ID)
			verdict, reason = o.Prompt(t.ID)
			cb("manual_verdict", t.ID, verdict, reason)
		}
	}

	t.Status = task.StatusPendingReview
	t.Verdict = verdict
	t.VerdictReason = reason
	_ = task.Update(o.Store, t)
	cb("task_verdict", t.ID, verdict, reason)

	if verdict == "approve" {
		proj, _ := project.Get(o.Store, t.ProjectID)
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
		if verdict != "approve" {
			t.Status = task.StatusPendingReview
			t.Verdict = verdict
			t.VerdictReason = reason
			_ = task.Update(o.Store, t)
			cb("task_verdict", t.ID, verdict, reason)
		}
	}

	// Policy: require explicit disposition, auto-approve stale pending tasks.
	verdict, reason = o.applyPolicy(t, verdict, reason)
	if t.Verdict != verdict || t.VerdictReason != reason {
		t.Verdict = verdict
		t.VerdictReason = reason
		_ = task.Update(o.Store, t)
		cb("task_verdict", t.ID, verdict, reason)
	}

	return verdict, reason, nil
}

// policy returns the workspace task policy, defaulting to sane values.
func (o *Orchestrator) policy() config.TaskPolicy {
	cfg, err := config.LoadOrCreate(o.Store.Root)
	if err != nil {
		return config.TaskPolicy{}
	}
	return cfg.Policy
}

// applyPolicy enforces require_disposition_for and auto_approve_after.
// It mutates t.Status and t.EndedAt when auto-approving.
func (o *Orchestrator) applyPolicy(t *task.Task, verdict, reason string) (string, string) {
	pol := o.policy()

	// Require explicit disposition: anything other than approve/reject is rejected.
	if !t.HasValidVerdict() && pol.RequiresDisposition(string(t.Type)) {
		return "reject", "policy requires explicit disposition: " + t.DispositionReason()
	}

	// If the agent produced a clear approve, mark the task as approved now so
	// it does not get stuck in pending_review waiting for a resolution pass
	// that may never come (e.g. watch mode where only open tasks are selected).
	if verdict == "approve" {
		t.Status = task.StatusApproved
		t.EndedAt = time.Now().UTC().Format(time.RFC3339)
		return verdict, reason
	}

	// Auto-approve tasks stuck in pending_review beyond the configured duration.
	if verdict != "approve" && pol.AutoApproveDuration() > 0 && t.IsStale(pol.AutoApproveDuration()) {
		t.Status = task.StatusApproved
		t.EndedAt = time.Now().UTC().Format(time.RFC3339)
		return "approve", "policy auto-approve after " + pol.AutoApproveAfter
	}

	return verdict, reason
}

func (o *Orchestrator) applyTaskResolution(t *task.Task, verdict, reason string, e *epic.Epic, proj *project.Project, cb func(string, ...interface{})) error {
	if verdict == "approve" {
		t.Status = task.StatusApproved
		t.EndedAt = time.Now().UTC().Format(time.RFC3339)
		_ = task.Update(o.Store, t)

		if t.Type == task.TypePMConsistency {
			e.Status = "done"
			_ = epic.Update(o.Store, e)
			cb("epic_done", e.ID)
			return nil
		}

		nextType := transitions[t.Type]["approved"]
		if t.Type == task.TypeResearch {
			if err := o.copyResearchDoc(t); err != nil {
				cb("research_doc_error", t.ID, err)
			}
		}
		if t.Type == task.TypePMPlan {
			if err := o.copyPlanDoc(t); err != nil {
				cb("plan_doc_error", t.ID, err)
			}
		}
		if err := o.mergeApprovedWorktree(t); err != nil {
			cb("merge_error", t.ID, err)
		}

		if nextType == "" || nextType == task.TypeQAVerify {
			cb("task_done", t.ID)
			return nil
		}
		if nextType == task.TypeEngineering {
			if err := o.expandEngineering(t); err != nil {
				return err
			}
			return nil
		}
		if _, err := task.Create(o.Store, &task.Task{
			Title:       o.nextTitle(nextType, t),
			Type:        nextType,
			ProjectID:   e.ProjectID,
			EpicID:      e.ID,
			ParentID:    t.ID,
			TeamID:      proj.DefaultTeam,
			Description: t.Description,
		}); err != nil {
			return err
		}
		return nil
	}

	// Reject path: normal redo or senior/recovery escalation when max redo reached.
	t.Status = task.StatusRejected
	t.EndedAt = time.Now().UTC().Format(time.RFC3339)
	_ = task.Update(o.Store, t)

	pol := o.policy()
	if t.RedoIndex >= pol.MaxRedo()-1 && t.EscalatedTo == "" {
		recoveryTask, err := o.escalateToSenior(t, e, proj, reason)
		if err != nil {
			cb("escalation_error", t.ID, err)
			// Fallback to normal redo if no senior agent is configured.
		} else {
			cb("escalated_to_senior", recoveryTask.ID, recoveryTask.AssignedAgentName, reason)
			// Do not create a normal redo; the recovery task is a fresh group for
			// the senior agent to take over manually.
			return nil
		}
	}

	redoType := transitions[t.Type]["rejected"]
	if redoType == "" {
		redoType = t.Type
	}
	redo, err := task.Redo(o.Store, t, reason)
	if err != nil {
		return err
	}
	redo.Type = redoType
	_ = task.Update(o.Store, redo)
	if redoType == task.TypeEngineering {
		if err := o.updateDependenciesAfterRedo(t, redo); err != nil {
			cb("redo_dep_update_error", redo.ID, err)
		}
	}
	cb("redo_created", redo.ID, redoType, reason)
	return nil
}

// escalateToSenior creates a recovery task assigned to a senior/recovery agent.
// The rejected base task is marked done; the recovery task inherits the base's
// upstream dependencies so it can run immediately.
func (o *Orchestrator) escalateToSenior(base *task.Task, e *epic.Epic, proj *project.Project, reason string) (*task.Task, error) {
	seniorTeam, seniorAgent, profile := o.findSeniorAgent(proj.DefaultTeam)
	if seniorTeam == "" || seniorAgent == "" {
		return nil, errors.New("no senior/recovery agent configured in team " + proj.DefaultTeam)
	}
	base.Status = task.StatusDone
	base.EndedAt = time.Now().UTC().Format(time.RFC3339)
	base.Verdict = "escalated"
	base.VerdictReason = "max redo reached; escalated to senior: " + reason
	if err := task.Update(o.Store, base); err != nil {
		return nil, err
	}
	recovery := &task.Task{
		Title:             "Recovery: " + base.Title,
		Description:       base.Description,
		Type:              task.TypeRecovery,
		ProjectID:         e.ProjectID,
		EpicID:            e.ID,
		ParentID:          base.ID,
		TeamID:            seniorTeam,
		AssignedAgentName: seniorAgent,
		AssignedProfile:   profile,
		Dependencies:      append([]string(nil), base.Dependencies...),
		Group:             base.Group,
		EscalatedTo:       seniorAgent,
		EscalatedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	if created, err := task.Create(o.Store, recovery); err != nil {
		return nil, err
	} else {
		return created, nil
	}
}

// findSeniorAgent looks for an agent with role senior/recovery/lead in the given team.
// It returns teamID, agentName, profile. Empty strings mean not found.
func (o *Orchestrator) findSeniorAgent(teamID string) (string, string, string) {
	tm, err := team.Get(o.Store, teamID)
	if err != nil {
		return "", "", ""
	}
	for _, role := range []string{"senior", "recovery", "lead"} {
		for name, ref := range tm.Agents {
			if strings.EqualFold(ref.Role, role) {
				return tm.ID, name, ref.Profile
			}
		}
	}
	return "", "", ""
}

// Expand first-run helpers.
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

// nextPendingReviewTasks returns tasks that already have a valid verdict but
// are still in pending_review, so the loop can apply their resolution.
func (o *Orchestrator) nextPendingReviewTasks(epicID string) ([]task.Task, error) {
	tasks, err := task.ListByEpic(o.Store, epicID)
	if err != nil {
		return nil, err
	}
	var pending []task.Task
	for _, t := range tasks {
		if t.Status != task.StatusPendingReview {
			continue
		}
		if t.Verdict != "approve" && t.Verdict != "reject" {
			continue
		}
		pending = append(pending, t)
	}
	return pending, nil
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

// copyPlanDoc copies docs/plan.json from the PM plan worktree into project docs
// so that engineering agents can read it alongside RESEARCH.md.
func (o *Orchestrator) copyPlanDoc(pmTask *task.Task) error {
	proj, err := project.Get(o.Store, pmTask.ProjectID)
	if err != nil {
		return err
	}
	runs, err := run.ListByTask(o.Store, pmTask.ID)
	if err != nil {
		return err
	}
	var wt string
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].ProjectID == pmTask.ProjectID {
			wt = runs[i].Worktree
			break
		}
	}
	if wt == "" {
		return errors.New("no run for PM plan task")
	}
	if wt == "" {
		return errors.New("PM plan run has no worktree")
	}
	src := filepath.Join(wt, "docs", "plan.json")
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("docs/plan.json not found in worktree: %w", err)
	}
	if proj.DocsDir != "" {
		docsDir := filepath.Join(proj.DocsDir, "docs")
		if err := os.MkdirAll(docsDir, 0o755); err != nil {
			return fmt.Errorf("mkdir docs dir failed: %w", err)
		}
		dst := filepath.Join(docsDir, "plan.json")
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("read plan.json failed: %w", err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return fmt.Errorf("write plan.json to docs dir failed: %w", err)
		}
	}
	// Also place docs/plan.json at the project repo root so consistency checks can find it.
	if proj.RepoPath != "" {
		docsDir := filepath.Join(proj.RepoPath, "docs")
		_ = os.MkdirAll(docsDir, 0o755)
		dst := filepath.Join(docsDir, "plan.json")
		data, _ := os.ReadFile(src)
		if len(data) > 0 {
			_ = os.WriteFile(dst, data, 0o644)
			_ = exec.Command("git", "-C", proj.RepoPath, "add", "docs/plan.json").Run()
			_ = exec.Command("git", "-C", proj.RepoPath, "commit", "-m", "docs: add plan.json").Run()
		}
	}
	return project.AddDoc(o.Store, proj.ID, src)
}

// expandEngineering reads the PM plan output, validates it, and creates engineering tasks.
// copyResearchDoc copies RESEARCH.md from the research worktree into project docs
// so that downstream agents (PM plan, engineering) can read it as a source of truth.
func (o *Orchestrator) copyResearchDoc(researchTask *task.Task) error {
	proj, err := project.Get(o.Store, researchTask.ProjectID)
	if err != nil {
		return err
	}
	runs, err := run.ListByTask(o.Store, researchTask.ID)
	if err != nil {
		return err
	}
	var wt string
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].ProjectID == researchTask.ProjectID {
			wt = runs[i].Worktree
			break
		}
	}
	if wt == "" {
		return errors.New("no run for research task")
	}
	if wt == "" {
		return errors.New("research run has no worktree")
	}
	src := filepath.Join(wt, "RESEARCH.md")
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("RESEARCH.md not found in worktree: %w", err)
	}
	if err := project.AddDoc(o.Store, proj.ID, src); err != nil {
		return fmt.Errorf("AddDoc failed: %w", err)
	}
	// Also place RESEARCH.md at the project repo root so downstream consistency checks can find it.
	if proj.RepoPath != "" {
		dst := filepath.Join(proj.RepoPath, "RESEARCH.md")
		data, _ := os.ReadFile(src)
		if len(data) > 0 {
			_ = os.WriteFile(dst, data, 0o644)
			_ = exec.Command("git", "-C", proj.RepoPath, "add", "RESEARCH.md").Run()
			_ = exec.Command("git", "-C", proj.RepoPath, "commit", "-m", "docs: add RESEARCH.md").Run()
		}
	}
	return nil
}

// updateDependenciesAfterRedo replaces references to the rejected task ID
// with the new redo task ID in all downstream task dependencies.
func (o *Orchestrator) updateDependenciesAfterRedo(rejected, redo *task.Task) error {
	all, err := task.List(o.Store)
	if err != nil {
		return err
	}
	for _, tt := range all {
		if tt.EpicID != redo.EpicID {
			continue
		}
		if tt.ID == redo.ID {
			continue
		}
		updated := false
		for i, dep := range tt.Dependencies {
			if dep == rejected.ID {
				tt.Dependencies[i] = redo.ID
				updated = true
			}
		}
		if updated {
			if err := task.Update(o.Store, &tt); err != nil {
				return err
			}
		}
	}
	return nil
}

// mergeApprovedWorktree merges the approved engineering worktree branch back into
// the project's main repo and advances the project's base commit so that
// downstream runs start from the updated state.
func (o *Orchestrator) mergeApprovedWorktree(t *task.Task) error {
	proj, err := project.Get(o.Store, t.ProjectID)
	if err != nil {
		return err
	}
	if proj.RepoPath == "" {
		return nil
	}
	runs, err := run.ListByTask(o.Store, t.ID)
	if err != nil || len(runs) == 0 {
		return errors.New("no run for engineering task")
	}
	// Use the latest finished run for this task in the same project.
	var r *run.Run
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].ProjectID == t.ProjectID {
			r = &runs[i]
			break
		}
	}
	if r == nil || r.Branch == "" || r.Worktree == "" {
		return nil
	}
	// Pull any new files from the worktree back into the branch so the merge
	// includes everything the agent produced, even uncommitted changes.
	_ = exec.Command("git", "-C", r.Worktree, "add", "-A").Run()
	if _, err := exec.Command("git", "-C", r.Worktree, "diff", "--cached", "--quiet").CombinedOutput(); err != nil {
		if out, err := exec.Command("git", "-C", r.Worktree, "commit", "-m", fmt.Sprintf("agent: %s %s", t.Type, t.ID)).CombinedOutput(); err != nil {
			return fmt.Errorf("commit worktree failed: %w\n%s", err, out)
		}
	}
	// Fast-forward main to the approved branch when possible.
	out, err := exec.Command("git", "-C", proj.RepoPath, "merge", "--ff-only", r.Branch).CombinedOutput()
	if err != nil {
		// main has moved on; rebase the approved branch onto main and try ff-only again.
		if rb, err := exec.Command("git", "-C", r.Worktree, "rebase", "main").CombinedOutput(); err != nil {
			_ = exec.Command("git", "-C", r.Worktree, "rebase", "--abort").Run()
			return fmt.Errorf("rebase worktree onto main failed: %w\n%s", err, rb)
		}
		out, err = exec.Command("git", "-C", proj.RepoPath, "merge", "--ff-only", r.Branch).CombinedOutput()
		if err != nil {
			return fmt.Errorf("merge failed after rebase: %w\n%s", err, out)
		}
	}
	// Update project base commit so that new worktrees start from the merged state.
	head, err := currentHeadCommit(proj.RepoPath)
	if err != nil {
		return fmt.Errorf("failed to read merged HEAD: %w", err)
	}
	proj.BaseCommit = head
	if err := project.Update(o.Store, proj); err != nil {
		return fmt.Errorf("failed to update project base commit: %w", err)
	}
	return nil
}

func currentHeadCommit(repoPath string) (string, error) {
	if repoPath == "" {
		return "", errors.New("no repo path")
	}
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

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

	// Support both {"plan": {"tasks": [...]}} and {"plan": [...]} shapes.
	var wrapper struct {
		Plan json.RawMessage `json:"plan"`
	}
	if err := json.Unmarshal([]byte(planData), &wrapper); err != nil {
		return fmt.Errorf("invalid plan JSON wrapper: %w", err)
	}
	var rawTasks json.RawMessage
	if len(wrapper.Plan) > 0 {
		if wrapper.Plan[0] == '[' {
			rawTasks = wrapper.Plan
		} else {
			var taskWrapper struct {
				Tasks json.RawMessage `json:"tasks"`
			}
			if err := json.Unmarshal(wrapper.Plan, &taskWrapper); err == nil {
				rawTasks = taskWrapper.Tasks
			}
		}
	}
	if rawTasks == nil {
		return errors.New("plan metadata missing tasks array")
	}
	var plan Plan
	if err := json.Unmarshal(rawTasks, &plan.Tasks); err != nil {
		return fmt.Errorf("invalid plan tasks JSON: %w", err)
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
			Title:        "QA verify: " + e.Title,
			Type:         task.TypeQAVerify,
			ProjectID:    e.ProjectID,
			EpicID:       e.ID,
			ParentID:     pmTask.ID,
			TeamID:       pmTask.TeamID,
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
