// Package memory stores durable, typed notes the agent chooses to keep
// across sessions: user preferences, project facts and team decisions.
package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Type is the scope of a memory entry.
type Type string

const (
	// TypeUser is personal context: how the user likes to work.
	TypeUser Type = "user"
	// TypeProject is a fact about this repository or workspace.
	TypeProject Type = "project"
	// TypeTeam is shared context other collaborators should also see.
	TypeTeam Type = "team"
)

// Types is every valid memory type.
var Types = []Type{TypeUser, TypeProject, TypeTeam}

// Validate reports whether the type is known.
func (t Type) Validate() error {
	for _, known := range Types {
		if t == known {
			return nil
		}
	}
	names := make([]string, 0, len(Types))
	for _, known := range Types {
		names = append(names, string(known))
	}
	return fmt.Errorf("unknown memory type %q: must be one of %s", t, strings.Join(names, ", "))
}

// Entry is one stored memory.
type Entry struct {
	ID        string    `json:"id"`
	Type      Type      `json:"type"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Limits bound what a single store file may contain. They are deliberately
// small: memory is for durable facts, not for archiving transcripts.
const (
	maxEntries         = 500
	maxTitleChars      = 120
	maxContentChars    = 8_000
	maxStoreBytes      = 2 << 20
	searchDefaultLimit = 5
)

// ErrNotFound is returned when an entry ID is unknown.
var ErrNotFound = errors.New("memory entry not found")

// secretMarkers are substrings that must never be persisted. This is a
// guard against the obvious mistake, not a secret scanner.
var secretMarkers = []string{
	"BEGIN PRIVATE KEY",
	"BEGIN RSA PRIVATE KEY",
	"sk-ant-",
	"AKIA",
	"ghp_",
}

// Store is a small file-backed memory store. All methods are safe for
// concurrent use.
type Store struct {
	mu      sync.Mutex
	path    string
	entries []Entry
}

// NewStore opens (or initializes) the store rooted at dir.
func NewStore(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("memory: directory is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("memory: create directory: %w", err)
	}
	store := &Store{path: filepath.Join(dir, "memory.json")}
	data, err := os.ReadFile(store.path)
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return nil, fmt.Errorf("memory: read store: %w", err)
	}
	if len(data) > maxStoreBytes {
		return nil, fmt.Errorf("memory: store exceeds %d bytes", maxStoreBytes)
	}
	if err := json.Unmarshal(data, &store.entries); err != nil {
		return nil, fmt.Errorf("memory: parse store: %w", err)
	}
	return store, nil
}

// Save inserts or replaces an entry. A missing ID creates one; an existing
// ID updates it.
func (s *Store) Save(entry Entry) (Entry, error) {
	if err := entry.Type.Validate(); err != nil {
		return Entry{}, err
	}
	entry.Title = strings.TrimSpace(entry.Title)
	entry.Content = strings.TrimSpace(entry.Content)
	if entry.Title == "" {
		return Entry{}, errors.New("memory: title is required")
	}
	if entry.Content == "" {
		return Entry{}, errors.New("memory: content is required")
	}
	if len(entry.Title) > maxTitleChars {
		return Entry{}, fmt.Errorf("memory: title longer than %d characters", maxTitleChars)
	}
	if len(entry.Content) > maxContentChars {
		return Entry{}, fmt.Errorf("memory: content longer than %d characters", maxContentChars)
	}
	if marker := findSecretMarker(entry.Content); marker != "" {
		return Entry{}, fmt.Errorf("memory: refusing to store a value that looks like a secret (%s)", marker)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if entry.ID == "" {
		entry.ID = uuid.NewString()
		entry.CreatedAt = now
		if len(s.entries) >= maxEntries {
			return Entry{}, fmt.Errorf("memory: store is full (%d entries); forget something first", maxEntries)
		}
	} else {
		found := false
		for i := range s.entries {
			if s.entries[i].ID == entry.ID {
				entry.CreatedAt = s.entries[i].CreatedAt
				found = true
				break
			}
		}
		if !found {
			return Entry{}, ErrNotFound
		}
	}
	entry.UpdatedAt = now

	replaced := false
	for i := range s.entries {
		if s.entries[i].ID == entry.ID {
			s.entries[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		s.entries = append(s.entries, entry)
	}
	if err := s.persistLocked(); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// Search returns the entries whose title, content or type match every term
// of the query, most recently updated first.
func (s *Store) Search(query string, limit int) []Entry {
	if limit <= 0 {
		limit = searchDefaultLimit
	}
	terms := make([]string, 0, 4)
	for _, term := range strings.Fields(strings.ToLower(query)) {
		if len(term) >= 2 {
			terms = append(terms, term)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	matches := make([]Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		haystack := strings.ToLower(entry.Title + "\n" + entry.Content + "\n" + string(entry.Type))
		matched := true
		for _, term := range terms {
			if !strings.Contains(haystack, term) {
				matched = false
				break
			}
		}
		if matched {
			matches = append(matches, entry)
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].UpdatedAt.After(matches[j].UpdatedAt)
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

// List returns every entry, newest first.
func (s *Store) List() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, len(s.entries))
	copy(out, s.entries)
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

// Forget removes an entry by ID.
func (s *Store) Forget(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.entries {
		if s.entries[i].ID != id {
			continue
		}
		s.entries = append(s.entries[:i], s.entries[i+1:]...)
		return s.persistLocked()
	}
	return ErrNotFound
}

// persistLocked writes the store atomically. Callers hold the lock.
func (s *Store) persistLocked() error {
	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return fmt.Errorf("memory: encode store: %w", err)
	}
	if len(data) > maxStoreBytes {
		return fmt.Errorf("memory: store exceeds %d bytes", maxStoreBytes)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("memory: write store: %w", err)
	}
	return os.Rename(tmp, s.path)
}

func findSecretMarker(content string) string {
	for _, marker := range secretMarkers {
		if strings.Contains(content, marker) {
			return marker
		}
	}
	return ""
}
