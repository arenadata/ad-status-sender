-- +goose Up
-- The rule_docker PRIMARY KEY (rule_id, name, label) does not enforce uniqueness:
-- exactly one of name/label is always NULL and SQLite treats NULLs as distinct in
-- a composite primary key, so duplicate docker rules can be inserted. Enforce it
-- with a NULL-normalizing unique index.
CREATE UNIQUE INDEX IF NOT EXISTS ux_rule_docker_ident
  ON rule_docker (rule_id, COALESCE(name, ''), COALESCE(label, ''));

-- +goose Down
DROP INDEX IF EXISTS ux_rule_docker_ident;
