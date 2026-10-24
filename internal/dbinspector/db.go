package dbinspector

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// DBConfig describes one user-configured database connection.
type DBConfig struct {
	Name string `json:"name"`
	Type string `json:"type"` // "postgres" or "mysql"
	URL  string `json:"url"`
}

// Column is one table column with its key metadata.
type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
	Default  string `json:"default,omitempty"`
	Key      string `json:"key,omitempty"` // PK, FK, or empty
}

// Table is one table with its columns.
type Table struct {
	Name    string   `json:"name"`
	Columns []Column `json:"columns"`
}

// Open opens a connection to the configured database.
func Open(dbcfg DBConfig) (*sql.DB, error) {
	var db *sql.DB
	switch dbcfg.Type {
	case "postgres":
		cfg, err := pgx.ParseConfig(dbcfg.URL)
		if err != nil {
			return nil, fmt.Errorf("invalid PostgreSQL connection settings for %q", dbcfg.Name)
		}
		cfg.ConnectTimeout = 5 * time.Second
		cfg.RuntimeParams["default_transaction_read_only"] = "on"
		cfg.RuntimeParams["statement_timeout"] = "5000"
		db = stdlib.OpenDB(*cfg)
	case "mysql":
		cfg, err := mysql.ParseDSN(dbcfg.URL)
		if err != nil {
			return nil, fmt.Errorf("invalid MySQL connection settings for %q", dbcfg.Name)
		}
		cfg.MultiStatements = false
		cfg.Timeout = 5 * time.Second
		cfg.ReadTimeout = 5 * time.Second
		cfg.WriteTimeout = 5 * time.Second
		connector, err := mysql.NewConnector(cfg)
		if err != nil {
			return nil, fmt.Errorf("invalid MySQL connection settings for %q", dbcfg.Name)
		}
		db = sql.OpenDB(connector)
	default:
		return nil, fmt.Errorf("unsupported database type %q", dbcfg.Type)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	return db, nil
}

// Introspect lists the tables and columns of the database, scoped to
// the default schema.
func Introspect(ctx context.Context, db *sql.DB, dbType string) ([]Table, error) {
	if dbType == "mysql" {
		return introspectMySQL(ctx, db)
	}
	return introspectPostgres(ctx, db)
}

func introspectPostgres(ctx context.Context, db *sql.DB) ([]Table, error) {
	const colsQuery = `
SELECT c.table_schema || '.' || c.table_name, c.column_name, c.data_type, c.is_nullable, COALESCE(c.column_default, ''),
       COALESCE((SELECT string_agg(DISTINCT tc.constraint_type, ',')
         FROM information_schema.key_column_usage kcu
         JOIN information_schema.table_constraints tc
           ON tc.constraint_name = kcu.constraint_name AND tc.table_schema = kcu.table_schema
          AND tc.table_name = kcu.table_name
         WHERE kcu.table_schema = c.table_schema AND kcu.table_name = c.table_name
           AND kcu.column_name = c.column_name), '')
FROM information_schema.columns c
WHERE c.table_schema = ANY (current_schemas(false))
ORDER BY c.table_schema, c.table_name, c.ordinal_position LIMIT 2001`
	return collectTables(ctx, db, colsQuery)
}

func introspectMySQL(ctx context.Context, db *sql.DB) ([]Table, error) {
	const colsQuery = `
SELECT c.table_name, c.column_name, c.column_type, c.is_nullable, COALESCE(c.column_default, ''),
       COALESCE((SELECT GROUP_CONCAT(DISTINCT tc.constraint_type)
         FROM information_schema.key_column_usage kcu
         JOIN information_schema.table_constraints tc
           ON tc.constraint_name = kcu.constraint_name AND tc.table_schema = kcu.table_schema
          AND tc.table_name = kcu.table_name
         WHERE kcu.table_schema = c.table_schema AND kcu.table_name = c.table_name
           AND kcu.column_name = c.column_name), '')
FROM information_schema.columns c
WHERE c.table_schema = DATABASE()
ORDER BY c.table_name, c.ordinal_position LIMIT 2001`
	return collectTables(ctx, db, colsQuery)
}

func collectTables(ctx context.Context, db *sql.DB, query string) ([]Table, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("failed to begin read-only schema transaction: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("introspection query failed: %w", err)
	}
	defer rows.Close()

	byName := make(map[string]*Table)
	var order []string
	count := 0
	for rows.Next() {
		count++
		if count > 2000 {
			return nil, fmt.Errorf("schema exceeds the 2000-column limit")
		}
		var tableName, colName, colType, nullable, def, constraint string
		if err := rows.Scan(&tableName, &colName, &colType, &nullable, &def, &constraint); err != nil {
			return nil, err
		}
		t, ok := byName[tableName]
		if !ok {
			t = &Table{Name: tableName}
			byName[tableName] = t
			order = append(order, tableName)
		}
		key := ""
		if strings.Contains(strings.ToUpper(constraint), "PRIMARY") {
			key = "PK"
		}
		if strings.Contains(strings.ToUpper(constraint), "FOREIGN") {
			if key != "" {
				key += ","
			}
			key += "FK"
		}
		t.Columns = append(t.Columns, Column{
			Name:     colName,
			Type:     colType,
			Nullable: strings.EqualFold(nullable, "YES") || strings.EqualFold(nullable, "1") || strings.EqualFold(nullable, "true"),
			Default:  def,
			Key:      key,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	tables := make([]Table, 0, len(order))
	for _, name := range order {
		tables = append(tables, *byName[name])
	}
	return tables, nil
}

// Query runs a guard-validated read-only query and returns the result
// set.
func Query(ctx context.Context, db *sql.DB, query string, guard Guard) ([]string, [][]string, error) {
	if err := guard.Validate(query); err != nil {
		return nil, nil, err
	}
	qctx, cancel := context.WithTimeout(ctx, guard.Timeout)
	defer cancel()

	tx, err := db.BeginTx(qctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to begin read-only query transaction: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(qctx, query)
	if err != nil {
		return nil, nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	var out [][]string
	totalBytes := 0
	for rows.Next() {
		if len(out) >= guard.MaxRows {
			break
		}
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		record := make([]string, len(cols))
		for i, v := range raw {
			if b, ok := v.([]byte); ok {
				record[i] = string(b)
			} else if t, ok := v.(time.Time); ok {
				record[i] = t.Format(time.RFC3339)
			} else if v == nil {
				record[i] = "NULL"
			} else {
				record[i] = fmt.Sprintf("%v", v)
			}
			if len(record[i]) > 4096 {
				record[i] = record[i][:4096] + " [truncated]"
			}
			totalBytes += len(record[i])
		}
		if totalBytes > 128<<10 {
			break
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(out) == guard.MaxRows || totalBytes > 128<<10 {
		out = append(out, []string{"[Result limit reached; narrow the query for more rows.]"})
	}
	return cols, out, nil
}

// RenderSchema renders tables as a markdown report.
func RenderSchema(tables []Table) string {
	var sb strings.Builder
	for _, t := range tables {
		fmt.Fprintf(&sb, "## %s\n", cell(t.Name))
		sb.WriteString("| Column | Type | Nullable | Default | Key |\n|---|---|---|---|---|\n")
		for _, c := range t.Columns {
			fmt.Fprintf(&sb, "| %s | %s | %v | %s | %s |\n",
				cell(c.Name), cell(c.Type), c.Nullable, cell(c.Default), cell(c.Key))
		}
		sb.WriteString("\n")
	}
	if sb.Len() == 0 {
		return "(no tables found)"
	}
	return sb.String()
}

// RenderResult renders a query result as a markdown table.
func RenderResult(cols []string, rows [][]string) string {
	var sb strings.Builder
	cleanCols := make([]string, len(cols))
	for i, col := range cols {
		cleanCols[i] = cell(col)
	}
	sb.WriteString("| " + strings.Join(cleanCols, " | ") + " |\n")
	sb.WriteString("|" + strings.Repeat("---|", len(cols)) + "\n")
	for _, row := range rows {
		clean := make([]string, len(row))
		for i, value := range row {
			clean[i] = cell(value)
		}
		sb.WriteString("| " + strings.Join(clean, " | ") + " |\n")
	}
	if len(rows) == 0 {
		sb.WriteString("(no rows)\n")
	}
	return sb.String()
}

func cell(value string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ", "\r", " ").Replace(value)
}
