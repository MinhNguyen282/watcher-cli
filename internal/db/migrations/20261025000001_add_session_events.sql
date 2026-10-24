-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS session_events (
    session_id TEXT NOT NULL CHECK (session_id != ''),
    seq INTEGER NOT NULL CHECK (seq > 0),
    type TEXT NOT NULL CHECK (type != ''),
    created_at INTEGER NOT NULL,  -- Unix timestamp in seconds
    data TEXT NOT NULL DEFAULT '',
    ignorable INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (session_id) REFERENCES sessions (id) ON DELETE CASCADE,
    PRIMARY KEY (session_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_session_events_session_seq
    ON session_events (session_id, seq);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_session_events_session_seq;
DROP TABLE IF EXISTS session_events;
-- +goose StatementEnd
