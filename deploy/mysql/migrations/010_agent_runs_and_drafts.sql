-- Run once on an existing go_im database before enabling persistent Agent drafts.
USE go_im;

CREATE TABLE agent_runs (
    id           BIGINT PRIMARY KEY,
    team_id      BIGINT NOT NULL,
    group_id     BIGINT NOT NULL,
    initiator_id BIGINT NOT NULL,
    status       VARCHAR(32) NOT NULL,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB;

CREATE TABLE agent_task_drafts (
    run_id            BIGINT NOT NULL,
    item_index        SMALLINT UNSIGNED NOT NULL,
    title             VARCHAR(200) NOT NULL,
    description       TEXT NOT NULL,
    assignee_id       BIGINT NOT NULL DEFAULT 0,
    due_at_unix_ms    BIGINT NOT NULL DEFAULT 0,
    source_message_id BIGINT NOT NULL DEFAULT 0,
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (run_id, item_index)
) ENGINE=InnoDB;
