package dbinspector

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type testConnector struct{ conn *testConn }

func (c testConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c testConnector) Driver() driver.Driver                        { return testDriver{} }

type testDriver struct{}

func (testDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type testConn struct {
	options    driver.TxOptions
	query      string
	rolledBack bool
	beginError error
	rows       *testRows
}

func (*testConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unexpected prepare") }
func (*testConn) Close() error                        { return nil }
func (*testConn) Begin() (driver.Tx, error)           { return nil, errors.New("read-only BeginTx required") }
func (c *testConn) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	c.options = options
	if c.beginError != nil {
		return nil, c.beginError
	}
	return testTx{c}, nil
}
func (c *testConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.query = query
	return c.rows, nil
}

type testTx struct{ conn *testConn }

func (testTx) Commit() error      { return errors.New("read queries must roll back") }
func (tx testTx) Rollback() error { tx.conn.rolledBack = true; return nil }

type testRows struct {
	columns  []string
	records  [][]driver.Value
	next     int
	closed   bool
	rowError error
}

func (r *testRows) Columns() []string { return r.columns }
func (r *testRows) Close() error      { r.closed = true; return nil }
func (r *testRows) Next(dest []driver.Value) error {
	if r.next >= len(r.records) {
		if r.rowError != nil {
			return r.rowError
		}
		return io.EOF
	}
	copy(dest, r.records[r.next])
	r.next++
	return nil
}

func TestQueryReadOnlyLimitsAndRollback(t *testing.T) {
	t.Parallel()
	conn := &testConn{rows: &testRows{columns: []string{"name", "value"}, records: [][]driver.Value{
		{[]byte("first|row"), nil}, {"second\nrow", strings.Repeat("x", 5000)}, {"third", int64(3)},
	}}}
	db := sql.OpenDB(testConnector{conn})
	defer db.Close()
	guard := DefaultGuard()
	guard.MaxRows = 2
	cols, rows, err := Query(t.Context(), db, "SELECT name, value FROM examples", guard)
	require.NoError(t, err)
	require.True(t, conn.options.ReadOnly)
	require.True(t, conn.rolledBack)
	require.True(t, conn.rows.closed)
	require.Equal(t, "NULL", rows[0][1])
	require.Contains(t, rows[1][1], "[truncated]")
	require.Contains(t, rows[2][0], "limit reached")
	rendered := RenderResult(cols, rows)
	require.Contains(t, rendered, `first\|row`)
	require.Contains(t, rendered, "second row")
	require.Equal(t, "second\nrow", rows[1][0], "Rendering must not mutate data")
}

func TestQueryRejectsBeforeConnecting(t *testing.T) {
	t.Parallel()
	_, _, err := Query(t.Context(), nil, "DELETE FROM users", DefaultGuard())
	require.Error(t, err)
}

func TestQueryRequiresReadOnlyTransaction(t *testing.T) {
	t.Parallel()
	conn := &testConn{beginError: errors.New("read-only transaction unsupported")}
	db := sql.OpenDB(testConnector{conn})
	defer db.Close()
	_, _, err := Query(t.Context(), db, "SELECT 1", DefaultGuard())
	require.ErrorContains(t, err, "read-only")
	require.Empty(t, conn.query)
}

func TestIntrospectionKeysAndScope(t *testing.T) {
	t.Parallel()
	for _, dialect := range []string{"postgres", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			t.Parallel()
			conn := &testConn{rows: &testRows{columns: []string{"table", "column", "type", "nullable", "default", "constraint"}, records: [][]driver.Value{
				{"public.users", "id", "integer", "NO", "", "PRIMARY KEY,FOREIGN KEY"},
			}}}
			db := sql.OpenDB(testConnector{conn})
			defer db.Close()
			tables, err := Introspect(t.Context(), db, dialect)
			require.NoError(t, err)
			require.True(t, conn.options.ReadOnly)
			require.True(t, conn.rolledBack)
			require.Equal(t, "PK,FK", tables[0].Columns[0].Key)
			require.False(t, tables[0].Columns[0].Nullable)
			require.Contains(t, conn.query, "LIMIT 2001")
			if dialect == "postgres" {
				require.Contains(t, conn.query, "current_schemas(false)")
			} else {
				require.Contains(t, conn.query, "DATABASE()")
			}
		})
	}
}

func TestOpenRejectsInvalidConfigurationWithoutLeakingURL(t *testing.T) {
	t.Parallel()
	for _, cfg := range []DBConfig{{Type: "unknown", URL: "secret"}, {Type: "mysql", URL: "secret invalid dsn"}, {Type: "postgres", URL: "://secret invalid"}} {
		_, err := Open(cfg)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret")
	}
}
