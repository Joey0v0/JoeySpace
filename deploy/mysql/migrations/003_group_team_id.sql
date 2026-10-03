-- Stage 3: link future team groups to a team without changing existing groups.
-- Run once on an existing go_im database; old group rows keep team_id = NULL.
USE go_im;

ALTER TABLE `groups`
    ADD COLUMN team_id BIGINT NULL AFTER owner_id,
    ADD INDEX idx_groups_team (team_id, id);

-- No FK to teams: IM owns groups; team existence and permissions are checked
-- through the user/team RPC when a team group is created or accessed.
