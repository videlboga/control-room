package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// ─── Memory API ─────────────────────────────────────────────────────────────

// GET  /api/v1/memory/{node_type}/{node_id}?layer=raw&limit=50
// POST /api/v1/memory/{node_type}/{node_id}  { layer, content, source }
// GET  /api/v1/memory/{node_type}/{node_id}/briefing  (latest narrative + policy)

func (s *Server) apiMemory(w http.ResponseWriter, r *http.Request) {
	// Path: /api/v1/memory/{node_type}/{node_id}[/{action}]
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/memory/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		jsonError(w, "path must be /memory/{node_type}/{node_id}", http.StatusBadRequest)
		return
	}
	nodeType := parts[0]
	nodeID := parts[1]
	action := ""
	if len(parts) >= 3 {
		action = parts[2]
	}

	switch {
	case action == "briefing" && r.Method == http.MethodGet:
		s.apiMemoryBriefing(w, r, nodeType, nodeID)
		return
	case action != "" && r.Method == http.MethodGet:
		// e.g. /memory/project/xxx/policy → filter by layer
		s.apiMemoryGet(w, r, nodeType, nodeID, action)
		return
	case r.Method == http.MethodGet:
		s.apiMemoryGet(w, r, nodeType, nodeID, "")
		return
	case r.Method == http.MethodPost:
		s.apiMemoryAdd(w, r, nodeType, nodeID)
		return
	}
	jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) apiMemoryGet(w http.ResponseWriter, r *http.Request, nodeType, nodeID, layer string) {
	q := r.URL.Query()
	if layer == "" {
		layer = q.Get("layer") // optional filter
	}
	limit := 50
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	entries, err := s.db.GetMemory(nodeType, nodeID, layer, limit)
	if err != nil {
		jsonError(w, "memory query: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]any{"entries": entries})
}

func (s *Server) apiMemoryAdd(w http.ResponseWriter, r *http.Request, nodeType, nodeID string) {
	var body struct {
		Layer   string `json:"layer"`
		Content string `json:"content"`
		Source  string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Layer == "" || body.Content == "" {
		jsonError(w, "layer and content are required", http.StatusBadRequest)
		return
	}
	if body.Layer != "raw" && body.Layer != "narrative" && body.Layer != "policy" {
		jsonError(w, "layer must be: raw, narrative, or policy", http.StatusBadRequest)
		return
	}
	entry, err := s.db.AddMemory(nodeType, nodeID, body.Layer, body.Content, body.Source)
	if err != nil {
		jsonError(w, "memory insert: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Broadcast via WebSocket
	if s.hub != nil {
		s.hub.BroadcastMessage("conversation:"+nodeType+":"+nodeID, map[string]any{
			"type": "memory_update",
			"data": entry,
		})
	}

	jsonResponse(w, entry)
}

// apiMemoryBriefing returns the latest narrative + all policy entries for a node.
// This is the "briefing" that agents (project agent, controller) receive as context.
func (s *Server) apiMemoryBriefing(w http.ResponseWriter, r *http.Request, nodeType, nodeID string) {
	narrative, _ := s.db.GetLatestNarrative(nodeType, nodeID)
	policy, _ := s.db.GetPolicy(nodeType, nodeID)
	structured, _ := s.db.GetMemory(nodeType, nodeID, "structured", 10)

	// For project nodes: also include project docs
	var docs string
	if nodeType == "project" {
		p, err := s.store.GetProject(nodeID)
		if err == nil && p != nil {
			docs = p.DocsDir
		}
	}

	jsonResponse(w, map[string]any{
		"node_type":  nodeType,
		"node_id":    nodeID,
		"narrative":  narrative,
		"policy":     policy,
		"structured": structured,
		"docs_dir":   docs,
	})
}