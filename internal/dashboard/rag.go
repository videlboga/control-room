package dashboard

import (
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ─── RAG — cold memory via FTS5 full-text search ───────────────────────────

// InitRAG creates the FTS5 table for document chunks if it doesn't exist.
func (db *DB) InitRAG() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	_, err := db.db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS rag_chunks USING fts5(
		project_id,
		source,
		chunk_idx,
		content,
	 tokenize = 'porter unicode61')`)
	return err
}

// ragChunk represents a searchable document chunk.
type RAGChunk struct {
	ProjectID string `json:"project_id"`
	Source    string `json:"source"`     // file path or "memory"
	ChunkIdx  int    `json:"chunk_idx"`
	Content   string `json:"content"`
}

// IndexProjectDocs reads all docs for a project, splits them into chunks,
// and indexes them in the FTS5 table. Replaces existing chunks for the project.
func (db *DB) IndexProjectDocs(projectID, docsDir string) error {
	// Clear existing chunks for this project
	db.mu.Lock()
	_, err := db.db.Exec("DELETE FROM rag_chunks WHERE project_id = ?", projectID)
	db.mu.Unlock()
	if err != nil {
		return err
	}

	if _, err := os.Stat(docsDir); os.IsNotExist(err) {
		return nil // no docs dir, nothing to index
	}

	// Read all .md and .json files in docs dir
	entries, err := os.ReadDir(docsDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".md" && ext != ".json" && ext != ".txt" && ext != ".py" && ext != ".go" {
			continue
		}

		path := filepath.Join(docsDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		// Split into chunks of ~500 chars, trying to break on paragraph/line boundaries
		text := string(data)
		chunks := splitChunks(text, 500)

		for i, chunk := range chunks {
			db.mu.Lock()
			_, err := db.db.Exec(
				"INSERT INTO rag_chunks (project_id, source, chunk_idx, content) VALUES (?, ?, ?, ?)",
				projectID, entry.Name(), i, chunk,
			)
			db.mu.Unlock()
			if err != nil {
				slog.Warn("RAG index chunk", "err", err)
			}
		}
	}

	return nil
}

// SearchRAG performs FTS5 search for the given query, returning top-K chunks
// for a specific project. If projectID is empty, searches all projects.
func (db *DB) SearchRAG(projectID, query string, limit int) ([]RAGChunk, error) {
	if limit <= 0 {
		limit = 5
	}

	// Build FTS5 query — extract meaningful words, join with OR
	// Remove punctuation, split, filter short words
	query = strings.ToLower(query)
	query = strings.ReplaceAll(query, "(", " ")
	query = strings.ReplaceAll(query, ")", " ")
	query = strings.ReplaceAll(query, "\"", " ")
	query = strings.ReplaceAll(query, "/", " ")
	query = strings.ReplaceAll(query, ":", " ")
	query = strings.ReplaceAll(query, ".", " ")
	query = strings.ReplaceAll(query, ",", " ")
	words := strings.Fields(query)
	if len(words) == 0 {
		return nil, nil
	}
	// Take up to 5 words, filter short ones, wrap each in quotes for FTS5
	var meaningful []string
	for _, w := range words {
		if len(w) >= 3 {
			// Wrap in double quotes for FTS5 string literal — prevents
			// FTS5 from interpreting words as column names
			meaningful = append(meaningful, "\""+w+"\"")
		}
		if len(meaningful) >= 5 {
			break
		}
	}
	if len(meaningful) == 0 {
		return nil, nil
	}
	ftsQuery := strings.Join(meaningful, " OR ")

	var rows *sql.Rows
	var err error
	if projectID != "" {
		rows, err = db.db.Query(
			`SELECT project_id, source, chunk_idx, content FROM rag_chunks
			 WHERE project_id = ? AND rag_chunks MATCH ?
			 ORDER BY rank LIMIT ?`,
			projectID, ftsQuery, limit)
	} else {
		rows, err = db.db.Query(
			`SELECT project_id, source, chunk_idx, content FROM rag_chunks
			 WHERE rag_chunks MATCH ?
			 ORDER BY rank LIMIT ?`,
			ftsQuery, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chunks []RAGChunk
	for rows.Next() {
		var c RAGChunk
		if err := rows.Scan(&c.ProjectID, &c.Source, &c.ChunkIdx, &c.Content); err != nil {
			continue
		}
		chunks = append(chunks, c)
	}
	return chunks, nil
}

// splitChunks splits text into chunks of approximately maxChars, trying to
// break on paragraph or line boundaries.
func splitChunks(text string, maxChars int) []string {
	if len(text) <= maxChars {
		return []string{text}
	}

	var chunks []string
	lines := strings.Split(text, "\n")
	var current strings.Builder

	for _, line := range lines {
		if current.Len()+len(line)+1 > maxChars && current.Len() > 0 {
			chunks = append(chunks, current.String())
			current.Reset()
		}
		current.WriteString(line)
		current.WriteString("\n")
	}

	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}

	return chunks
}

// ─── HTTP Handler ──────────────────────────────────────────────────────────

// apiRAG handles RAG operations:
// GET  /api/v1/rag/{project_id}/search?q=query&limit=5  — search chunks
// POST /api/v1/rag/{project_id}/index   — index project docs
func (s *Server) apiRAG(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/rag/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		jsonError(w, "path must be /rag/{project_id}/{action}", http.StatusBadRequest)
		return
	}
	projectID := parts[0]
	action := parts[1]

	switch action {
	case "search":
		q := r.URL.Query().Get("q")
		if q == "" {
			jsonError(w, "missing q parameter", http.StatusBadRequest)
			return
		}
		limit := 5
		if l := r.URL.Query().Get("limit"); l != "" {
			if n, err := strconv.Atoi(l); err == nil && n > 0 {
				limit = n
			}
		}
		chunks, err := s.db.SearchRAG(projectID, q, limit)
		if err != nil {
			jsonError(w, "search failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]any{
			"project_id": projectID,
			"query":      q,
			"chunks":     chunks,
			"count":      len(chunks),
		})

	case "index":
		// Find docs dir for this project
		p, err := s.store.GetProject(projectID)
		if err != nil {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}
		docsDir := p.DocsDir
		if docsDir == "" {
			docsDir = filepath.Join(s.store.Root, "projects", projectID, "docs")
		}
		// Also index RESEARCH.md and plan.json at project repo root
		researchPath := filepath.Join(p.RepoPath, "RESEARCH.md")
		if _, err := os.Stat(researchPath); err == nil {
			docsDir = p.RepoPath
		}

		if err := s.db.IndexProjectDocs(projectID, docsDir); err != nil {
			jsonError(w, "index failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]any{
			"project_id": projectID,
			"status":     "indexed",
			"docs_dir":   docsDir,
		})

	default:
		jsonError(w, "unknown action: "+action, http.StatusBadRequest)
	}
}