package shellconfig

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"regexp"
)

// handleDB implements the `db` builtin for read-only database
// connections used by the db_schema/db_query tools.
//
// Usage:
//
//	db add <name> --type postgres|mysql --url URL
//	db remove <name>   (alias: rm)
//	db list
func handleDB(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	b := configBuilderFromCtx(ctx)
	if b == nil {
		return nil
	}
	if len(args) < 2 {
		return usage(stderr, "usage: db add <name> --type postgres|mysql --url URL | db remove <name> | db list")
	}

	switch args[1] {
	case "add":
		return dbAdd(ctx, b, args, stderr)
	case "remove", "rm":
		return dbRemove(ctx, b, args, stderr)
	case "list":
		if len(args) != 2 {
			return usage(stderr, "usage: db list")
		}
		return dbList(ctx, b, args, stdout)
	default:
		return usage(stderr, fmt.Sprintf("db: unknown subcommand %q (expected add, remove, or list)", args[1]))
	}
}

var dbAddFlags = []flagSpec{
	{name: "--type", jsonKey: "type", kind: flagString, op: opSet},
	{name: "--url", jsonKey: "url", kind: flagString, op: opSet},
}

var databaseName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func dbEntries(b *ConfigBuilder) []any {
	entries, _ := b.root["databases"].([]any)
	return entries
}

func dbAdd(ctx context.Context, b *ConfigBuilder, args []string, stderr io.Writer) error {
	if len(args) < 3 {
		return usage(stderr, "usage: db add <name> --type postgres|mysql --url URL")
	}
	name := args[2]
	if !databaseName.MatchString(name) {
		return usage(stderr, "db add: name must contain only letters, numbers, underscores or hyphens")
	}
	entry := map[string]any{"name": name}
	if err := applyFlags(dbAddFlags, args, 3, entry, "db add", stderr); err != nil {
		return err
	}
	if entry["type"] != "postgres" && entry["type"] != "mysql" {
		return usage(stderr, "db add: --type must be postgres or mysql")
	}
	if url, ok := entry["url"].(string); !ok || url == "" {
		return usage(stderr, "db add: --url is required")
	}

	// Re-adding a name updates the existing entry in place.
	entries := dbEntries(b)
	kept := make([]any, 0, len(entries)+1)
	for _, item := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m, ok := item.(map[string]any); ok && m["name"] == name {
			continue
		}
		kept = append(kept, item)
	}
	b.root["databases"] = append(kept, entry)

	slog.Info("Database defined in shell config", "name", name, "type", entry["type"])
	return nil
}

func dbRemove(ctx context.Context, b *ConfigBuilder, args []string, stderr io.Writer) error {
	if len(args) != 3 {
		return usage(stderr, "usage: db remove <name>")
	}
	name := args[2]
	entries := dbEntries(b)
	kept := make([]any, 0, len(entries))
	for _, item := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m, ok := item.(map[string]any); ok && m["name"] == name {
			continue
		}
		kept = append(kept, item)
	}
	b.root["databases"] = kept

	slog.Info("Database removed in shell config", "name", name)
	return nil
}

func dbList(ctx context.Context, b *ConfigBuilder, args []string, stdout io.Writer) error {
	for _, item := range dbEntries(b) {
		if err := ctx.Err(); err != nil {
			return err
		}
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		typ, _ := m["type"].(string)
		fmt.Fprintf(stdout, "%s\t%s\n", name, typ)
	}
	return nil
}
