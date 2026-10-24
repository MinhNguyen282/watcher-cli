Run a single read-only SQL statement against a configured database.
Only SELECT, WITH, SHOW, DESCRIBE, and EXPLAIN statements are allowed;
write statements are rejected by a conservative guard and a database-enforced
read-only transaction. Results are limited to 200 rows and roughly 128 KiB,
with a five-second query timeout. Each call follows the normal permission flow.
Dollar quoting, backslash escapes, variables, locking reads and EXPLAIN ANALYZE
are unsupported. Use a dedicated read-only database account.

Use this to verify queries against the real schema before writing code
that runs them.
