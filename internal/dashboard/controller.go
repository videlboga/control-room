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

// ControllerSession manages a running hermes controller agent process.
type ControllerSession struct {
	mu           sync.Mutex
	id           string
	cmd          *exec.Cmd
	stdout       io.Reader
	stderr       io.Reader
	sessionID    string // hermes session id for --resume
	hub          *Hub
	done         chan struct{}
	logFile      *os.File  // session log file for persistence
	sessionReader *SessionReader // polls state.db for structured messages
}

var (
	controllerMu      sync.Mutex
	controllerSession *ControllerSession
)

// LaunchControllerWithPrompt starts a hermes controller agent with a pre-built prompt.
// Use this when the caller has already compiled context into the prompt.
func LaunchControllerWithPrompt(hub *Hub, epicID, fullPrompt, workspace string) (*ControllerSession, error) {
	controllerMu.Lock()
	defer controllerMu.Unlock()

	if controllerSession != nil && controllerSession.alive() {
		return nil, fmt.Errorf("controller already running (session %s)", controllerSession.id)
	}

	hermesPath := "/home/cyberkitty/.local/bin/hermes"
	if _, err := os.Stat(hermesPath); err != nil {
		return nil, fmt.Errorf("hermes binary not found: %w", err)
	}

	// Use --pass-session-id so we can resume later.
	cmd := exec.Command(hermesPath,
		"--profile", "hw_agent_controller",
		"chat", "-q", fullPrompt,
		"--toolsets", "terminal,file,web",
		"--yolo", "--source", "tool",
		"--max-turns", "100",
		"--pass-session-id",
	)
	cmd.Dir = "/home/cyberkitty/Projects/control-room"

	return startControllerSession(hub, cmd, epicID)
}

// startControllerSession creates pipes, starts the cmd, and sets up streaming.
// Shared by LaunchController and LaunchControllerWithPrompt.
func startControllerSession(hub *Hub, cmd *exec.Cmd, epicID string) (*ControllerSession, error) {
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

	// Open a log file for this session — persists controller output across page refreshes.
	logDir := filepath.Join(os.Getenv("HOME"), ".control-room", "controller_logs")
	_ = os.MkdirAll(logDir, 0o755)
	logPath := filepath.Join(logDir, fmt.Sprintf("controller_%d.log", time.Now().Unix()))
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		slog.Warn("controller log file", "err", err)
	}

	cs := &ControllerSession{
		id:        fmt.Sprintf("ctrl_%d", time.Now().Unix()),
		cmd:       cmd,
		stdout:    stdoutPipe,
		stderr:    stderrPipe,
		hub:       hub,
		done:      make(chan struct{}),
		logFile:   logFile,
	}

	// Broadcast launch event.
	hub.BroadcastMessage("controller", map[string]any{
		"type":    "started",
		"id":      cs.id,
		"epic_id": epicID,
		"pid":     cmd.Process.Pid,
	})

	// Stream stdout to WS channel "controller" (for session ID extraction + fallback).
	go cs.streamOutput(stdoutPipe, "output", epicID)
	// Stream stderr to WS channel "controller" (for errors/debug).
	go cs.streamOutput(stderrPipe, "error", epicID)

	// Start session reader — auto-detects latest session from state.db.
	// Polls every 500ms for new structured messages.
	reader, err := NewSessionReader("hw_agent_controller", "controller", hub)
	if err == nil {
		reader.Start("") // empty = auto-detect latest session
		cs.mu.Lock()
		cs.sessionReader = reader
		cs.mu.Unlock()
		slog.Info("controller session reader started (auto-detect)")
	}

	// Wait for process to finish.
	go func() {
		err := cmd.Wait()
		close(cs.done)
		if cs.logFile != nil {
			_ = cs.logFile.Close()
		}
		// Stop session reader
		cs.mu.Lock()
		if cs.sessionReader != nil {
			cs.sessionReader.Stop()
			cs.sessionReader = nil
		}
		cs.mu.Unlock()
		hub.BroadcastMessage("controller", map[string]any{
			"type":  "ended",
			"id":    cs.id,
			"exit":  cmd.ProcessState.ExitCode(),
			"error": fmt.Sprintf("%v", err),
		})
		controllerMu.Lock()
		if controllerSession == cs {
			controllerSession = nil
		}
		controllerMu.Unlock()
	}()

	controllerSession = cs
	return cs, nil
}

// LaunchController starts a hermes controller agent and streams output to the hub.
// epicID may be empty — when empty, the prompt is used directly as the instruction.
func LaunchController(hub *Hub, epicID, prompt, workspace string) (*ControllerSession, error) {
	controllerMu.Lock()
	defer controllerMu.Unlock()

	if controllerSession != nil && controllerSession.alive() {
		return nil, fmt.Errorf("controller already running (session %s)", controllerSession.id)
	}

	hermesPath := "/home/cyberkitty/.local/bin/hermes"
	if _, err := os.Stat(hermesPath); err != nil {
		return nil, fmt.Errorf("hermes binary not found: %w", err)
	}

	var fullPrompt string
	if epicID != "" {
		fullPrompt = fmt.Sprintf("Manage Control Room epic %s.", epicID)
		if prompt != "" {
			fullPrompt += " " + prompt
		}
	} else {
		// No epic — use the prompt as the direct instruction.
		fullPrompt = prompt
	}
	fullPrompt += " Check task statuses with cr CLI at /home/cyberkitty/Projects/control-room/cr. Reopen failed tasks, restart orchestrator, clean up zombies. Report what you did."

	// Use --pass-session-id so we can resume later.
	cmd := exec.Command(hermesPath,
		"--profile", "hw_agent_controller",
		"chat", "-q", fullPrompt,
		"--toolsets", "terminal,file,web",
		"--yolo", "--source", "tool",
		"--max-turns", "100",
		"--pass-session-id",
	)
	cmd.Dir = "/home/cyberkitty/Projects/control-room"

	return startControllerSession(hub, cmd, epicID)
}

// SendToController sends a follow-up message to the controller agent via --resume.
func SendToController(hub *Hub, message string) error {
	controllerMu.Lock()
	cs := controllerSession
	controllerMu.Unlock()

	if cs == nil || !cs.alive() {
		// Start a new session with just this message.
		return fmt.Errorf("no controller session running")
	}

	// Launch a follow-up hermes --resume with the message.
	// This runs in the same session context.
	go func() {
		hermesPath := "/home/cyberkitty/.local/bin/hermes"
		cmd := exec.Command(hermesPath,
			"--profile", "hw_agent_controller",
			"--resume", cs.sessionID,
			"chat", "-q", message,
			"--toolsets", "terminal,file,web",
			"--yolo", "--source", "tool",
			"--max-turns", "50",
		)
		cmd.Dir = "/home/cyberkitty/Projects/control-room"
		out, _ := cmd.CombinedOutput()
		hub.BroadcastMessage("controller", map[string]any{
			"type":   "output",
			"id":     cs.id,
			"source": "user_followup",
			"text":   string(out),
		})
	}()

	return nil
}

func (cs *ControllerSession) alive() bool {
	if cs.cmd == nil || cs.cmd.ProcessState != nil {
		return false
	}
	return true
}

func (cs *ControllerSession) streamOutput(r io.Reader, source, epicID string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	// Buffer lines and flush in blocks. Lines of the same "kind" (┊ vs non-┊)
	// are accumulated and sent as a single WS message with joined text.
	// This prevents one agent response from becoming 20 separate messages.
	var buf []string
	var bufKind string // "tool" (┊/🔧/$/───) or "text" (everything else)

	flush := func() {
		if len(buf) == 0 {
			return
		}
		text := strings.Join(buf, "\n")
		buf = nil
		bufKind = ""

		// Write to log file as JSONL — one entry per block (not per line).
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

		cs.hub.BroadcastMessage("controller", map[string]any{
			"type":    source,
			"id":      cs.id,
			"epic_id": epicID,
			"text":    text,
			"ts":      time.Now().UTC().Format(time.RFC3339),
		})
	}

	for sc.Scan() {
		line := sc.Text()

		// Try to capture session ID from hermes output.
		if strings.Contains(line, "--resume") || strings.Contains(line, "Session:") || strings.Contains(line, "session_id") {
			cs.extractSessionID(line)
		}

		// Determine line kind
		trimmed := strings.TrimLeft(line, " 	")
		var kind string
		if trimmed == "" {
			kind = bufKind // empty line inherits current buffer kind
		} else if strings.HasPrefix(trimmed, "┊") || strings.HasPrefix(trimmed, "🔧") ||
			strings.HasPrefix(trimmed, "💻") || strings.HasPrefix(trimmed, "$") ||
			strings.HasPrefix(trimmed, "───") || strings.HasPrefix(trimmed, "Initializing") {
			kind = "tool"
		} else {
			kind = "text"
		}

		// Flush if kind changed (and buffer is non-empty and non-empty-kind)
		if bufKind != "" && kind != "" && kind != bufKind {
			flush()
		}

		if kind != "" {
			bufKind = kind
		}
		buf = append(buf, line)
	}
	// Flush remaining buffer.
	flush()
}

func (cs *ControllerSession) extractSessionID(line string) {
	// Hermes outputs: "hermes --resume 20260627_223615_be0df9 -p hw_agent_controller"
	if strings.Contains(line, "--resume") {
		parts := strings.Fields(line)
		for i, p := range parts {
			if p == "--resume" && i+1 < len(parts) {
				candidate := strings.Trim(parts[i+1], ",;:\"'")
				if len(candidate) > 8 {
					cs.mu.Lock()
					cs.sessionID = candidate
					cs.mu.Unlock()
					slog.Info("controller session id captured", "id", candidate)
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
					slog.Info("controller session id captured", "id", candidate)
					return
				}
			}
		}
	}
}

// apiControllerHistory returns the most recent controller session log.
// GET /api/v1/controller/history
// Reads the latest controller_*.log file and returns its lines as messages.
func (s *Server) apiControllerHistory(w http.ResponseWriter, r *http.Request) {
	logDir := filepath.Join(os.Getenv("HOME"), ".control-room", "controller_logs")
	entries, err := os.ReadDir(logDir)
	if err != nil || len(entries) == 0 {
		jsonResponse(w, map[string]any{"messages": []any{}})
		return
	}

	// Find the latest log file
	var latest os.DirEntry
	var latestName string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "controller_") && strings.HasSuffix(e.Name(), ".log") {
			if latest == nil || e.Name() > latestName {
				latest = e
				latestName = e.Name()
			}
		}
	}
	if latest == nil {
		jsonResponse(w, map[string]any{"messages": []any{}})
		return
	}

	// Read the log file
	path := filepath.Join(logDir, latestName)
	data, err := os.ReadFile(path)
	if err != nil {
		jsonError(w, "failed to read controller log: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Parse JSONL — each line is a JSON object with ts, type, body
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

	jsonResponse(w, map[string]any{
		"messages":  messages,
		"log_file":  latestName,
	})
}

// StopController kills the running controller session.
func StopController() error {
	controllerMu.Lock()
	defer controllerMu.Unlock()
	if controllerSession == nil {
		return fmt.Errorf("no controller running")
	}
	if controllerSession.cmd != nil && controllerSession.cmd.Process != nil {
		controllerSession.cmd.Process.Kill()
	}
	controllerSession = nil
	return nil
}

// ControllerStatus returns the current controller session state.
func ControllerStatus() map[string]any {
	controllerMu.Lock()
	defer controllerMu.Unlock()
	if controllerSession == nil {
		return map[string]any{"running": false}
	}
	return map[string]any{
		"running":     true,
		"id":          controllerSession.id,
		"session_id":  controllerSession.sessionID,
		"pid":         controllerSession.cmd.Process.Pid,
	}
}

// LoadControllerHistory reads saved controller chat messages from disk.
func LoadControllerHistory(storeRoot string) ([]map[string]any, error) {
	path := filepath.Join(storeRoot, "controller_chat.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var msgs []map[string]any
	if err := json.Unmarshal(data, &msgs); err != nil {
		return nil, err
	}
	return msgs, nil
}