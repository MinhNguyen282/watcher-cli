package memory

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSaveAndSearch(t *testing.T) {
	t.Parallel()

	store, err := NewStore(t.TempDir())
	require.NoError(t, err)

	entry, err := store.Save(Entry{
		Type:    TypeProject,
		Title:   "Run tests with task test",
		Content: "This repository uses `task test`; `go test ./...` also works.",
	})
	require.NoError(t, err)
	require.NotEmpty(t, entry.ID)
	require.False(t, entry.CreatedAt.IsZero())

	other, err := store.Save(Entry{
		Type:    TypeUser,
		Title:   "Prefers terse answers",
		Content: "Keep responses short and skip preamble.",
	})
	require.NoError(t, err)
	require.NotEqual(t, entry.ID, other.ID)

	matches := store.Search("task test", 5)
	require.Len(t, matches, 1)
	require.Equal(t, entry.ID, matches[0].ID)

	require.Len(t, store.Search("terse", 5), 1)
	require.Empty(t, store.Search("kubernetes", 5))
}

func TestSaveValidation(t *testing.T) {
	t.Parallel()

	store, err := NewStore(t.TempDir())
	require.NoError(t, err)

	_, err = store.Save(Entry{Type: "bogus", Title: "t", Content: "c"})
	require.ErrorContains(t, err, "unknown memory type")

	_, err = store.Save(Entry{Type: TypeUser, Title: " ", Content: "c"})
	require.ErrorContains(t, err, "title is required")

	_, err = store.Save(Entry{Type: TypeUser, Title: "t", Content: ""})
	require.ErrorContains(t, err, "content is required")

	_, err = store.Save(Entry{Type: TypeUser, Title: "t", Content: "token ghp_abcdefghijklmnop"})
	require.ErrorContains(t, err, "looks like a secret")
}

func TestUpdateExistingEntry(t *testing.T) {
	t.Parallel()

	store, err := NewStore(t.TempDir())
	require.NoError(t, err)

	entry, err := store.Save(Entry{Type: TypeUser, Title: "Title", Content: "one"})
	require.NoError(t, err)

	updated, err := store.Save(Entry{ID: entry.ID, Type: TypeUser, Title: "Title", Content: "two"})
	require.NoError(t, err)
	require.Equal(t, entry.ID, updated.ID)
	require.Equal(t, entry.CreatedAt, updated.CreatedAt)
	require.True(t, updated.UpdatedAt.After(entry.UpdatedAt) || updated.UpdatedAt.Equal(entry.UpdatedAt))

	require.Len(t, store.List(), 1)
	require.Equal(t, "two", store.List()[0].Content)

	_, err = store.Save(Entry{ID: "missing", Type: TypeUser, Title: "t", Content: "c"})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestForget(t *testing.T) {
	t.Parallel()

	store, err := NewStore(t.TempDir())
	require.NoError(t, err)

	entry, err := store.Save(Entry{Type: TypeTeam, Title: "Decision", Content: "Use sqlc."})
	require.NoError(t, err)
	require.NoError(t, store.Forget(entry.ID))
	require.Empty(t, store.List())
	require.ErrorIs(t, store.Forget(entry.ID), ErrNotFound)
}

func TestStorePersistsAcrossOpens(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := NewStore(dir)
	require.NoError(t, err)
	_, err = store.Save(Entry{Type: TypeProject, Title: "Keep me", Content: "durable"})
	require.NoError(t, err)

	reopened, err := NewStore(dir)
	require.NoError(t, err)
	entries := reopened.List()
	require.Len(t, entries, 1)
	require.Equal(t, "Keep me", entries[0].Title)
}
