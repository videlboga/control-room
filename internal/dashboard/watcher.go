package dashboard

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"control-room/internal/run"
)

// Watcher monitors the control-room filesystem store for changes to JSON files
// and activity logs, updates the SQLite mirror, and broadcasts WebSocket
// notifications to the relevant channels.
type Watcher struct {
	db      *DB
	store   *DashboardStore
	hub     *Hub
	w       *fsnotify.Watcher
	root    string

	// logOffsets remembers how many bytes of activity.log we have already
	// broadcast for each run, so we only tail new lines.
	logOffsets map[string]int64
	mu         sync.Mutex

	stop chan struct{}
}

// NewWatcher creates (but does not start) a watcher. Call Start to begin
// watching and Close to stop.
func NewWatcher(db *DB, ds *DashboardStore, hub *Hub) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &Watcher{
		db:         db,
		store:      ds,
		hub:        hub,
		w:          fw,
		root:       ds.Root,
		logOffsets: map[string]int64{},
		stop:       make(chan struct{}),
	}, nil
}

// Start adds the watched directories and launches the event loop goroutine.
// Missing directories are created first so a fresh workspace still works.
func (w *Watcher) Start() error {
	dirs := []string{"tasks", "runs", "epics", "projects", "teams", "comments"}
	for _, d := range dirs {
		p := filepath.Join(w.root, d)
		_ = os.MkdirAll(p, 0o755)
		if err := w.w.Add(p); err != nil {
			slog.Warn("watcher add dir", "dir", p, "err", err)
		}
	}
	// Also watch comments subdirectories (workspace, project, task, run, epic)
	w.watchCommentDirs()
	// Also watch the runs subdirectories for activity.log changes.
	w.watchRunDirs()
	go w.loop()
	return nil
}

func (w *Watcher) watchRunDirs() {
	runsDir := filepath.Join(w.root, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(runsDir, e.Name())
		if err := w.w.Add(p); err != nil {
			slog.Warn("watcher add run dir", "dir", p, "err", err)
		}
	}
}

// watchCommentDirs adds all comment subdirectories to the fsnotify watcher.
// Comments are stored as comments/{kind}/{entityID}.jsonl — we watch each
// kind subdirectory so new comments trigger a conversation WS broadcast.
func (w *Watcher) watchCommentDirs() {
	commentsDir := filepath.Join(w.root, "comments")
	entries, err := os.ReadDir(commentsDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(commentsDir, e.Name())
		if err := w.w.Add(p); err != nil {
			slog.Warn("watcher add comment dir", "dir", p, "err", err)
		}
	}
}

// Close stops the watcher and closes the fsnotify watcher.
func (w *Watcher) Close() error {
	close(w.stop)
	return w.w.Close()
}

func (w *Watcher) loop() {
	// Debounce coalescing: multiple writes to the same file in quick
	// succession are collapsed into one DB update.
	debounce := map[string]*time.Timer{}
	var dmu sync.Mutex

	for {
		select {
		case <-w.stop:
			return
		case ev, ok := <-w.w.Events:
			if !ok {
				return
			}
			if ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
				continue
			}
			// Debounce: schedule the path to be processed after 150ms of quiet.
			path := ev.Name
			dmu.Lock()
			if t, ok := debounce[path]; ok {
				t.Reset(150 * time.Millisecond)
				dmu.Unlock()
				continue
			}
			t := time.AfterFunc(150*time.Millisecond, func() {
				dmu.Lock()
				delete(debounce, path)
				dmu.Unlock()
				w.handle(path)
			})
			debounce[path] = t
			dmu.Unlock()
		case err, ok := <-w.w.Errors:
			if !ok {
				return
			}
			slog.Warn("watcher error", "err", err)
		}
	}
}

// handle processes a single changed file path.
func (w *Watcher) handle(path string) {
	rel, err := filepath.Rel(w.root, path)
	if err != nil {
		return
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) < 2 {
		return
	}
	base := filepath.Base(path)

	switch parts[0] {
	case "tasks":
		if !strings.HasSuffix(base, ".json") {
			return
		}
		if err := w.db.SyncTaskFile(w.store, base); err != nil {
			slog.Warn("watcher sync task", "file", base, "err", err)
			return
		}
		w.hub.BroadcastMessage("board", WSMessage{Channel: "board", Type: "task_update", At: time.Now()})
		w.hub.BroadcastMessage("tree", map[string]any{"type": "tree_update", "data": map[string]any{"node_type": "task"}})
	case "epics":
		if !strings.HasSuffix(base, ".json") {
			return
		}
		if err := w.db.SyncEpicFile(w.store, base); err != nil {
			slog.Warn("watcher sync epic", "file", base, "err", err)
			return
		}
		w.hub.BroadcastMessage("board", WSMessage{Channel: "board", Type: "epic_update", At: time.Now()})
	case "projects":
		if !strings.HasSuffix(base, ".json") {
			return
		}
		if err := w.db.SyncProjectFile(w.store, base); err != nil {
			slog.Warn("watcher sync project", "file", base, "err", err)
			return
		}
		w.hub.BroadcastMessage("board", WSMessage{Channel: "board", Type: "project_update", At: time.Now()})
		w.hub.BroadcastMessage("tree", map[string]any{"type": "tree_update", "data": map[string]any{"node_type": "project"}})
	case "teams":
		if !strings.HasSuffix(base, ".json") {
			return
		}
		if err := w.db.SyncTeamFile(w.store, base); err != nil {
			slog.Warn("watcher sync team", "file", base, "err", err)
			return
		}
		w.hub.BroadcastMessage("board", WSMessage{Channel: "board", Type: "team_update", At: time.Now()})
	case "comments":
		// Comments are stored as comments/{kind}/{entityID}.jsonl
		// path = .../comments/{kind}/{entityID}.jsonl
		// parts = ["comments", kind, "entityID.jsonl"]
		if len(parts) < 3 {
			// A new comment kind directory appeared — watch it.
			if isDir(path) {
				_ = w.w.Add(path)
			}
			return
		}
		kind := parts[1]
		entityID := strings.TrimSuffix(base, ".jsonl")
		// Broadcast to the conversation channel so subscribed clients update.
		w.hub.BroadcastMessage("conversation:"+kind+":"+entityID, map[string]any{
			"type": "new_comment",
			"data": map[string]any{
				"entity_kind": kind,
				"entity_id":   entityID,
			},
		})
	case "runs":
		if len(parts) < 3 {
			// A new run directory may have appeared — watch it.
			if isDir(path) {
				_ = w.w.Add(path)
				w.watchRunDirs()
			}
			return
		}
		runID := parts[1]
		switch base {
		case "run.json":
			if err := w.db.SyncRunFile(w.store, runID); err != nil {
				slog.Warn("watcher sync run", "run", runID, "err", err)
				return
			}
			w.hub.BroadcastMessage("runs", WSMessage{Channel: "runs", Type: "run_update", At: time.Now()})
			w.hub.BroadcastMessage("tree", map[string]any{"type": "tree_update", "data": map[string]any{"node_type": "run"}})
			rn, err := w.store.GetRun(runID)
			if err == nil {
				w.hub.BroadcastMessage("run:"+runID, WSMessage{Channel: "run:" + runID, Type: "run_update", Data: jsonRaw(rn), At: time.Now()})
			}
		case "events.jsonl":
			// Reload all events for the run and update tool_use_count.
			if err := w.db.SyncRunFile(w.store, runID); err != nil {
				slog.Warn("watcher sync run events", "run", runID, "err", err)
			}
			// Count tool_call events and update the run row.
			tc, _ := w.db.CountRunToolUse(runID)
			if tc >= 0 {
				_ = w.db.UpdateToolUseCount(runID, tc)
			}
			// Broadcast run_update to both runs and run:{id} channels.
			rn, err := w.store.GetRun(runID)
			if err == nil {
				// Build a map with the run data + tool_use_count
				runData := map[string]any{
					"id":             rn.ID,
					"task_id":        rn.TaskID,
					"project_id":     rn.ProjectID,
					"status":         rn.Status,
					"agent":          rn.Agent,
					"step":           rn.Step,
					"started_at":     rn.StartedAt,
					"ended_at":       rn.EndedAt,
					"tool_use_count": tc,
				}
				w.hub.BroadcastMessage("runs", map[string]any{
					"type": "run_update",
					"data": runData,
				})
				w.hub.BroadcastMessage("run:"+runID, map[string]any{
					"type": "run_update",
					"data": runData,
				})

				// Auto-write raw memory when run completes.
				if rn.Status == "done" || rn.Status == "failed" {
					w.writeRawMemory(rn, tc)
				}
			}
		case "activity.log":
			lines := w.tailActivityLog(runID, path)
			for _, line := range lines {
				// Send to both runs (for card) and run:{id} (for detail)
				w.hub.BroadcastMessage("runs", map[string]any{
					"type": "log_line",
					"data": map[string]any{
						"run_id":    runID,
						"line":      line,
						"timestamp": time.Now().UTC().Format(time.RFC3339),
					},
				})
				w.hub.BroadcastMessage("run:"+runID, map[string]any{
					"type": "log_line",
					"data": map[string]any{
						"run_id":    runID,
						"line":      line,
						"timestamp": time.Now().UTC().Format(time.RFC3339),
					},
				})
			}
		}
	}
}

// tailActivityLog reads only the newly-appended bytes of activity.log since the
// last time we tailed it, returning the new lines.
func (w *Watcher) tailActivityLog(runID, path string) []string {
	w.mu.Lock()
	off := w.logOffsets[runID]
	w.mu.Unlock()

	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if info.Size() < off {
		// File was truncated/recreated — start from 0.
		off = 0
	}
	if info.Size() == off {
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if off > 0 {
		if _, err := f.Seek(off, 0); err != nil {
			return nil
		}
	}
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	newOff := off
	if n, err := f.Seek(0, 1); err == nil {
		newOff = n
	} else {
		newOff = info.Size()
	}
	w.mu.Lock()
	w.logOffsets[runID] = newOff
	w.mu.Unlock()
	return lines
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func jsonRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return b
}

// writeProjectMemoryFile writes a memory.md file for a project, combining
// narrative + policy from the SQLite memory_entries. This file is read by
// the orchestrator's buildPrompt to give task agents access to project memory.
func (w *Watcher) writeProjectMemoryFile(projectID string) {
	if w.db == nil {
		return
	}

	narrative, _ := w.db.GetLatestNarrative("project", projectID)
	policies, _ := w.db.GetPolicy("project", projectID)
	evidence, _ := w.db.GetEvidence("project", projectID)

	var b strings.Builder
	if narrative != "" {
		b.WriteString("## Narrative\n")
		b.WriteString(narrative)
		b.WriteString("\n\n")
	}
	if len(policies) > 0 {
		b.WriteString("## Policy\n")
		for _, p := range policies {
			b.WriteString("- ")
			b.WriteString(p.Content)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(evidence) > 0 {
		b.WriteString("## Recent Evidence\n")
		for _, e := range evidence {
			b.WriteString("- ")
			b.WriteString(e.Content)
			b.WriteString("\n")
		}
	}

	memPath := filepath.Join(w.root, "projects", projectID, "memory.md")
	if err := os.WriteFile(memPath, []byte(b.String()), 0o644); err != nil {
		slog.Warn("writeProjectMemoryFile", "project", projectID, "err", err)
	}
}

// writeRawMemory writes a raw memory entry for a completed run.
// This captures the run summary, tool count, and verdict as the base layer
// for the memory pipeline (raw → narrative → briefing).
func (w *Watcher) writeRawMemory(rn *run.Run, toolCount int) {
	if w.db == nil {
		return
	}
	// Build a compact summary of the run
	raw := map[string]any{
		"run_id":         rn.ID,
		"task_id":        rn.TaskID,
		"project_id":     rn.ProjectID,
		"status":         rn.Status,
		"agent":          rn.Agent,
		"step":           rn.Step,
		"summary":        rn.Summary,
		"tool_use_count": toolCount,
		"started_at":     rn.StartedAt,
		"ended_at":       rn.EndedAt,
	}
	content, err := json.Marshal(raw)
	if err != nil {
		slog.Warn("writeRawMemory marshal", "run", rn.ID, "err", err)
		return
	}
	// Write to both task and project nodes
	if _, err := w.db.AddMemory("task", rn.TaskID, "raw", string(content), "system"); err != nil {
		slog.Warn("writeRawMemory task", "run", rn.ID, "err", err)
	}
	// Also write to project node if we can find the project ID
	if rn.ProjectID != "" {
		if _, err := w.db.AddMemory("project", rn.ProjectID, "raw", string(content), "system"); err != nil {
			slog.Warn("writeRawMemory project", "run", rn.ID, "err", err)
		}
	}

	// Write evidence for notable events: failures, merge errors, verdicts
	w.writeEvidence(rn)

	// Update memory.md file for the project so orchestrator's buildPrompt can read it
	if rn.ProjectID != "" {
		w.writeProjectMemoryFile(rn.ProjectID)
		// Trigger belief update — async, don't block watcher
		go w.updateBeliefs(rn.ProjectID, rn.TaskID, rn.Status, rn.Agent, rn.Step, rn.Summary)
	}
}

// writeEvidence writes evidence-layer entries for notable run events.
// Evidence captures specific artifacts: merge errors, failures, verdicts, commits.
func (w *Watcher) writeEvidence(rn *run.Run) {
	if w.db == nil {
		return
	}

	// Evidence for failed runs
	if rn.Status == "failed" {
		ev := map[string]any{
			"type":      "run_failed",
			"run_id":    rn.ID,
			"task_id":   rn.TaskID,
			"agent":     rn.Agent,
			"step":      rn.Step,
			"summary":   rn.Summary,
			"ended_at":  rn.EndedAt,
		}
		if b, err := json.Marshal(ev); err == nil {
			w.db.AddEvidence("task", rn.TaskID, string(b), "system")
			if rn.ProjectID != "" {
				w.db.AddEvidence("project", rn.ProjectID, string(b), "system")
			}
		}
	}

	// Evidence for merge errors — check run metadata
	metaPath := filepath.Join(w.root, "runs", rn.ID, "metadata.json")
	if data, err := os.ReadFile(metaPath); err == nil {
		var meta map[string]any
		if json.Unmarshal(data, &meta) == nil {
			if reason, ok := meta["reason"]; ok {
				reasonStr := fmt.Sprintf("%v", reason)
				if strings.Contains(reasonStr, "merge") || strings.Contains(reasonStr, "rebase") {
					ev := map[string]any{
						"type":       "merge_error",
						"run_id":     rn.ID,
						"task_id":    rn.TaskID,
						"reason":     reasonStr,
						"verdict":    meta["verdict"],
						"ended_at":   rn.EndedAt,
					}
					if b, err := json.Marshal(ev); err == nil {
						w.db.AddEvidence("task", rn.TaskID, string(b), "system")
						if rn.ProjectID != "" {
							w.db.AddEvidence("project", rn.ProjectID, string(b), "system")
						}
					}
				}
			}
			// Evidence for verdicts
			if verdict, ok := meta["verdict"]; ok && verdict == "reject" {
				ev := map[string]any{
					"type":       "verdict_reject",
					"run_id":     rn.ID,
					"task_id":    rn.TaskID,
					"reason":     meta["reason"],
					"agent":      rn.Agent,
					"step":       rn.Step,
				}
				if b, err := json.Marshal(ev); err == nil {
					w.db.AddEvidence("task", rn.TaskID, string(b), "system")
				}
			}
		}
	}
}