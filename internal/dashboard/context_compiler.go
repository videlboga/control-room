package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// humanizeEvidence converts raw JSON evidence entries into readable strings.
// Input: {"type":"verdict_reject","run_id":"xxx","task_id":"task_123","agent":"qa","step":"verify"}
// Output: "⚠ QA отверг задачу task_123 (verify)"
func humanizeEvidence(raw string) string {
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return raw // fallback to raw if not JSON
	}

	evType, _ := data["type"].(string)
	taskID, _ := data["task_id"].(string)
	agent, _ := data["agent"].(string)
	step, _ := data["step"].(string)
	reason, _ := data["reason"].(string)
	runID, _ := data["run_id"].(string)

	switch evType {
	case "verdict_reject":
		s := fmt.Sprintf("⚠ %s отверг задачу %s (%s)", agent, taskID, step)
		if reason != "" {
			s += fmt.Sprintf(" — %s", reason)
		}
		return s
	case "merge_error":
		s := fmt.Sprintf("⚠ Merge конфликт в %s (run %s)", taskID, runID)
		if reason != "" {
			s += fmt.Sprintf(" — %s", reason)
		}
		return s
	case "run_failed":
		s := fmt.Sprintf("⚠ Run %s упал (%s/%s)", runID, agent, step)
		if reason != "" {
			s += fmt.Sprintf(" — %s", reason)
		}
		return s
	default:
		return raw
	}
}

// ─── Context Compiler ───────────────────────────────────────────────────────

// CompiledContext is the output of compile_context().
// It gives an agent everything it needs to understand its mission without
// having to assemble context itself.
type CompiledContext struct {
	NodeType        string `json:"node_type"`         // workspace | project | task
	NodeID          string `json:"node_id"`
	Title           string `json:"title"`
	Mission         string `json:"mission"`           // what this node is about
	CurrentState    string `json:"current_state"`     // summary of current state
	Narrative       string `json:"narrative"`         // latest narrative memory
	Policy          string `json:"policy"`            // decisions/constraints (policy entries)
	RawEntries      int    `json:"raw_entries"`       // count of raw memory entries
	PreviousFailures string       `json:"previous_failures"` // recent failed runs (raw)
	RAGChunks        []string     `json:"rag_chunks"`         // relevant doc chunks from FTS5 search
	Knowledge        []string     `json:"knowledge"`          // cached project facts (architecture, stack)
	Beliefs          []string     `json:"beliefs"`            // current world model: confirmed/unverified claims
	Evidence         []string     `json:"evidence"`           // humanized evidence (readable strings)
	OpenTasks        []TaskBrief  `json:"open_tasks"`         // tasks not done
	Constraints      string       `json:"constraints"`        // project-level constraints
	GeneratedAt      string       `json:"generated_at"`
}

// TaskBrief is a compact task summary for context.
type TaskBrief struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	RedoIndex  int    `json:"redo_index"`
	Agent      string `json:"agent"`
}

// CompileContext gathers all relevant context for a node and returns it.
// This is the "secretary" — no agent assembles its own context.
func (s *Server) CompileContext(nodeType, nodeID string) (*CompiledContext, error) {
	ctx := &CompiledContext{
		NodeType:    nodeType,
		NodeID:      nodeID,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}

	switch nodeType {
	case "workspace":
		s.compileWorkspaceContext(ctx)
	case "project":
		s.compileProjectContext(ctx)
	case "task":
		s.compileTaskContext(ctx)
	}

	return ctx, nil
}

// compileWorkspaceContext gathers workspace-wide context.
func (s *Server) compileWorkspaceContext(ctx *CompiledContext) {
	ctx.Title = "Control Room"
	ctx.Mission = "Управление системой: запуск эпиков, мониторинг задач, диагностика проблем."

	// Get latest narrative
	narrative, _ := s.db.GetLatestNarrative("workspace", "workspace")
	ctx.Narrative = narrative

	// Get policy
	policies, _ := s.db.GetPolicy("workspace", "workspace")
	if len(policies) > 0 {
		var lines []string
		for _, p := range policies {
			lines = append(lines, "- "+p.Content)
		}
		ctx.Policy = strings.Join(lines, "\n")
	}

	// Count raw entries
	rawEntries, _ := s.db.GetMemory("workspace", "workspace", "raw", 1)
	ctx.RawEntries = len(rawEntries)

	// Open tasks across all projects
	allTasks, _ := s.store.ListTasks()
	var openTasks []TaskBrief
	for _, t := range allTasks {
		if t.Status == "open" || t.Status == "in_progress" || t.Status == "pending_review" {
			openTasks = append(openTasks, TaskBrief{
				ID: t.ID, Title: t.Title, Type: string(t.Type),
				Status: string(t.Status), RedoIndex: t.RedoIndex,
				Agent: t.AssignedAgentName,
			})
		}
	}
	ctx.OpenTasks = openTasks
	ctx.CurrentState = fmt.Sprintf("%d открытых задач, %d проектов.", len(openTasks), countProjects(s))

	// Recent failures: rejected tasks
	for _, t := range allTasks {
		if t.Status == "rejected" {
			ctx.PreviousFailures += fmt.Sprintf("- %s (%s): rejected — %s\n", t.ID, t.Type, t.VerdictReason)
		}
	}
}

// compileProjectContext gathers project-level context.
func (s *Server) compileProjectContext(ctx *CompiledContext) {
	p, err := s.store.GetProject(ctx.NodeID)
	if err != nil || p == nil {
		ctx.Title = ctx.NodeID
		return
	}
	ctx.Title = p.Title
	ctx.Mission = fmt.Sprintf("Проект \"%s\" (ID: %s). Репозиторий: %s. Язык: %s. Команда: %s.",
		p.Title, p.ID, p.RepoPath, p.Language, p.DefaultTeam)
	ctx.Constraints = fmt.Sprintf("Test: %s, Lint: %s", p.TestCommand, p.LintCommand)

	// Latest narrative
	narrative, _ := s.db.GetLatestNarrative("project", ctx.NodeID)
	ctx.Narrative = narrative

	// Policy
	policies, _ := s.db.GetPolicy("project", ctx.NodeID)
	if len(policies) > 0 {
		var lines []string
		for _, p := range policies {
			lines = append(lines, "- "+p.Content)
		}
		ctx.Policy = strings.Join(lines, "\n")
	}

	// Count raw entries
	rawEntries, _ := s.db.GetMemory("project", ctx.NodeID, "raw", 1)
	ctx.RawEntries = len(rawEntries)

	// Evidence — humanize raw JSON into readable strings
	evidenceEntries, _ := s.db.GetEvidence("project", ctx.NodeID)
	for _, e := range evidenceEntries {
		ctx.Evidence = append(ctx.Evidence, humanizeEvidence(e.Content))
	}

	// Beliefs — current world model (confirmed/unverified claims)
	beliefEntries, _ := s.db.GetBeliefs("project", ctx.NodeID)
	for _, b := range beliefEntries {
		ctx.Beliefs = append(ctx.Beliefs, b.Content)
	}

	// RAG — search project docs for context relevant to open tasks or project mission
	ragQuery := ""
	if len(ctx.OpenTasks) > 0 {
		ragQuery = ctx.OpenTasks[0].Title
	} else if ctx.Mission != "" {
		// Use first 50 chars of mission as RAG query — shorter = better recall
		if len(ctx.Mission) > 50 {
			ragQuery = ctx.Mission[:50]
		} else {
			ragQuery = ctx.Mission
		}
	}
	if ragQuery != "" {
		chunks, _ := s.db.SearchRAG(ctx.NodeID, ragQuery, 3)
		for _, c := range chunks {
			ctx.RAGChunks = append(ctx.RAGChunks, c.Content)
		}
	}

	// Knowledge — cached project facts (architecture, stack, dependencies)
	knowledgeEntries, _ := s.db.GetKnowledge("project", ctx.NodeID)
	for _, k := range knowledgeEntries {
		ctx.Knowledge = append(ctx.Knowledge, k.Content)
	}

	// Open tasks for this project
	allTasks, _ := s.store.ListTasks()
	var openTasks []TaskBrief
	var failedRuns []string
	for _, t := range allTasks {
		if t.ProjectID != ctx.NodeID {
			continue
		}
		if t.Status == "open" || t.Status == "in_progress" || t.Status == "pending_review" {
			openTasks = append(openTasks, TaskBrief{
				ID: t.ID, Title: t.Title, Type: string(t.Type),
				Status: string(t.Status), RedoIndex: t.RedoIndex,
				Agent: t.AssignedAgentName,
			})
		}
		if t.Status == "rejected" {
			failedRuns = append(failedRuns, fmt.Sprintf("- %s (%s): %s", t.ID, t.Type, t.VerdictReason))
		}
	}
	ctx.OpenTasks = openTasks
	ctx.PreviousFailures = strings.Join(failedRuns, "\n")
	ctx.CurrentState = fmt.Sprintf("%d открытых задач, %d raw записей в памяти.", len(openTasks), ctx.RawEntries)
}

// compileTaskContext gathers task-level context.
func (s *Server) compileTaskContext(ctx *CompiledContext) {
	t, err := s.store.GetTask(ctx.NodeID)
	if err != nil || t == nil {
		ctx.Title = ctx.NodeID
		return
	}
	ctx.Title = t.Title
	ctx.Mission = fmt.Sprintf("Задача: %s. Тип: %s. Описание: %s", t.Title, t.Type, t.Description)

	// Latest narrative for this task
	narrative, _ := s.db.GetLatestNarrative("task", ctx.NodeID)
	ctx.Narrative = narrative

	// Policy (inherited from project)
	if t.ProjectID != "" {
		policies, _ := s.db.GetPolicy("project", t.ProjectID)
		if len(policies) > 0 {
			var lines []string
			for _, p := range policies {
				lines = append(lines, "- "+p.Content)
			}
			ctx.Policy = strings.Join(lines, "\n")
		}
	}

	// Raw entries (previous runs)
	rawEntries, _ := s.db.GetMemory("task", ctx.NodeID, "raw", 20)
	ctx.RawEntries = len(rawEntries)

	// Previous failures from raw entries
	var failures []string
	for _, r := range rawEntries {
		// Parse raw JSON to check for failed status
		var raw struct {
			Status  string `json:"status"`
			RunID   string `json:"run_id"`
			Step    string `json:"step"`
			Summary string `json:"summary"`
		}
		if json.Unmarshal([]byte(r.Content), &raw) == nil {
			if raw.Status == "failed" {
				failures = append(failures, fmt.Sprintf("- Run %s (%s): %s", raw.RunID, raw.Step, raw.Summary))
			}
		}
	}
	ctx.PreviousFailures = strings.Join(failures, "\n")

	ctx.CurrentState = fmt.Sprintf("Статус: %s, redo: %d, агент: %s.", t.Status, t.RedoIndex, t.AssignedAgentName)
}

func countProjects(s *Server) int {
	projects, err := s.store.ListProjects()
	if err != nil {
		return 0
	}
	return len(projects)
}

// ─── HTTP Handler ───────────────────────────────────────────────────────────

func (s *Server) apiContext(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/context/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		jsonError(w, "path must be /context/{node_type}/{node_id}", http.StatusBadRequest)
		return
	}
	nodeType := parts[0]
	nodeID := parts[1]

	ctx, err := s.CompileContext(nodeType, nodeID)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, ctx)
}