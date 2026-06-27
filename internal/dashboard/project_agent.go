package dashboard

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ProjectSession manages a running hermes project agent process.
// Unlike the controller (singleton), multiple project sessions can run
// concurrently — one per project.
type ProjectSession struct {
	mu        sync.Mutex
	id        string
	projectID string
	cmd       *exec.Cmd
	stdout    io.Reader
	stderr    io.Reader
	sessionID string // hermes session id for --resume
	hub       *Hub
	done      chan struct{}
}

var (
	projectMu        sync.Mutex
	projectSessions  = map[string]*ProjectSession{} // projectID → session
)

// LaunchProjectAgent starts a hermes project agent for a specific project
// and streams output to the hub on a project-specific channel.
// fullPrompt is the complete prompt to send to the agent (already compiled with context).
func LaunchProjectAgent(hub *Hub, projectID, fullPrompt string) (*ProjectSession, error) {
	projectMu.Lock()
	defer projectMu.Unlock()

	if existing, ok := projectSessions[projectID]; ok && existing.alive() {
		return nil, fmt.Errorf("project agent already running for %s (session %s)", projectID, existing.id)
	}

	hermesPath := "/home/cyberkitty/.local/bin/hermes"
	if _, err := os.Stat(hermesPath); err != nil {
		return nil, fmt.Errorf("hermes binary not found: %w", err)
	}

	// Use --pass-session-id so we can resume later.
	cmd := exec.Command(hermesPath,
		"--profile", "hw_agent_project",
		"chat", "-q", fullPrompt,
		"--toolsets", "terminal,file,web",
		"--yolo", "--source", "tool",
		"--max-turns", "30",
		"--pass-session-id",
	)
	cmd.Dir = "/home/cyberkitty/Projects/control-room"

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start hermes: %w", err)
	}

	channel := "project:" + projectID
	cs := &ProjectSession{
		id:        fmt.Sprintf("proj_%d", time.Now().Unix()),
		projectID: projectID,
		cmd:       cmd,
		stdout:    stdoutPipe,
		stderr:    stderrPipe,
		hub:       hub,
		done:      make(chan struct{}),
	}

	// Broadcast launch event.
	hub.BroadcastMessage(channel, map[string]any{
		"type":       "started",
		"id":         cs.id,
		"project_id": projectID,
		"pid":        cmd.Process.Pid,
	})

	// Stream stdout to WS channel.
	go cs.streamOutput(stdoutPipe, "output", channel)
	// Stream stderr to WS channel.
	go cs.streamOutput(stderrPipe, "error", channel)

	// Wait for process to finish.
	go func() {
		err := cmd.Wait()
		close(cs.done)
		hub.BroadcastMessage(channel, map[string]any{
			"type":   "ended",
			"id":     cs.id,
			"exit":   cmd.ProcessState.ExitCode(),
			"error":  fmt.Sprintf("%v", err),
		})
		projectMu.Lock()
		if existing, ok := projectSessions[projectID]; ok && existing == cs {
			delete(projectSessions, projectID)
		}
		projectMu.Unlock()
	}()

	projectSessions[projectID] = cs
	return cs, nil
}

// SendToProjectAgent sends a follow-up message to a running project agent via --resume.
func SendToProjectAgent(hub *Hub, projectID, message string) error {
	projectMu.Lock()
	cs := projectSessions[projectID]
	projectMu.Unlock()

	if cs == nil || !cs.alive() {
		return fmt.Errorf("no project agent running for %s", projectID)
	}

	channel := "project:" + projectID
	go func() {
		hermesPath := "/home/cyberkitty/.local/bin/hermes"
		cmd := exec.Command(hermesPath,
			"--profile", "hw_agent_project",
			"--resume", cs.sessionID,
			"chat", "-q", message,
			"--toolsets", "terminal,file,web",
			"--yolo", "--source", "tool",
			"--max-turns", "20",
		)
		cmd.Dir = "/home/cyberkitty/Projects/control-room"
		out, _ := cmd.CombinedOutput()
		hub.BroadcastMessage(channel, map[string]any{
			"type":   "output",
			"id":     cs.id,
			"source": "user_followup",
			"text":   string(out),
		})
	}()

	return nil
}

// StopProjectAgent kills a running project agent.
func StopProjectAgent(projectID string) error {
	projectMu.Lock()
	cs := projectSessions[projectID]
	projectMu.Unlock()

	if cs == nil || !cs.alive() {
		return fmt.Errorf("no project agent running for %s", projectID)
	}

	if cs.cmd != nil && cs.cmd.Process != nil {
		_ = cs.cmd.Process.Kill()
	}
	return nil
}

// GetProjectAgentStatus returns whether a project agent is running for the given project.
func GetProjectAgentStatus(projectID string) map[string]any {
	projectMu.Lock()
	cs := projectSessions[projectID]
	projectMu.Unlock()

	if cs == nil || !cs.alive() {
		return map[string]any{"running": false}
	}
	return map[string]any{
		"running":    true,
		"id":         cs.id,
		"pid":        cs.cmd.Process.Pid,
		"session_id": cs.sessionID,
	}
}

// ListRunningProjectAgents returns all running project agent sessions.
func ListRunningProjectAgents() []map[string]any {
	projectMu.Lock()
	defer projectMu.Unlock()

	var out []map[string]any
	for pid, cs := range projectSessions {
		if cs.alive() {
			out = append(out, map[string]any{
				"project_id": pid,
				"id":         cs.id,
				"pid":        cs.cmd.Process.Pid,
			})
		}
	}
	return out
}

func (cs *ProjectSession) alive() bool {
	if cs.cmd == nil || cs.cmd.ProcessState != nil {
		return false
	}
	return true
}

func (cs *ProjectSession) streamOutput(r io.Reader, source, channel string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()

		// Try to capture session ID from hermes output.
		if strings.Contains(line, "session_id") || strings.Contains(line, "Session ID:") {
			cs.extractSessionID(line)
		}

		cs.hub.BroadcastMessage(channel, map[string]any{
			"type":   source,
			"id":     cs.id,
			"text":   line,
			"ts":     time.Now().UTC().Format(time.RFC3339),
		})
	}
}

func (cs *ProjectSession) extractSessionID(line string) {
	parts := strings.Fields(line)
	for i, p := range parts {
		if (p == "session_id" || p == "Session" || p == "ID:") && i+1 < len(parts) {
			candidate := strings.Trim(parts[i+1], ",;:\"'")
			if len(candidate) > 8 && (strings.Contains(candidate, "_") || strings.Contains(candidate, "-")) {
				cs.mu.Lock()
				cs.sessionID = candidate
				cs.mu.Unlock()
				slog.Info("project agent session id captured", "session", candidate, "project", cs.projectID)
			}
		}
	}
}

// ─── HTTP Handlers ──────────────────────────────────────────────────────────

// apiProjectAgent handles project agent lifecycle.
// Path: /api/v1/project-agent/{project_id}[/{action}]
// action: "launch" (POST), "send" (POST), "stop" (POST), "status" (GET)
func (s *Server) apiProjectAgent(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/project-agent/")
	parts := strings.Split(rest, "/")
	if len(parts) < 1 || parts[0] == "" {
		jsonError(w, "project_id required", http.StatusBadRequest)
		return
	}
	projectID := parts[0]
	action := ""
	if len(parts) >= 2 {
		action = parts[1]
	}

	// Default action based on method
	if action == "" {
		if r.Method == http.MethodGet {
			action = "status"
		} else if r.Method == http.MethodPost {
			action = "launch"
		}
	}

	switch action {
	case "launch":
		if r.Method != http.MethodPost {
			jsonError(w, "launch requires POST", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Prompt string `json:"prompt"`
		}
		// Body is optional
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}

		// Get project title
		title := projectID
		p, err := s.store.GetProject(projectID)
		if err == nil && p != nil {
			title = p.Title
		}

		// Compile context for this project
		compiled, ctxErr := s.CompileContext("project", projectID)
		fullPrompt := fmt.Sprintf("Ты — проектный агент для проекта \"%s\".", title)
		if body.Prompt != "" {
			fullPrompt += " " + body.Prompt
		}
		if ctxErr == nil && compiled != nil {
			fullPrompt += fmt.Sprintf("\n\n## Контекст проекта\n\n**Миссия:** %s\n**Состояние:** %s\n",
				compiled.Mission, compiled.CurrentState)
			if compiled.Narrative != "" {
				fullPrompt += "\n### Narrative\n" + compiled.Narrative + "\n"
			}
			if compiled.Policy != "" {
				fullPrompt += "\n### Policy\n" + compiled.Policy + "\n"
			}
			if len(compiled.OpenTasks) > 0 {
				fullPrompt += fmt.Sprintf("\n### Открытые задачи (%d)\n", len(compiled.OpenTasks))
				for _, t := range compiled.OpenTasks {
					fullPrompt += fmt.Sprintf("- [%s] %s (redo: %d, agent: %s)\n", t.Status, t.Title, t.RedoIndex, t.Agent)
				}
			}
			if compiled.PreviousFailures != "" {
				fullPrompt += "\n### Предыдущие неудачи\n" + compiled.PreviousFailures + "\n"
			}
		}
		fullPrompt += "\nОтветь на русском."

		cs, err := LaunchProjectAgent(s.hub, projectID, fullPrompt)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]any{
			"status":      "started",
			"id":          cs.id,
			"project_id":  projectID,
			"pid":         cs.cmd.Process.Pid,
		})

	case "send":
		if r.Method != http.MethodPost {
			jsonError(w, "send requires POST", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if body.Message == "" {
			jsonError(w, "message is required", http.StatusBadRequest)
			return
		}
		if err := SendToProjectAgent(s.hub, projectID, body.Message); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Echo user message to channel
		s.hub.BroadcastMessage("project:"+projectID, map[string]any{
			"type": "user_message",
			"text": body.Message,
			"ts":   time.Now().UTC().Format(time.RFC3339),
		})
		jsonResponse(w, map[string]any{"status": "sent"})

	case "stop":
		if r.Method != http.MethodPost {
			jsonError(w, "stop requires POST", http.StatusMethodNotAllowed)
			return
		}
		if err := StopProjectAgent(projectID); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]any{"status": "stopped"})

	case "status":
		status := GetProjectAgentStatus(projectID)
		jsonResponse(w, status)

	default:
		jsonError(w, "unknown action: "+action, http.StatusBadRequest)
	}
}