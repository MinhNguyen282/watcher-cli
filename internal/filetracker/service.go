// Package filetracker provides functionality to track file reads in sessions.
package filetracker

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/charmbracelet/crush/internal/db"
)

// ErrStaleRead means a file changed, or appeared, after the session read
// it. The message is stable so tool prompts and tests can rely on it.
var ErrStaleRead = errors.New("file changed since it was last read")

// ErrUnreadFile means a mutator touched an existing file the session never
// observed.
var ErrUnreadFile = errors.New("file has not been read in this session")

// Service defines the interface for tracking file reads in sessions.
type Service interface {
	// RecordRead records when a file was read.
	RecordRead(ctx context.Context, sessionID, path string)

	// RecordVersion records the observed content version of a path. A
	// missing path is recorded as an absent observation.
	RecordVersion(ctx context.Context, sessionID, path string)

	// CheckVersion verifies that an existing file still matches the
	// version most recently observed in the session. A missing file is
	// always allowed: creating a new file is not a stale read.
	CheckVersion(ctx context.Context, sessionID, path string) error

	// LastReadTime returns when a file was last read.
	// Returns zero time if never read.
	LastReadTime(ctx context.Context, sessionID, path string) time.Time

	// ListReadFiles returns the paths of all files read in a session.
	ListReadFiles(ctx context.Context, sessionID string) ([]string, error)
}

type service struct {
	q    *db.Queries
	conn *sql.DB
}

// NewService creates a new file tracker service. The optional connection is
// used for version observations; without it, version checks are disabled.
func NewService(q *db.Queries, conn ...*sql.DB) Service {
	s := &service{q: q}
	if len(conn) > 0 {
		s.conn = conn[0]
	}
	return s
}

// RecordRead records when a file was read.
func (s *service) RecordRead(ctx context.Context, sessionID, path string) {
	if err := s.q.RecordFileRead(ctx, db.RecordFileReadParams{
		SessionID: sessionID,
		Path:      relpath(path),
	}); err != nil {
		slog.Error("Error recording file read", "error", err, "file", path)
	}
}

// RecordVersion stores the current content version of path. If the file
// does not exist it stores an explicit absence so a later create can be
// distinguished from a rewrite.
func (s *service) RecordVersion(ctx context.Context, sessionID, path string) {
	if s.conn == nil || sessionID == "" {
		return
	}
	version, exists, err := versionOf(path)
	if err != nil {
		slog.Error("Error observing file version", "error", err, "file", path)
		return
	}
	_, err = s.conn.ExecContext(ctx, `
		INSERT INTO file_versions (session_id, path, version, exists_flag, observed_at)
		VALUES (?, ?, ?, ?, strftime('%s','now'))
		ON CONFLICT(session_id, path) DO UPDATE SET
			version = excluded.version,
			exists_flag = excluded.exists_flag,
			observed_at = excluded.observed_at`,
		sessionID, relpath(path), version, boolInt(exists),
	)
	if err != nil {
		slog.Error("Error recording file version", "error", err, "file", path)
	}
}

// CheckVersion rejects an edit when an existing file no longer matches the
// version the session observed.
func (s *service) CheckVersion(ctx context.Context, sessionID, path string) error {
	if s.conn == nil || sessionID == "" {
		return nil
	}
	current, exists, err := versionOf(path)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	var observedVersion string
	var observedExists int
	err = s.conn.QueryRowContext(ctx,
		`SELECT version, exists_flag FROM file_versions WHERE session_id = ? AND path = ?`,
		sessionID, relpath(path),
	).Scan(&observedVersion, &observedExists)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s. Read the file before modifying it", ErrUnreadFile, path)
	}
	if err != nil {
		return fmt.Errorf("checking read version: %w", err)
	}
	if observedExists == 0 {
		return fmt.Errorf("%w: %s appeared after the session observed it as missing. Read it before modifying it", ErrStaleRead, path)
	}
	if observedVersion != current {
		return fmt.Errorf("%w: %s. Read the file again before modifying it", ErrStaleRead, path)
	}
	return nil
}

// LastReadTime returns when a file was last read.
// Returns zero time if never read.
func (s *service) LastReadTime(ctx context.Context, sessionID, path string) time.Time {
	readFile, err := s.q.GetFileRead(ctx, db.GetFileReadParams{
		SessionID: sessionID,
		Path:      relpath(path),
	})
	if err != nil {
		return time.Time{}
	}

	return time.Unix(readFile.ReadAt, 0)
}

func relpath(path string) string {
	path = filepath.Clean(path)
	basepath, err := os.Getwd()
	if err != nil {
		slog.Warn("Error getting basepath", "error", err)
		return path
	}
	relpath, err := filepath.Rel(basepath, path)
	if err != nil {
		slog.Warn("Error getting relpath", "error", err)
		return path
	}
	return relpath
}

// ListReadFiles returns the paths of all files read in a session.
func (s *service) ListReadFiles(ctx context.Context, sessionID string) ([]string, error) {
	readFiles, err := s.q.ListSessionReadFiles(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("listing read files: %w", err)
	}

	basepath, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting working directory: %w", err)
	}

	paths := make([]string, 0, len(readFiles))
	for _, rf := range readFiles {
		paths = append(paths, filepath.Join(basepath, rf.Path))
	}
	return paths, nil
}

// versionOf returns a content version built from the file size and the
// SHA-256 of its bytes. Size alone is not enough: a same-size rewrite must
// be detected.
func versionOf(path string) (version string, exists bool, err error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if info.IsDir() {
		return "", false, fmt.Errorf("%s is a directory", path)
	}

	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close() //nolint:errcheck

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", false, err
	}
	return fmt.Sprintf("%d:%s", info.Size(), hex.EncodeToString(h.Sum(nil))), true, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
