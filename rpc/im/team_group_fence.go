package main

import (
	"context"
	"errors"

	"github.com/yjydist/go-im/internal/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

const initializeTeamGroupFenceSQL = `INSERT INTO im_team_group_fences (team_id, user_id, closed_through_generation)
VALUES (?, ?, 0) ON DUPLICATE KEY UPDATE team_id = team_id`
const lockTeamGroupFenceSQL = `SELECT team_id, user_id, closed_through_generation FROM im_team_group_fences
WHERE team_id = ? AND user_id = ? FOR UPDATE`
const advanceTeamGroupFenceSQL = `UPDATE im_team_group_fences SET closed_through_generation = ?
WHERE team_id = ? AND user_id = ? AND closed_through_generation = ?`
const deleteTeamGroupMembershipsSQL = "DELETE gm FROM group_members AS gm JOIN `groups` AS g ON g.id = gm.group_id\n" +
	"WHERE g.team_id = ? AND gm.user_id = ?"

func validateTeamGroupFenceInput(ctx context.Context, db *gorm.DB, teamID, userID, generation int64) error {
	if ctx == nil || teamID <= 0 || userID <= 0 || generation <= 0 {
		return status.Error(codes.InvalidArgument, "positive team, user and generation with a context required")
	}
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if db == nil {
		return status.Error(codes.Unavailable, "team group fence storage is not configured")
	}
	return nil
}

func teamGroupFenceDBError(ctx context.Context) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	return status.Error(codes.Unavailable, "team group fence storage unavailable")
}

// The caller owns a local transaction. Never call a remote service while this
// pair is locked. Initialization is a no-op on conflict and cannot reopen it.
func lockTeamGroupFence(ctx context.Context, tx *gorm.DB, teamID, userID int64) (model.TeamGroupFence, error) {
	var fence model.TeamGroupFence
	if tx.Exec(initializeTeamGroupFenceSQL, teamID, userID).Error != nil || ctx.Err() != nil {
		return fence, teamGroupFenceDBError(ctx)
	}
	rows, err := tx.Raw(lockTeamGroupFenceSQL, teamID, userID).Rows()
	if err != nil {
		return fence, teamGroupFenceDBError(ctx)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		if found || rows.Scan(&fence.TeamID, &fence.UserID, &fence.ClosedThroughGeneration) != nil ||
			fence.TeamID != teamID || fence.UserID != userID || fence.ClosedThroughGeneration < 0 {
			return model.TeamGroupFence{}, teamGroupFenceDBError(ctx)
		}
		found = true
	}
	if !found || rows.Err() != nil || ctx.Err() != nil {
		return model.TeamGroupFence{}, teamGroupFenceDBError(ctx)
	}
	return fence, nil
}

// Only a generation beyond the permanent closed boundary may write. This is a
// package-local primitive, not caller authorization or a production RPC entry.
func withTeamGroupGeneration(ctx context.Context, db *gorm.DB, teamID, userID, generation int64, write func(*gorm.DB) error) error {
	if err := validateTeamGroupFenceInput(ctx, db, teamID, userID, generation); err != nil {
		return err
	}
	if write == nil {
		return status.Error(codes.InvalidArgument, "team group generation write callback required")
	}
	var businessErr error
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		fence, err := lockTeamGroupFence(ctx, tx, teamID, userID)
		if err != nil {
			return err
		}
		if generation <= fence.ClosedThroughGeneration {
			businessErr = status.Error(codes.PermissionDenied, "team group membership generation is closed")
			return businessErr
		}
		if err := write(tx); err != nil {
			// Keep a callback's business code while never exposing its text.
			code := status.Code(err)
			if errors.Is(err, context.Canceled) {
				code = codes.Canceled
			} else if errors.Is(err, context.DeadlineExceeded) {
				code = codes.DeadlineExceeded
			}
			if code == codes.Unknown || code == codes.OK {
				code = codes.Unavailable
			}
			businessErr = status.Error(code, "team group generation write failed")
			return businessErr
		}
		return ctx.Err()
	})
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if err != nil {
		if businessErr != nil {
			return businessErr
		}
		return teamGroupFenceDBError(ctx)
	}
	return nil
}

// Closing and removing group memberships share the same local transaction as
// the write guard's lock. Older retries cannot touch a later generation's rows.
// Messages, offline deliveries and per-message reading receipts are preserved.
func closeTeamGroupMemberships(ctx context.Context, db *gorm.DB, teamID, userID, generation int64) (int64, error) {
	if err := validateTeamGroupFenceInput(ctx, db, teamID, userID, generation); err != nil {
		return 0, err
	}
	var closed int64
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		fence, err := lockTeamGroupFence(ctx, tx, teamID, userID)
		if err != nil {
			return err
		}
		closed = fence.ClosedThroughGeneration
		if generation <= closed {
			return nil
		}
		advanced := tx.Exec(advanceTeamGroupFenceSQL, generation, teamID, userID, closed)
		if advanced.Error != nil || advanced.RowsAffected != 1 || ctx.Err() != nil {
			return teamGroupFenceDBError(ctx)
		}
		deleted := tx.Exec(deleteTeamGroupMembershipsSQL, teamID, userID)
		if deleted.Error != nil || deleted.RowsAffected < 0 || ctx.Err() != nil {
			return teamGroupFenceDBError(ctx)
		}
		closed = generation
		return nil
	})
	if err != nil || ctx.Err() != nil {
		return 0, teamGroupFenceDBError(ctx)
	}
	return closed, nil
}
