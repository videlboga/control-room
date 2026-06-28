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
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ProjectSession manages a running hermes project agent process.
// Unlike the controller (singleton), multiple project sessions can run
// concurrently — one per project.
type ProjectSession struct {
	mu           sync.Mutex
	id           string
	projectID    string
	cmd          *exec.Cmd
	stdout       io.Reader
	stderr       io.Reader
	sessionID    string // hermes session id for --resume
	hub          *Hub
	done         chan struct{}
	logFile      *os.File // session log file for persistence
	idleSince    time.Time // when the process ended (for idle timeout)
	sessionReader *SessionReader // polls state.db for structured messages
}

var (
	projectMu        sync.Mutex
	projectSessions  = map[string]*ProjectSession{} // projectID → session (alive or idle)
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
		"--max-turns", "60",
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

	// Open a log file for this session — persists project agent output across page refreshes.
	logDir := filepath.Join(os.Getenv("HOME"), ".control-room", "project_agent_logs")
	_ = os.MkdirAll(logDir, 0o755)
	logPath := filepath.Join(logDir, projectID+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		slog.Warn("project agent log file", "err", err)
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
		logFile:   logFile,
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

	// Start session reader — auto-detects latest session from state.db.
	reader, err := NewSessionReader("hw_agent_project", channel, hub)
	if err == nil {
		reader.Start("") // auto-detect
		cs.sessionReader = reader
		slog.Info("project agent session reader started", "project", projectID)
	}

	// Wait for process to finish.
	go func() {
		err := cmd.Wait()
		close(cs.done)
		if cs.logFile != nil {
			_ = cs.logFile.Close()
		}
		// Stop session reader
		if cs.sessionReader != nil {
			cs.sessionReader.Stop()
		}
		hub.BroadcastMessage(channel, map[string]any{
			"type":   "ended",
			"id":     cs.id,
			"exit":   cmd.ProcessState.ExitCode(),
			"error":  fmt.Sprintf("%v", err),
		})
		// Don't delete the session — keep it idle so we can --resume.
		// Mark when the process ended.
		projectMu.Lock()
		if existing, ok := projectSessions[projectID]; ok && existing == cs {
			cs.idleSince = time.Now()
		}
		projectMu.Unlock()

		// Schedule cleanup after 10 minutes idle.
		go func() {
			time.Sleep(10 * time.Minute)
			projectMu.Lock()
			if existing, ok := projectSessions[projectID]; ok && existing == cs && !cs.alive() {
				delete(projectSessions, projectID)
			}
			projectMu.Unlock()
		}()
	}()

	projectSessions[projectID] = cs
	return cs, nil
}

// SendToProjectAgent sends a follow-up message to a project agent.
// If the agent is still running → resume live session.
// If the agent has ended but has a session ID → --resume (starts a new process
// that continues the conversation). This keeps context across messages without
// relaunching from scratch.
func SendToProjectAgent(hub *Hub, projectID, message string) error {
	projectMu.Lock()
	cs := projectSessions[projectID]
	projectMu.Unlock()

	if cs == nil {
		return fmt.Errorf("no project agent session for %s", projectID)
	}

	// If process is still alive, send via --resume (continues running session).
	// If process ended but we have sessionID, also use --resume (new process, same context).
	// If no sessionID — return error so frontend launches a new agent.
	if cs.sessionID == "" {
		return fmt.Errorf("no session ID for %s", projectID)
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
			"--max-turns", "30",
			"--pass-session-id",
		)
		cmd.Dir = "/home/cyberkitty/Projects/control-room"

		// Reopen log file for append
		logDir := filepath.Join(os.Getenv("HOME"), ".control-room", "project_agent_logs")
		logPath := filepath.Join(logDir, projectID+".log")
		logFile, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)

		stdoutPipe, err := cmd.StdoutPipe()
		if err != nil {
			if logFile != nil { _ = logFile.Close() }
			return
		}
		stderrPipe, err := cmd.StderrPipe()
		if err != nil {
			if logFile != nil { _ = logFile.Close() }
			return
		}

		if err := cmd.Start(); err != nil {
			if logFile != nil { _ = logFile.Close() }
			hub.BroadcastMessage(channel, map[string]any{
				"type":  "error",
				"id":    cs.id,
				"text":  fmt.Sprintf("resume failed: %v", err),
			})
			return
		}

		// Create a new session object for this resumed process
		resumeCS := &ProjectSession{
			id:        cs.id,
			projectID: projectID,
			cmd:       cmd,
			hub:       hub,
			done:      make(chan struct{}),
			logFile:   logFile,
		}

		// Broadcast started event
		hub.BroadcastMessage(channel, map[string]any{
			"type":       "started",
			"id":         resumeCS.id,
			"project_id": projectID,
			"pid":        cmd.Process.Pid,
		})

		go resumeCS.streamOutput(stdoutPipe, "output", channel)
		go resumeCS.streamOutput(stderrPipe, "error", channel)

		// Wait for process to finish
		go func() {
			err := cmd.Wait()
			close(resumeCS.done)
			if resumeCS.logFile != nil { _ = resumeCS.logFile.Close() }
			hub.BroadcastMessage(channel, map[string]any{
				"type":  "ended",
				"id":    resumeCS.id,
				"exit":  cmd.ProcessState.ExitCode(),
				"error": fmt.Sprintf("%v", err),
			})
			// Mark idle, keep sessionID for future resumes
			projectMu.Lock()
			if existing, ok := projectSessions[projectID]; ok && existing == cs {
				existing.idleSince = time.Now()
			}
			projectMu.Unlock()

			// Schedule cleanup
			go func() {
				time.Sleep(10 * time.Minute)
				projectMu.Lock()
				if existing, ok := projectSessions[projectID]; ok && existing == cs && !cs.alive() {
					delete(projectSessions, projectID)
				}
				projectMu.Unlock()
			}()
		}()

		// Update the session map with the resumed session
		projectMu.Lock()
		projectSessions[projectID] = resumeCS
		projectMu.Unlock()
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

	if cs == nil {
		return map[string]any{"running": false}
	}
	if cs.alive() {
		return map[string]any{
			"running":    true,
			"id":         cs.id,
			"pid":        cs.cmd.Process.Pid,
			"session_id": cs.sessionID,
		}
	}
	// Idle session — has sessionID for resume
	return map[string]any{
		"running":    false,
		"idle":       true,
		"id":         cs.id,
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

	// Buffer lines and flush in blocks by kind (tool vs text).
	var buf []string
	var bufKind string

	flush := func() {
		if len(buf) == 0 {
			return
		}
		text := strings.Join(buf, "\n")
		buf = nil
		bufKind = ""

		// Write to log file as JSONL — one entry per block.
		if cs.logFile != nil {
			ts := time.Now().UTC().Format(time.RFC3339)
			entry := struct {
				Ts   string `json:"ts"`
				Type string `json:"type"`
				Body string `json:"body"`
			}{ts, source, text}
			b, _ := json.Marshal(entry)
			fmt.Fprintf(cs.logFile, "%s\n", b)
		}

		cs.hub.BroadcastMessage(channel, map[string]any{
			"type":   source,
			"id":     cs.id,
			"text":   text,
			"ts":     time.Now().UTC().Format(time.RFC3339),
		})
	}

	for sc.Scan() {
		line := sc.Text()
		// Try to capture session ID from hermes output.
		// Hermes outputs: "hermes --resume SESSION_ID -p hw_agent_project"
		// and: "Session:   SESSION_ID"
		if strings.Contains(line, "--resume") || strings.Contains(line, "Session:") || strings.Contains(line, "session_id") {
			cs.extractSessionID(line)
		}

		trimmed := strings.TrimLeft(line, " 	")
		var kind string
		if trimmed == "" {
			kind = bufKind
		} else if strings.HasPrefix(trimmed, "┊") || strings.HasPrefix(trimmed, "🔧") ||
			strings.HasPrefix(trimmed, "💻") || strings.HasPrefix(trimmed, "$") ||
			strings.HasPrefix(trimmed, "───") || strings.HasPrefix(trimmed, "Initializing") {
			kind = "tool"
		} else {
			kind = "text"
		}

		if bufKind != "" && kind != "" && kind != bufKind {
			flush()
		}
		if kind != "" {
			bufKind = kind
		}
		buf = append(buf, line)
	}
	flush()
}

func (cs *ProjectSession) extractSessionID(line string) {
	// Hermes outputs: "hermes --resume 20260627_223615_be0df9 -p hw_agent_project"
	// Also tries: "Session: 20260627_223615_be0df9"
	if strings.Contains(line, "--resume") {
		parts := strings.Fields(line)
		for i, p := range parts {
			if p == "--resume" && i+1 < len(parts) {
				candidate := strings.Trim(parts[i+1], ",;:\"'")
				if len(candidate) > 8 {
					cs.mu.Lock()
					cs.sessionID = candidate
					cs.mu.Unlock()
					slog.Info("project agent session id captured", "session", candidate, "project", cs.projectID)
					return
				}
			}
		}
	}
	// Also try "Session: XXX" format
	if strings.Contains(line, "Session:") {
		parts := strings.Fields(line)
		for i, p := range parts {
			if p == "Session:" && i+1 < len(parts) {
				candidate := strings.Trim(parts[i+1], ",;:\"'")
				if len(candidate) > 8 {
					cs.mu.Lock()
					cs.sessionID = candidate
					cs.mu.Unlock()
					slog.Info("project agent session id captured", "session", candidate, "project", cs.projectID)
					return
				}
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
	case "history":
		// GET /api/v1/project-agent/{id}/history — returns session log as JSONL
		logDir := filepath.Join(os.Getenv("HOME"), ".control-room", "project_agent_logs")
		logPath := filepath.Join(logDir, projectID+".log")
		data, err := os.ReadFile(logPath)
		if err != nil {
			jsonResponse(w, map[string]any{"messages": []any{}})
			return
		}
		var messages []map[string]any
		for _, line := range strings.Split(string(data), "\n") {
			if line == "" || !strings.HasPrefix(line, "{") {
				continue
			}
			var entry struct {
				Ts   string `json:"ts"`
				Type string `json:"type"`
				Body string `json:"body"`
			}
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				continue
			}
			role := "agent"
			if entry.Type == "error" {
				role = "system"
			}
			messages = append(messages, map[string]any{
				"role":      role,
				"body":      entry.Body,
				"timestamp": entry.Ts,
				"type":      entry.Type,
			})
		}
		jsonResponse(w, map[string]any{"messages": messages})
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
			if len(compiled.Evidence) > 0 {
				fullPrompt += fmt.Sprintf("\n### Evidence (%d)\n", len(compiled.Evidence))
				for _, e := range compiled.Evidence {
					fullPrompt += "- " + e + "\n"
				}
			}
			if len(compiled.RAGChunks) > 0 {
				fullPrompt += fmt.Sprintf("\n### Relevant docs (RAG, %d chunks)\n", len(compiled.RAGChunks))
				for _, c := range compiled.RAGChunks {
					// Truncate each chunk to avoid oversized prompt
					if len(c) > 500 {
						fullPrompt += c[:500] + "...\n"
					} else {
						fullPrompt += c + "\n"
					}
				}
			}
			if len(compiled.Knowledge) > 0 {
				fullPrompt += fmt.Sprintf("\n### Knowledge (%d)\n", len(compiled.Knowledge))
				for _, k := range compiled.Knowledge {
					fullPrompt += k + "\n"
				}
			}
			if len(compiled.Beliefs) > 0 {
				fullPrompt += fmt.Sprintf("\n### Current beliefs (%d)\n", len(compiled.Beliefs))
				for _, b := range compiled.Beliefs {
					fullPrompt += b + "\n"
				}
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
		// No user_message echo — frontend already added it locally via appendToStream
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