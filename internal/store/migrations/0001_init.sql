-- athens app store, SQLite. Applied in order by internal/store (embedded).
-- schema_migrations(version, applied_at) is created by the migrator itself.
-- PRAGMAs set at open: journal_mode=WAL, foreign_keys=ON, busy_timeout=5000, synchronous=NORMAL.

CREATE TABLE users (
  id            TEXT PRIMARY KEY,                   -- Entra oid
  email         TEXT NOT NULL,
  name          TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL,
  last_login_at TEXT NOT NULL
);

CREATE TABLE sessions (
  id            TEXT PRIMARY KEY,                   -- sha256(cookie value), never store raw token
  user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  groups_json   TEXT NOT NULL,                      -- JSON array of group object IDs
  csrf_token    TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  last_seen_at  TEXT NOT NULL,
  expires_at    TEXT NOT NULL,
  client_ip     TEXT NOT NULL DEFAULT '',
  user_agent    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_expires_idx ON sessions(expires_at);
CREATE INDEX sessions_user_idx ON sessions(user_id);

CREATE TABLE query_history (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  target_id    TEXT NOT NULL,
  database     TEXT NOT NULL,
  sql          TEXT NOT NULL,
  class        TEXT NOT NULL,                       -- read|dml|ddl|other
  result       TEXT NOT NULL,                       -- ok|error|cancelled|denied
  duration_ms  INTEGER,
  rows         INTEGER,
  error        TEXT,
  created_at   TEXT NOT NULL
);
CREATE INDEX query_history_user_idx ON query_history(user_id, id DESC);

CREATE TABLE saved_queries (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,
  sql         TEXT NOT NULL,
  target_id   TEXT,
  database    TEXT,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  UNIQUE (user_id, name)
);

CREATE TABLE drafts (
  user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  target_id   TEXT NOT NULL,
  database    TEXT NOT NULL,
  sql         TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  PRIMARY KEY (user_id, target_id, database)
);

CREATE TABLE ui_state (
  user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  key         TEXT NOT NULL,
  value_json  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  PRIMARY KEY (user_id, key)
);

CREATE TABLE audit_log (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  ts               TEXT NOT NULL,
  event            TEXT NOT NULL,
  user_id          TEXT,                            -- no FK: audit must survive user deletion
  email            TEXT,
  client_ip        TEXT,
  user_agent       TEXT,
  target_id        TEXT,
  database         TEXT,
  object           TEXT,
  statement_class  TEXT,
  destructive      INTEGER NOT NULL DEFAULT 0,
  sql              TEXT,
  rows_affected    INTEGER,
  duration_ms      INTEGER,
  result           TEXT NOT NULL,
  error_code       TEXT,
  details_json     TEXT                             -- e.g. pk + changed column names for grid writes
);
CREATE INDEX audit_ts_idx ON audit_log(ts);
CREATE INDEX audit_user_idx ON audit_log(user_id, ts);
CREATE INDEX audit_target_idx ON audit_log(target_id, ts);
-- Append-only: no UPDATE path in code; DELETE only by retention purge.
CREATE TRIGGER audit_no_update BEFORE UPDATE ON audit_log
BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END;
