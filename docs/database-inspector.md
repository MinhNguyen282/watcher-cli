# Database and API inspectors

The coding, research and planning agents can use three permission-controlled
tools: `db_schema`, `db_query`, and `openapi_spec`. They use the normal Crush
permission flow, including existing allowlists and session approvals. No
database is configured automatically.

Declare connections in `crushrc`, keeping credentials in environment variables:

```bash
db add app --type postgres --url '$APP_DATABASE_URL'
db add reporting --type mysql --url '$REPORTING_MYSQL_DSN'
db list
# db remove reporting
```

The single quotes preserve the variable reference until a permitted tool call
resolves it. PostgreSQL accepts a PostgreSQL URL or pgx connection string;
MySQL accepts its native driver DSN, for example
`reader:password@tcp(localhost:3306)/reporting`. Prefer TLS for remote servers.
Use a dedicated account restricted to SELECT and metadata access. Names must
contain only letters, digits, underscores or hyphens and start with a letter
or digit. Re-adding a name replaces that declaration; `db list` prints names
and types, never URLs. Equivalent JSON:

```json
{
  "databases": [
    {"name": "app", "type": "postgres", "url": "$APP_DATABASE_URL"}
  ]
}
```

`db_schema` reports tables, columns, nullable/default information, and primary
and foreign keys. PostgreSQL tables are schema-qualified and scoped to the
active search path; MySQL uses the selected database. More than 2,000 columns
returns an explicit size error. Schema calls have a ten-second context limit;
connection and driver statement timeouts may expire sooner.

`db_query` accepts one SELECT, WITH, SHOW, DESCRIBE/DESC, or EXPLAIN statement.
The conservative lexical guard rejects writes, data-modifying CTEs,
multi-statements, file exports, locking reads, EXPLAIN ANALYZE, several known
side-effect functions and executable comments. A trailing semicolon and normal
comments are supported. Dollar quoting, backslash escapes, variables and hash
comments/operators are deliberately unsupported. Some otherwise valid queries
will be rejected; simplify them rather than weakening the guard.

Queries run in a database-enforced read-only transaction and always roll back.
There is no fallback to an ordinary transaction if the driver/server cannot
enforce read-only mode. PostgreSQL also sets read-only sessions and a five-second
statement timeout. MySQL multi-statements are disabled. Each query is bounded
to five seconds, 200 returned rows, 4 KiB per cell and about 128 KiB of data,
with a visible limit notice. These are returned-data limits, not an optimizer
LIMIT rewrite; a large scan may run until the timeout. The guard and transaction
do not replace least-privilege database credentials: arbitrary stored functions
and database extensions can have effects outside ordinary table writes.

`openapi_spec` reads a local JSON/YAML OpenAPI 3.x or Swagger 2.0 file. It
summarizes methods, paths, summaries and request/response schemas, including
local component/definition schema references. It never fetches external refs.
File size is limited to 2 MiB, operations to 500, individual schema snippets
to 600 bytes, and rendered output to roughly 128 KiB. Output order is stable.
Nested schema refs are retained as refs; this is a compact inspector, not a
complete OpenAPI validator. Each call reads the current file, so edits are
immediately visible without cache invalidation.

All tools use the existing generic tool renderer. Permissions are requested
before resolving database credentials or accessing a specification file.
Invalid SQL is rejected before requesting database access. Driver parse errors
and variable-resolution failures do not expose connection URLs.

Run the relevant checks with:

```bash
go test ./internal/dbinspector ./internal/shellconfig ./internal/config ./internal/agent/tools
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go build .
```

Unit tests cover SQL guard bypass cases, read-only transaction requirements,
rollback and result limits, permissions, configuration validation, and both
specification formats. Connection tests against a live database additionally
require an explicitly configured test PostgreSQL/MySQL instance.
