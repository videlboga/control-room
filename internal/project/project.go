package project

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"control-room/internal/store"
)

// Project describes a single project.
type Project struct {
	ID          string   `json:"id" yaml:"id"`
	Title       string   `json:"title" yaml:"title"`
	RepoPath    string   `json:"repo_path" yaml:"repo_path"`
	DocsDir     string   `json:"docs_dir" yaml:"docs_dir"`
	Docs        []string `json:"docs" yaml:"docs"`
	DefaultTeam string   `json:"default_team" yaml:"default_team"`
	Rules       []string `json:"rules" yaml:"rules"`
	CreatedAt   string   `json:"created_at" yaml:"created_at"`
}

func Create(st *store.Store, p *Project) error {
	if p.ID == "" || p.Title == "" {
		return errors.New("project id and title are required")
	}
	if p.RepoPath != "" {
		if _, err := os.Stat(p.RepoPath); os.IsNotExist(err) {
			return errors.New("repo path does not exist: " + p.RepoPath)
		}
	}
	if p.DocsDir != "" {
		if info, err := os.Stat(p.DocsDir); err != nil || !info.IsDir() {
			return errors.New("docs-dir is not a directory: " + p.DocsDir)
		}
	}
	if p.CreatedAt == "" {
		p.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return st.WriteJSON([]string{"projects", p.ID + ".json"}, p)
}

func Get(st *store.Store, id string) (*Project, error) {
	var p Project
	err := st.ReadJSON([]string{"projects", id + ".json"}, &p)
	return &p, err
}

func List(st *store.Store) ([]Project, error) {
	names, err := st.ListJSON([]string{"projects"})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Project
	for _, n := range names {
		var p Project
		if err := st.ReadJSON([]string{"projects", n}, &p); err == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

func RepoExists(repoPath string) bool {
	_, err := os.Stat(filepath.Join(repoPath, ".git"))
	return err == nil
}

// AddDoc registers a doc file for the project and copies it into the project's docs dir if possible.
func AddDoc(st *store.Store, projectID string, filePath string) error {
	p, err := Get(st, projectID)
	if err != nil {
		return err
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("file is a directory")
	}

	// If project has a DocsDir, copy the file there and keep the stored path relative to DocsDir.
	if p.DocsDir != "" {
		target := filepath.Join(p.DocsDir, filepath.Base(filePath))
		data, err := os.ReadFile(filePath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
		filePath = target
	}

	for _, d := range p.Docs {
		if d == filePath {
			return nil
		}
	}
	p.Docs = append(p.Docs, filePath)
	return st.WriteJSON([]string{"projects", p.ID + ".json"}, p)
}

// ListDocs returns the list of doc files registered for the project.
func ListDocs(st *store.Store, projectID string) ([]string, error) {
	p, err := Get(st, projectID)
	if err != nil {
		return nil, err
	}
	return p.Docs, nil
}

// ReadDocs reads the content of all registered docs, capped per file.
func ReadDocs(st *store.Store, projectID string, maxBytes int) (map[string]string, error) {
	p, err := Get(st, projectID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for _, path := range p.Docs {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if maxBytes > 0 && len(data) > maxBytes {
			data = data[:maxBytes]
		}
		out[path] = string(data)
	}
	return out, nil
}
