ALTER TABLE logs ADD COLUMN billing_scope varchar(36) NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS integration_consumption_cutovers (id bigint PRIMARY KEY, last_log_id bigint NOT NULL);
INSERT INTO integration_consumption_cutovers (id, last_log_id) SELECT 1, COALESCE(MAX(id), 0) FROM logs WHERE NOT EXISTS (SELECT 1 FROM integration_consumption_cutovers WHERE id = 1) HAVING NOT EXISTS (SELECT 1 FROM integration_consumption_cutovers WHERE id = 1);
