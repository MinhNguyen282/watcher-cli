List the tables and columns of a configured database. Use this before
writing SQL queries or structs so names and types match the real
schema instead of guessing.

Only databases declared in the crush config (via `db add <name>
--type postgres|mysql --url ...`) are available. Access is read-only
and follows the normal permission flow. PostgreSQL tables are qualified by
schema and scoped to the active search path; MySQL uses the selected database.
The inspector returns an explicit error for schemas above 2,000 columns.
