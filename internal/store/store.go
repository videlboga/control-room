package store

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Store is a simple filesystem-backed store with JSON/YAML helpers.
type Store struct {
	Root                string
	HermesUser          string
	HermesSourceProfile string
	MaxConcurrentRuns   int
	StubMode            bool
}

func New(root string) *Store {
	return &Store{Root: root}
}

func (s *Store) dir(parts ...string) string {
	return filepath.Join(append([]string{s.Root}, parts...)...)
}

func (s *Store) EnsureDir(parts ...string) error {
	return os.MkdirAll(s.dir(parts...), 0o755)
}

func (s *Store) WriteJSON(parts []string, v any) error {
	path := s.dir(parts...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (s *Store) ReadJSON(parts []string, v any) error {
	path := s.dir(parts...)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func (s *Store) WriteYAML(parts []string, v any) error {
	path := s.dir(parts...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (s *Store) ReadYAML(parts []string, v any) error {
	path := s.dir(parts...)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, v)
}

func (s *Store) AppendJSONL(parts []string, v any) error {
	path := s.dir(parts...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, string(data))
	return err
}

func (s *Store) ListJSON(parts []string) ([]string, error) {
	entries, err := os.ReadDir(s.dir(parts...))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// DisplayIDFromInternal returns a stable human-readable ID for legacy objects.
func (s *Store) DisplayIDFromInternal(kind, internalID string) string {
	prefix := strings.ToUpper(kind)
	suffix := internalID
	if i := strings.LastIndex(internalID, "_"); i >= 0 {
		suffix = internalID[i+1:]
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(suffix))
	return fmt.Sprintf("%s-%d", prefix, h.Sum32()%900000+100000)
}
