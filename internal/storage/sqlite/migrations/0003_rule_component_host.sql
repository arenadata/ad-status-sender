-- +goose Up
-- Add host_id to rule_component so a component target can name a specific ADCM
-- shared-host duplicate. host_id=0 means the rule's scoped host (yaml/legacy).
-- Nothing references rule_component, so the child-table rebuild is FK-safe inside
-- goose's transaction.
CREATE TABLE rule_component_new (
  rule_id      INTEGER NOT NULL,
  component_id TEXT    NOT NULL,
  host_id      INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (rule_id, component_id, host_id),
  FOREIGN KEY (rule_id) REFERENCES rule(id) ON DELETE CASCADE
);
INSERT INTO rule_component_new(rule_id, component_id, host_id)
  SELECT rule_id, component_id, 0 FROM rule_component;
DROP TABLE rule_component;
ALTER TABLE rule_component_new RENAME TO rule_component;

-- +goose Down
CREATE TABLE rule_component_old (
  rule_id      INTEGER NOT NULL,
  component_id TEXT    NOT NULL,
  PRIMARY KEY (rule_id, component_id),
  FOREIGN KEY (rule_id) REFERENCES rule(id) ON DELETE CASCADE
);
INSERT OR IGNORE INTO rule_component_old(rule_id, component_id)
  SELECT rule_id, component_id FROM rule_component;
DROP TABLE rule_component;
ALTER TABLE rule_component_old RENAME TO rule_component;
