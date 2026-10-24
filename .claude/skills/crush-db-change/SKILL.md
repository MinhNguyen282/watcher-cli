---
name: crush-db-change
description: Add or change a SQLite table, column, index, or query in this repository. Use when the user asks for a schema change, a goose migration, or a new sqlc query.
allowed-tools: Read, Grep, Glob, Edit, Write, Bash
---

# Change the database schema or queries

`AGENTS.md` is the source of truth. This skill fixes the order of operations
and the checks that prove the change is wired correctly.

The database is SQLite. Queries live in `internal/db/sql/`, migrations in
`internal/db/migrations/`, and generated code in `internal/db/`. Goose applies
migrations at startup from the embedded migration files.

## 1. Add a goose migration

Create `internal/db/migrations/<YYYYMMDDHHMMSS>_<snake_name>.sql` using the
current UTC timestamp. Follow the existing style:

```sql
-- +goose Up
-- +goose StatementBegin
ALTER TABLE sessions ADD COLUMN example TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN example;
-- +goose StatementEnd
```

Use `StatementBegin` / `StatementEnd` around every multi-statement block. Use
`IF NOT EXISTS` / `IF EXISTS` where the statement supports it, matching the
existing migrations.

## 2. Add or update the sqlc query

Put the query in the matching file under `internal/db/sql/`, for example
`messages.sql` or `sessions.sql`. Annotate it with an sqlc directive:

```sql
-- name: GetSessionExample :one
SELECT example FROM sessions WHERE id = ?;
```

Use `:one`, `:many`, or `:exec`. Keep the SQL SQLite-compatible.

## 3. Regenerate the Go code

```sh
sqlc generate
```

Never hand-edit `internal/db/*.sql.go`, `internal/db/db.go`,
`internal/db/models.go`, or `internal/db/querier.go`. If `sqlc` is missing:

```sh
go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0
```

Pin v1.30.0 to match the `sqlc v1.30.0` headers in the generated files.

## 4. Wire the service and add tests

- Update the owning service (`internal/session`, `internal/message`,
  `internal/history`, and so on) to call the generated method.
- Add a negative control for new constraints. A `CHECK`, `FOREIGN KEY`, or
  `UNIQUE` clause is only proven by a test that supplies invalid data and
  asserts the write fails. `docs/testing.md` requires this.
- Read the latest state before asserting on it when the write path is
  debounced; call `Flush` / `FlushAll` first (`AGENTS.md`, "Message
  persistence and events").

## 5. Config options are separate

If the change adds a config option rather than a table, edit `Options` in
`internal/config/config.go` and `optionSpecs` in
`internal/shellconfig/options.go`, then regenerate the schema:

```sh
go run main.go schema > schema.json
```

Do not edit `schema.json` by hand.

## 6. Verify

```sh
sqlc generate
go build ./...
go test ./internal/db/... ./internal/session/... ./internal/message/...
git diff --stat
```

The generated files must change together with the SQL sources. A diff that
touches a generated file without its SQL source is a bug.
