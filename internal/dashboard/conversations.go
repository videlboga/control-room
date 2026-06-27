package dashboard

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"control-room/internal/comment"
)

// ─── Tree ──────────────────────────────────────────────────────────────────

// TreeNode represents a node in the workspace → project → task tree.
type TreeNode struct {
	Type     string      `json:"type"` // "workspace" | "project" | "task"
	ID       string      `json:"id"`
	Title    string      `json:"title"`
	Status   string      `json:"status,omitempty"`   // for tasks
	Step     string      `json:"step,omitempty"`     // for tasks: current workflow step
	Agent    string      `json:"agent,omitempty"`    // for tasks: assigned agent
	RedoIndex int        `json:"redo_index,omitempty"`
	Streaming bool       `json:"streaming,omitempty"` // true if a run is active
	RunID     string      `json:"run_id,omitempty"`   // active run ID if streaming
	Children  []*TreeNode `json:"children,omitempty"`
}

// BuildTree returns the full workspace → projects → tasks tree.
// Tasks that belong to a project are nested under it.
// Streaming status is determined by checking active runs in the DB.
func (s *Server) BuildTree() (*TreeNode, error) {
	workspace := &TreeNode{
		Type:  "workspace",
		ID:    "workspace",
		Title: "Control Room",
	}

	// Get all projects
	projects, err := s.store.ListProjects()
	if err != nil {
		return nil, err
	}

	// Get all tasks
	allTasks, err := s.store.ListTasks()
	if err != nil {
		return nil, err
	}

	// Get active runs for streaming status
	activeRuns, _ := s.db.GetActiveRuns()
	streamingTaskIDs := map[string]string{} // task_id → run_id
	for _, r := range activeRuns {
		streamingTaskIDs[r.TaskID] = r.ID
	}

	// Group tasks by project
	tasksByProject := map[string][]*TreeNode{}
	for _, t := range allTasks {
		node := &TreeNode{
			Type:      "task",
			ID:        t.ID,
			Title:     t.Title,
			Status:    string(t.Status),
			Step:      string(t.Type),
			Agent:     t.AssignedAgentName,
			RedoIndex: t.RedoIndex,
		}
		if runID, ok := streamingTaskIDs[t.ID]; ok {
			node.Streaming = true
			node.RunID = runID
		}
		tasksByProject[t.ProjectID] = append(tasksByProject[t.ProjectID], node)
	}

	// Build project nodes
	for _, p := range projects {
		pnode := &TreeNode{
			Type:    "project",
			ID:       p.ID,
			Title:    p.Title,
			Children: tasksByProject[p.ID],
		}
		workspace.Children = append(workspace.Children, pnode)
	}

	// Sort projects alphabetically
	sort.Slice(workspace.Children, func(i, j int) bool {
		return workspace.Children[i].Title < workspace.Children[j].Title
	})

	return workspace, nil
}

// ─── Conversations ──────────────────────────────────────────────────────────

// ConversationMessage is a single message in a conversation.
// It can be a comment (human/agent), an event (tool call, step), or a log line.
type ConversationMessage struct {
	ID        string `json:"id"`
	Role      string `json:"role"`       // "human" | "agent" | "system" | "event" | "log"
	Author    string `json:"author,omitempty"`
	Body      string `json:"body"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"type,omitempty"` // for events: tool_call, step, error, info
	Tool      string `json:"tool,omitempty"` // for tool_call events
}

// GetConversation returns the conversation (messages) for a given entity.
// kind is "workspace" | "project" | "task" | "run"
// For tasks, this includes: comments + run events (interleaved by time)
// For projects/workspace: just comments
// For runs: run events + agent log
func (s *Server) GetConversation(kind, id string) ([]ConversationMessage, error) {
	// Always get comments
	comments, err := comment.List(s.store.Store, kind, id)
	if err != nil {
		slog.Warn("conversation comments", "kind", kind, "id", id, "err", err)
		comments = nil
	}

	var messages []ConversationMessage

	// Convert comments to messages
	for _, c := range comments {
		messages = append(messages, ConversationMessage{
			ID:        c.ID,
			Role:      c.Author,
			Author:    c.Author,
			Body:      c.Body,
			Timestamp: c.CreatedAt,
		})
	}

	// For tasks: also include run events
	if kind == "task" {
		taskRuns, err := s.store.ListRuns()
		if err == nil {
			for _, r := range taskRuns {
				if r.TaskID != id {
					continue
				}
				// Add a system message about the run
				messages = append(messages, ConversationMessage{
					Role:      "system",
					Body:      "Run " + r.ID + " — " + r.Status + " — " + r.Step + " — " + r.Agent,
					Timestamp: r.StartedAt,
					Type:      "run_start",
				})

				// Get events for this run (use existing DB method, returns newest-first)
				events, err := s.db.GetRunEvents(r.ID, 200)
				if err == nil {
					// Reverse to chronological order
					for i := len(events) - 1; i >= 0; i-- {
						ev := events[i]
						messages = append(messages, ConversationMessage{
							Role:      "event",
							Author:    ev.Agent,
							Body:      ev.Payload,
							Timestamp: ev.Timestamp,
							Type:      ev.Type,
							Tool:      ev.Tool,
						})
					}
				}

				// Get summary if run is done
				if r.Status == "done" || r.Status == "failed" {
					summary := r.Summary
					if summary != "" {
						messages = append(messages, ConversationMessage{
							Role:      "system",
							Body:      summary,
							Timestamp: r.EndedAt,
							Type:      "run_end",
						})
					}
				}
			}
		}
	}

	// For runs: include events + agent log
	if kind == "run" {
		events, err := s.db.GetRunEvents(id, 200)
		if err == nil {
			// Reverse to chronological order
			for i := len(events) - 1; i >= 0; i-- {
				ev := events[i]
				messages = append(messages, ConversationMessage{
					Role:      "event",
					Author:    ev.Agent,
					Body:      ev.Payload,
					Timestamp: ev.Timestamp,
					Type:      ev.Type,
					Tool:      ev.Tool,
				})
			}
		}
	}

	// Sort by timestamp
	sort.SliceStable(messages, func(i, j int) bool {
		return messages[i].Timestamp < messages[j].Timestamp
	})

	return messages, nil
}

// PostComment adds a comment to an entity's conversation.
func (s *Server) PostComment(kind, id, author, body string) (*comment.Comment, error) {
	c, err := comment.Add(s.store.Store, kind, id, author, body)
	if err != nil {
		return nil, err
	}

	// Broadcast via WebSocket
	if s.hub != nil {
		s.hub.BroadcastMessage("conversation:"+kind+":"+id, map[string]any{
			"type": "new_comment",
			"data": c,
		})
	}

	return c, nil
}

// ─── Live Previews ──────────────────────────────────────────────────────────

// LivePreview represents a streaming chat preview for the left panel.
type LivePreview struct {
	Type      string `json:"type"`       // "task" | "workspace"
	ID        string `json:"id"`
	Title     string `json:"title"`
	ProjectID string `json:"project_id,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Step      string `json:"step,omitempty"`
	Status    string `json:"status,omitempty"`
	Tail      string `json:"tail"` // last few lines of output
	TailLines []string `json:"tail_lines"`
}

// GetLivePreviews returns previews of all currently streaming conversations.
// This includes active runs (task-level) and the controller agent (workspace-level).
func (s *Server) GetLivePreviews() []LivePreview {
	var previews []LivePreview

	// Active runs from DB
	activeRuns, err := s.db.GetActiveRuns()
	if err != nil {
		slog.Warn("live previews: GetActiveRuns", "err", err)
		return previews
	}

	for _, r := range activeRuns {
		// Get task info for title
		var title string
		var projectID string
		t, err := s.store.GetTask(r.TaskID)
		if err == nil && t != nil {
			title = t.Title
			projectID = t.ProjectID
		} else {
			title = r.TaskID
		}

		// Read tail from activity.log
		var tailLines []string
		if s.store != nil {
			logPath := filepath.Join(s.store.Root, "runs", r.ID, "activity.log")
			tailLines = readLastLines(logPath, 5)
		}

		tail := strings.Join(tailLines, "\n")

		previews = append(previews, LivePreview{
			Type:      "task",
			ID:        r.TaskID,
			Title:     title,
			ProjectID: projectID,
			RunID:     r.ID,
			Agent:     r.Agent,
			Step:      r.Step,
			Status:    r.Status,
			Tail:      tail,
			TailLines: tailLines,
		})
	}

	// Check if controller is running
	if controllerSession != nil && controllerSession.alive() {
		previews = append(previews, LivePreview{
			Type:   "workspace",
			ID:     "workspace",
			Title:  "Controller Agent",
			Agent:  "controller",
			Status: "running",
			Tail:   "",
		})
	}

	return previews
}

// ─── HTTP Handlers ──────────────────────────────────────────────────────────

func (s *Server) apiTree(w http.ResponseWriter, r *http.Request) {
	tree, err := s.BuildTree()
	if err != nil {
		jsonError(w, "failed to build tree: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, tree)
}

func (s *Server) apiConversation(w http.ResponseWriter, r *http.Request) {
	// Path: /api/v1/conversations/{kind}/{id}
	// kind: workspace | project | task | run
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/conversations/"), "/")
	if len(parts) < 2 {
		jsonError(w, "path must be /conversations/{kind}/{id}", http.StatusBadRequest)
		return
	}
	kind := parts[0]
	id := parts[1]

	switch r.Method {
	case http.MethodGet:
		msgs, err := s.GetConversation(kind, id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]any{"messages": msgs})

	case http.MethodPost:
		var body struct {
			Author string `json:"author"`
			Body   string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if body.Body == "" {
			jsonError(w, "body is required", http.StatusBadRequest)
			return
		}
		if body.Author == "" {
			body.Author = "human"
		}
		c, err := s.PostComment(kind, id, body.Author, body.Body)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, c)

	default:
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) apiLivePreviews(w http.ResponseWriter, r *http.Request) {
	previews := s.GetLivePreviews()
	jsonResponse(w, map[string]any{"previews": previews})
}

// ─── Run events helper ──────────────────────────────────────────────────────

// (GetRunEvents already exists in db.go with signature (runID string, limit int) → []EventRow)
// EventRow has: ID, RunID, Timestamp, Agent, Type, Step, Tool, Payload

// ─── Timestamp helper ───────────────────────────────────────────────────────

func nowRFC333() string {
	return time.Now().UTC().Format(time.RFC3339)
}