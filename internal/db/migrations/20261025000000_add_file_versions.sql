-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS file_versions (
    session_id TEXT NOT NULL CHECK (session_id != ''),
    path TEXT NOT NULL CHECK (path != ''),
    version TEXT NOT NULL,
    exists_flag INTEGER NOT NULL DEFAULT 1,
    observed_at INTEGER NOT NULL,  -- Unix timestamp in seconds
    FOREIGN KEY (session_id) REFERENCES sessions (id) ON DELETE CASCADE,
    PRIMARY KEY (session_id, path)
);

CREATE INDEX IF NOT EXISTS idx_file_versions_session_id ON file_versions (session_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_file_versions_session_id;
DROP TABLE IF EXISTS file_versions;
-- +goose StatementEnd
