-- +goose Up
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;

CREATE TABLE IF NOT EXISTS rules_revision (
  id          INTEGER PRIMARY KEY CHECK (id = 1),
  revision    INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
INSERT INTO rules_revision(id, revision, updated_at)
VALUES (1, 1, strftime('%s','now'))
ON CONFLICT(id) DO NOTHING;

CREATE TABLE IF NOT EXISTS host (
  id         INTEGER PRIMARY KEY,
  name       TEXT,
  updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);

CREATE TABLE IF NOT EXISTS rule (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  kind       TEXT NOT NULL,   -- 'systemd' | 'docker'
  name       TEXT,
  enabled    INTEGER NOT NULL DEFAULT 1,
  updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS ix_rule_kind    ON rule(kind);
CREATE INDEX IF NOT EXISTS ix_rule_enabled ON rule(enabled);
CREATE INDEX IF NOT EXISTS ix_rule_updated ON rule(updated_at);

CREATE TABLE IF NOT EXISTS rule_component (
  rule_id      INTEGER NOT NULL,
  component_id TEXT    NOT NULL,
  PRIMARY KEY (rule_id, component_id),
  FOREIGN KEY (rule_id) REFERENCES rule(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS rule_host_scope (
  rule_id INTEGER NOT NULL,
  host_id INTEGER NOT NULL,
  PRIMARY KEY (rule_id, host_id),
  FOREIGN KEY (rule_id) REFERENCES rule(id) ON DELETE CASCADE,
  FOREIGN KEY (host_id) REFERENCES host(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS rule_systemd (
  rule_id   INTEGER PRIMARY KEY,
  unit      TEXT,
  unit_glob TEXT,
  FOREIGN KEY (rule_id) REFERENCES rule(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS rule_docker (
  rule_id INTEGER NOT NULL,
  name    TEXT,
  label   TEXT,
  CHECK (
    (name IS NOT NULL AND label IS NULL) OR
    (name IS NULL AND label IS NOT NULL)
  ),
  PRIMARY KEY (rule_id, name, label),
  FOREIGN KEY (rule_id) REFERENCES rule(id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE IF EXISTS rule_docker;
DROP TABLE IF EXISTS rule_systemd;
DROP TABLE IF EXISTS rule_host_scope;
DROP TABLE IF EXISTS rule_component;
DROP TABLE IF EXISTS rule;
DROP TABLE IF EXISTS host;
DROP TABLE IF EXISTS rules_revision;
