package main

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

// Load only verified relations for already-authorized message IDs. A bounded
// batch prevents offline backlogs from creating an unbounded SQL IN clause.
func loadGroupMentionIDs(ctx context.Context, db *gorm.DB, ids []int64) (map[int64][]int64, error) {
	result := make(map[int64][]int64)
	if len(ids) == 0 {
		return result, nil
	}
	if db == nil {
		return nil, status.Error(codes.Unavailable, "IM database unavailable")
	}
	for start := 0; start < len(ids); start += 500 {
		end := min(start+500, len(ids))
		var rows []struct {
			MessageID       int64
			MentionedUserID int64
		}
		if err := db.WithContext(ctx).Table("im_group_message_mentions").
			Select("message_id, mentioned_user_id").Where("message_id IN ?", ids[start:end]).
			Order("message_id ASC, mentioned_user_id ASC").Find(&rows).Error; err != nil {
			return nil, groupUnreadDBError(ctx)
		}
		allowed := make(map[int64]bool, end-start)
		for _, id := range ids[start:end] {
			allowed[id] = true
		}
		for _, row := range rows {
			if !allowed[row.MessageID] || row.MentionedUserID <= 0 || len(result[row.MessageID]) >= 10 {
				return nil, groupUnreadDBError(ctx)
			}
			result[row.MessageID] = append(result[row.MessageID], row.MentionedUserID)
		}
	}
	return result, nil
}
