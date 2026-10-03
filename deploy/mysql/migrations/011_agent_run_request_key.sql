-- Run once after 010 on an existing go_im database.
-- NULL preserves pre-existing runs, whose original request key is unknown.
USE go_im;

ALTER TABLE agent_runs
    ADD COLUMN request_key VARCHAR(64) NULL AFTER initiator_id,
    ADD COLUMN request_fingerprint CHAR(64) NULL AFTER request_key,
    ADD UNIQUE KEY uk_agent_runs_initiator_request (initiator_id, request_key);
