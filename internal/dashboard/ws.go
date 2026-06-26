package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

// WSMessage is the JSON envelope broadcast over WebSocket channels.
type WSMessage struct {
	Channel string          `json:"channel"`
	Type    string          `json:"type"`
	Data    json.RawMessage `json:"data"`
	At      time.Time       `json:"at"`
}

// client represents a single WebSocket subscriber.
type client struct {
	conn    *websocket.Conn
	subs    map[string]bool // subscribed channels
	send    chan []byte      // buffered outbound queue
	done    chan struct{}
}

// Hub maintains the set of active WebSocket clients and routes broadcast
// Hub manages WebSocket clients and broadcast subscriptions.
type Hub struct {
	mu      sync.RWMutex
	clients map[*client]bool
	db      *DB // optional, for sending initial snapshots
	store   *DashboardStore
}

// NewHub creates a Hub. db and store may be nil.
func NewHub(db *DB, ds *DashboardStore) *Hub {
	return &Hub{clients: map[*client]bool{}, db: db, store: ds}
}

// subscribe registers a client. It is safe to call concurrently.
func (h *Hub) subscribe(c *client) {
	h.mu.Lock()
	h.clients[c] = true
	h.mu.Unlock()
}

// unsubscribe removes a client and closes its send channel.
func (h *Hub) unsubscribe(c *client) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
	h.mu.Unlock()
}

// BroadcastMessage marshals msg and sends it to every client subscribed to
// channel. Slow/dropped clients are skipped so one lagging browser can't block
// the hub.
func (h *Hub) BroadcastMessage(channel string, msg any) {
	data, err := json.Marshal(msg)
	if err != nil {
		slog.Warn("ws broadcast marshal", "channel", channel, "err", err)
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if !c.subs[channel] {
			continue
		}
		select {
		case c.send <- data:
		default:
			// client buffer full — drop this message rather than block.
		}
	}
}

// HandleWS upgrades the HTTP connection to a WebSocket, registers the client
// with the hub, and runs the read/write pumps until the connection closes.
//
// Client -> server messages are JSON objects of the form:
//   {"action":"subscribe","channel":"runs"}
//   {"action":"unsubscribe","channel":"runs"}
// The server does not currently send unsolicited messages to the client other
// than broadcasts the client has subscribed to.
func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// The SPA is served from the same origin, but we allow all origins so
		// local dev (Vite on :5173) works without extra config.
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		slog.Warn("ws accept", "err", err)
		return
	}
	// A generous read limit; broadcast payloads are small but event payloads
	// can include tool output.
	conn.SetReadLimit(1 << 20) // 1 MiB

	c := &client{
		conn: conn,
		subs: map[string]bool{"board": true}, // default subscription
		send: make(chan []byte, 64),
		done: make(chan struct{}),
	}
	h.subscribe(c)

	// Send initial snapshot with active runs, task stats, and recent log lines.
	if h.db != nil {
		active, _ := h.db.GetActiveRuns()
		stats, _ := h.db.GetTaskStats()
		// Enrich each run with tool_use_count from DB and recent log lines.
		logs := map[string][]string{}
		for i := range active {
			tc, _ := h.db.CountRunToolUse(active[i].ID)
			active[i].ToolUseCount = tc
			// Read last 5 lines from activity.log
			if h.store != nil {
				logPath := filepath.Join(h.store.Root, "runs", active[i].ID, "activity.log")
				if lines := readLastLines(logPath, 5); len(lines) > 0 {
					logs[active[i].ID] = lines
				}
			}
		}
		snapshot := map[string]any{
			"type": "snapshot",
			"data": map[string]any{
				"active_runs":  active,
				"task_stats":   stats,
				"initial_logs": logs,
			},
		}
		if data, err := json.Marshal(snapshot); err == nil {
			select {
			case c.send <- data:
			default:
			}
		}
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Write pump.
	go func() {
		defer close(c.done)
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-c.send:
				if !ok {
					return
				}
				wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
				err := conn.Write(wctx, websocket.MessageText, msg)
				wcancel()
				if err != nil {
					return
				}
			}
		}
	}()

	// Read pump: process subscribe/unsubscribe until the socket closes.
	for {
		_, payload, err := conn.Read(ctx)
		if err != nil {
			break
		}
		var cmd struct {
			Action  string `json:"action"`
			Channel string `json:"channel"`
		}
		if err := json.Unmarshal(payload, &cmd); err != nil {
			continue
		}
		switch cmd.Action {
		case "subscribe":
			if cmd.Channel != "" {
				c.subs[cmd.Channel] = true
			}
		case "unsubscribe":
			if cmd.Channel != "" {
				delete(c.subs, cmd.Channel)
			}
		}
	}
	h.unsubscribe(c)
	_ = conn.Close(websocket.StatusNormalClosure, "")
	<-c.done
}

// readLastLines reads the last n lines from a file.
func readLastLines(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var all []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		all = append(all, sc.Text())
	}
	if len(all) <= n {
		return all
	}
	return all[len(all)-n:]
}