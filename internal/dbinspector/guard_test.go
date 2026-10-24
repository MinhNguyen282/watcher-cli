package dbinspector

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGuard(t *testing.T) {
	t.Parallel()
	allowed := []string{
		"SELECT id, name FROM users LIMIT 10", " select 'delete; update' AS message; -- end",
		"/* ordinary comment */ WITH a AS (SELECT 1) SELECT * FROM a",
		"SHOW TABLES", "DESCRIBE users", "EXPLAIN SELECT * FROM users",
		"SELECT 'it''s safe', \"user\" FROM \"users\"", "SELECT `id` FROM `users`",
	}
	blocked := []string{
		"", "-- no statement", "DELETE FROM users", "SELECT 1; DELETE FROM users", "SELECT 1;;",
		"WITH x AS (DELETE FROM users RETURNING *) SELECT * FROM x", "SELECT * INTO backup FROM users",
		"SELECT * FROM users FOR UPDATE", "SELECT * FROM users FOR SHARE", "EXPLAIN ANALYZE SELECT 1",
		"SELECT 1 /*!50000 INTO OUTFILE '/tmp/leak' */", "SELECT 1 /*M! INTO OUTFILE 'x' */",
		"SELECT 'unterminated", "SELECT 1 /* unfinished", "SELECT 1 /* nested /* comment */ */",
		"SELECT $$hidden; DELETE$$", "SELECT @x := 1", `SELECT 'back\slash'`,
		"SELECT pg_sleep(5)", `SELECT "pg_sleep"(5)`, "SELECT nextval('counter')",
		"SELECT 1 # hidden; DELETE FROM users", "CALL mutate()", "EXPLAIN DELETE FROM users",
	}
	for _, query := range allowed {
		t.Run("allow_"+query, func(t *testing.T) { t.Parallel(); require.NoError(t, DefaultGuard().Validate(query)) })
	}
	for _, query := range blocked {
		t.Run("block_"+query, func(t *testing.T) { t.Parallel(); require.Error(t, DefaultGuard().Validate(query)) })
	}
}
