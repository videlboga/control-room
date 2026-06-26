package store

import (
	"fmt"
	"hash/fnv"
	"strings"
)

// Store is a persistence facade. By default it uses JSON files; a Backend
// implementation can switch it to Postgres or another engine.
type Store struct {
	be                  Backend
	Root                string
	HermesUser          string
	HermesSourceProfile string
	MaxConcurrentRuns   int
	StubMode            bool
}

// New creates a JSON-backed store.
func New(root string) *Store {
	return &Store{be: NewJSONBackend(root), Root: root}
}

// NewWithBackend creates a store using the supplied backend.
func NewWithBackend(root string, b Backend) *Store {
	return &Store{be: b, Root: root}
}

func (s *Store) backend() Backend {
	if s.be == nil {
		s.be = NewJSONBackend(s.Root)
	}
	return s.be
}

func (s *Store) EnsureDir(parts ...string) error {
	return s.backend().EnsureDir(parts...)
}

func (s *Store) WriteJSON(parts []string, v any) error {
	return s.backend().WriteJSON(parts, v)
}

func (s *Store) ReadJSON(parts []string, v any) error {
	return s.backend().ReadJSON(parts, v)
}

func (s *Store) WriteYAML(parts []string, v any) error {
	return s.backend().WriteYAML(parts, v)
}

func (s *Store) ReadYAML(parts []string, v any) error {
	return s.backend().ReadYAML(parts, v)
}

func (s *Store) AppendJSONL(parts []string, v any) error {
	return s.backend().AppendJSONL(parts, v)
}

func (s *Store) ListJSON(parts []string) ([]string, error) {
	return s.backend().ListJSON(parts)
}

func (s *Store) AgentLog(runID string, n int) ([]string, error) {
	return s.backend().AgentLog(runID, n)
}

func (s *Store) dir(parts ...string) string {
	return NewJSONBackend(s.Root).dir(parts...)
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
