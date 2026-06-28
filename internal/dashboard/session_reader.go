package dashboard

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// ─── Session Reader — reads structured messages from Hermes state.db ────────

// SessionReader polls a Hermes profile's state.db for new messages
// and broadcasts them as structured WS events. Replaces stdout pipe parsing.
type SessionReader struct {
	mu          sync.Mutex
	profile     string // e.g. "hw_agent_controller"
	dbPath      string // path to state.db
	sessionID   string // current hermes session ID
	lastMsgID   int    // last message ID read
	hub         *Hub
	channel     string // WS channel to broadcast on
	stop        chan struct{}
}

// SessionMessage is a structured message from Hermes session DB.
type SessionMessage struct {
	ID         int                    `json:"id"`
	Role       string                 `json:"role"`        // user, assistant, tool
	Content    string                 `json:"content"`     // text content
	ToolName   string                 `json:"tool_name"`   // which tool was called
	ToolCallID string                 `json:"tool_call_id"`
	ToolCalls  []SessionToolCall      `json:"tool_calls"`  // when role=assistant calling tools
	Timestamp  string                 `json:"timestamp"`
}

// SessionToolCall is a single tool call within an assistant message.
type SessionToolCall struct {
	ID       string                 `json:"id"`
	Function SessionToolFunction    `json:"function"`
}

// SessionToolFunction contains the tool name and arguments.
type SessionToolFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// NewSessionReader creates a reader for a Hermes profile.
func NewSessionReader(profile, channel string, hub *Hub) (*SessionReader, error) {
	dbPath := filepath.Join(os.Getenv("HOME"), ".hermes", "profiles", profile, "state.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("state.db not found for profile %s: %w", profile, err)
	}

	return &SessionReader{
		profile: profile,
		dbPath:  dbPath,
		hub:     hub,
		channel: channel,
		stop:    make(chan struct{}),
	}, nil
}

// Start begins polling the session DB for new messages.
// If sessionID is empty, polls for the latest session (auto-detect).
func (sr *SessionReader) Start(sessionID string) {
	sr.mu.Lock()
	sr.sessionID = sessionID
	sr.lastMsgID = 0
	sr.mu.Unlock()

	go sr.poll()
	slog.Info("session reader started", "profile", sr.profile, "session", sessionID)
}

// Stop stops polling.
func (sr *SessionReader) Stop() {
	select {
	case <-sr.stop:
	default:
		close(sr.stop)
	}
}

// poll reads new messages every 500ms and broadcasts them.
func (sr *SessionReader) poll() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-sr.stop:
			return
		case <-ticker.C:
			// If no session ID, try to auto-detect latest
			sr.mu.Lock()
			sessionID := sr.sessionID
			sr.mu.Unlock()
			if sessionID == "" {
				// Auto-detect: check if a new session appeared in state.db
				latest, err := GetLatestSessionID(sr.profile)
				if err == nil && latest != "" {
					sr.mu.Lock()
					sr.sessionID = latest
					sr.mu.Unlock()
					sessionID = latest
					slog.Info("session reader auto-detected", "session", latest)
				}
			}
			if sessionID != "" {
				sr.readNewMessages()
			}
		}
	}
}

// readNewMessages reads messages newer than lastMsgID and broadcasts them.
func (sr *SessionReader) readNewMessages() {
	sr.mu.Lock()
	sessionID := sr.sessionID
	lastID := sr.lastMsgID
	sr.mu.Unlock()

	if sessionID == "" {
		return // no session yet
	}

	db, err := sql.Open("sqlite", sr.dbPath)
	if err != nil {
		slog.Warn("session reader open", "err", err)
		return
	}
	defer db.Close()

	rows, err := db.Query(
		`SELECT id, role, content, tool_calls, tool_name, tool_call_id, timestamp
		 FROM messages
		 WHERE session_id = ? AND id > ?
		 ORDER BY id ASC`,
		sessionID, lastID,
	)
	if err != nil {
		slog.Warn("session reader query", "err", err)
		return
	}
	defer rows.Close()

	var maxID int = lastID
	for rows.Next() {
		var msg SessionMessage
		var toolCallsJSON, content sql.NullString
		var toolName, toolCallID sql.NullString

		if err := rows.Scan(&msg.ID, &msg.Role, &content, &toolCallsJSON, &toolName, &toolCallID, &msg.Timestamp); err != nil {
			continue
		}

		msg.Content = content.String
		msg.ToolName = toolName.String
		msg.ToolCallID = toolCallID.String

		// Parse tool_calls if present
		if toolCallsJSON.Valid && toolCallsJSON.String != "" {
			json.Unmarshal([]byte(toolCallsJSON.String), &msg.ToolCalls)
		}

		if msg.ID > maxID {
			maxID = msg.ID
		}

		// Broadcast structured message via WS
		sr.hub.BroadcastMessage(sr.channel, map[string]any{
			"type":       "session_message",
			"id":         msg.ID,
			"role":       msg.Role,
			"content":    msg.Content,
			"tool_name":  msg.ToolName,
			"tool_calls": msg.ToolCalls,
			"timestamp":  msg.Timestamp,
		})
	}

	sr.mu.Lock()
	sr.lastMsgID = maxID
	sr.mu.Unlock()
}

// GetLatestSessionID finds the most recent session ID for a profile.
func GetLatestSessionID(profile string) (string, error) {
	dbPath := filepath.Join(os.Getenv("HOME"), ".hermes", "profiles", profile, "state.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return "", err
	}
	defer db.Close()

	var sessionID string
	err = db.QueryRow("SELECT id FROM sessions ORDER BY id DESC LIMIT 1").Scan(&sessionID)
	if err != nil {
		return "", err
	}
	return sessionID, nil
}

// GetSessionMessages reads all messages for a session from state.db.
// Used for loading history on page refresh.
func GetSessionMessages(profile, sessionID string, limit int) ([]SessionMessage, error) {
	dbPath := filepath.Join(os.Getenv("HOME"), ".hermes", "profiles", profile, "state.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	if limit <= 0 {
		limit = 100
	}

	rows, err := db.Query(
		`SELECT id, role, content, tool_calls, tool_name, tool_call_id, timestamp
		 FROM messages
		 WHERE session_id = ?
		 ORDER BY id ASC
		 LIMIT ?`,
		sessionID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []SessionMessage
	for rows.Next() {
		var msg SessionMessage
		var toolCallsJSON, content sql.NullString
		var toolName, toolCallID sql.NullString

		if err := rows.Scan(&msg.ID, &msg.Role, &content, &toolCallsJSON, &toolName, &toolCallID, &msg.Timestamp); err != nil {
			continue
		}

		msg.Content = content.String
		msg.ToolName = toolName.String
		msg.ToolCallID = toolCallID.String

		if toolCallsJSON.Valid && toolCallsJSON.String != "" {
			json.Unmarshal([]byte(toolCallsJSON.String), &msg.ToolCalls)
		}

		messages = append(messages, msg)
	}

	return messages, nil
}

// ─── HTTP Handler ──────────────────────────────────────────────────────────

// apiSession handles session history requests:
// GET /api/v1/session/{profile}/{session_id}  — get messages
// GET /api/v1/session/{profile}/latest        — get latest session messages
func (s *Server) apiSession(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/session/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		jsonError(w, "path must be /session/{profile}/{session_id_or_latest}", http.StatusBadRequest)
		return
	}
	profile := parts[0]
	sessionRef := parts[1]

	sessionID := sessionRef
	if sessionRef == "latest" {
		var err error
		sessionID, err = GetLatestSessionID(profile)
		if err != nil {
			jsonError(w, "no sessions found: "+err.Error(), http.StatusNotFound)
			return
		}
	}

	limit := 200
	msgs, err := GetSessionMessages(profile, sessionID, limit)
	if err != nil {
		jsonError(w, "failed to read messages: "+err.Error(), http.StatusInternalServerError)
		return
	}

	jsonResponse(w, map[string]any{
		"profile":    profile,
		"session_id": sessionID,
		"messages":   msgs,
		"count":      len(msgs),
	})
}