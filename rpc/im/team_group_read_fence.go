package main

import (
	"context"
	"database/sql"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

// This single statement checks fresh group membership and the permanent
// closure boundary in one database snapshot after User has returned an active
// generation. A missing fence row represents a pre-migration member at 0.
const teamGroupReadFenceSQL = "SELECT gm.group_id, f.closed_through_generation FROM group_members AS gm\n" +
	"JOIN `groups` AS g ON g.id = gm.group_id\n" +
	"LEFT JOIN im_team_group_fences AS f ON f.team_id = g.team_id AND f.user_id = gm.user_id\n" +
	"WHERE gm.group_id = ? AND gm.user_id = ? AND g.team_id = ? LIMIT 2"

func checkTeamGroupReadGeneration(ctx context.Context, db *gorm.DB, groupID, teamID, userID, generation int64) error {
	if groupID <= 0 {
		return status.Error(codes.InvalidArgument, "positive group ID required")
	}
	if err := validateTeamGroupFenceInput(ctx, db, teamID, userID, generation); err != nil {
		return err
	}
	rows, err := db.WithContext(ctx).Raw(teamGroupReadFenceSQL, groupID, userID, teamID).Rows()
	if err != nil {
		return teamGroupFenceDBError(ctx)
	}
	defer rows.Close()
	found := false
	var closed int64
	for rows.Next() {
		var actualGroup int64
		var stored sql.NullInt64
		if found || rows.Scan(&actualGroup, &stored) != nil || actualGroup != groupID || stored.Valid && stored.Int64 < 0 {
			return teamGroupFenceDBError(ctx)
		}
		if stored.Valid {
			closed = stored.Int64
		}
		found = true
	}
	if rows.Err() != nil || ctx.Err() != nil {
		return teamGroupFenceDBError(ctx)
	}
	if !found || generation <= closed {
		return status.Error(codes.PermissionDenied, "current team group membership required")
	}
	return nil
}
