ALTER TABLE integration_accounts ADD COLUMN billing_suspended boolean NOT NULL DEFAULT false;
ALTER TABLE integration_accounts ADD COLUMN billing_protocol varchar(32) NOT NULL DEFAULT '';
{{integration_requests_table}};
CREATE UNIQUE INDEX idx_integration_request_key ON integration_requests(account_id, request_key);
CREATE INDEX idx_integration_requests_state ON integration_requests(state);
