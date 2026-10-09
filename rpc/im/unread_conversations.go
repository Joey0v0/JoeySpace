package main

import (
	"context"
	"strings"
	"time"
	"unicode"

	snowflake "github.com/bwmarrin/snowflake"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The two arms compute authoritative per-message unread counts in a single
// database query. A conversation's position is its latest message within the
// fixed upper bound, including messages the reader already acknowledged.
const unreadConversationCandidatesSQL = `SELECT c.chat_type, c.team_id, c.group_id, c.peer_id, c.group_name,
 c.last_message_id, c.unread_count, c.closed_generation, m.content, m.content_type, m.created_at
FROM (
 SELECT 1 AS chat_type, 0 AS team_id, 0 AS group_id,
  IF(m.from_id = ?, m.to_id, m.from_id) AS peer_id, '' AS group_name,
  MAX(m.id) AS last_message_id,
  SUM(IF(m.to_id = ? AND NOT EXISTS (
   SELECT 1 FROM im_direct_message_reads AS r
   WHERE r.user_id = ? AND r.peer_id = m.from_id AND r.message_id = m.id), 1, 0)) AS unread_count,
  0 AS closed_generation
 FROM messages AS m
 WHERE m.chat_type = 1 AND m.id <= ? AND
  ((m.from_id = ? AND m.to_id > 0 AND m.to_id <> ?) OR
   (m.to_id = ? AND m.from_id > 0 AND m.from_id <> ?))
 GROUP BY peer_id HAVING unread_count > 0
 UNION ALL
 SELECT 2 AS chat_type, g.team_id, g.id AS group_id, 0 AS peer_id, g.name AS group_name,
  MAX(m.id) AS last_message_id,
  SUM(IF(NOT (m.sender_type IN (0, 1) AND m.from_id = ?) AND NOT EXISTS (
   SELECT 1 FROM im_group_message_reads AS r
   WHERE r.user_id = ? AND r.group_id = g.id AND r.message_id = m.id), 1, 0)) AS unread_count,
  COALESCE(f.closed_through_generation, 0) AS closed_generation
 FROM group_members AS gm
 JOIN ` + "`groups`" + ` AS g ON g.id = gm.group_id AND g.team_id > 0
 JOIN messages AS m ON m.chat_type = 2 AND m.to_id = g.id AND m.id <= ?
 LEFT JOIN im_team_group_fences AS f ON f.team_id = g.team_id AND f.user_id = gm.user_id
 WHERE gm.user_id = ?
 GROUP BY g.team_id, g.id, g.name, f.closed_through_generation HAVING unread_count > 0
) AS c
JOIN messages AS m ON m.id = c.last_message_id
WHERE (? = 0 OR c.last_message_id < ?)
ORDER BY c.last_message_id DESC LIMIT ?`

type unreadConversationCandidate struct {
	ChatType         int32
	TeamID           int64
	GroupID          int64
	PeerID           int64
	GroupName        string
	LastMessageID    int64
	UnreadCount      int64
	ClosedGeneration int64
	Content          string
	ContentType      int32
	CreatedAt        time.Time
}

func (s *imServer) ListMyUnreadConversations(ctx context.Context, req *pb.ListMyUnreadConversationsRequest) (*pb.ListMyUnreadConversationsResponse, error) {
	if s.db == nil || s.jwtSecret == "" || s.teamClient == nil {
		return nil, status.Error(codes.Unavailable, "unread conversation listing is not enabled")
	}
	if req.GetSnapshotUpperMessageId() < 0 || req.GetBeforeLastMessageId() < 0 ||
		req.GetBeforeLastMessageId() > req.GetSnapshotUpperMessageId() ||
		req.GetLimit() < 0 || req.GetLimit() > 50 {
		return nil, status.Error(codes.InvalidArgument, "invalid unread conversation parameters")
	}
	userID, authorization, err := s.authenticatedUser(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetMentionsOnly() {
		return nil, status.Error(codes.Unavailable, "mention unread listing is not enabled")
	}
	upper := req.GetSnapshotUpperMessageId()
	if upper == 0 {
		// Capture an ID ceiling at request time without exposing a global MAX(id)
		// from conversations the caller cannot read. Snowflake's lower bits cover
		// every node and sequence value in this millisecond.
		upper = ((time.Now().UnixMilli() - snowflake.Epoch) << (snowflake.NodeBits + snowflake.StepBits)) |
			((1 << (snowflake.NodeBits + snowflake.StepBits)) - 1)
		if upper <= 0 {
			return nil, status.Error(codes.Unavailable, "unread snapshot unavailable")
		}
	}
	result := &pb.ListMyUnreadConversationsResponse{Conversations: make([]*pb.UnreadConversation, 0), SnapshotUpperMessageId: upper}
	if upper == 0 {
		return result, nil
	}
	limit := int(req.GetLimit())
	if limit == 0 {
		limit = 20
	}
	// A stale group may occupy a candidate slot. Scan successive bounded pages,
	// caching the remote generation by team, until a real lookahead is found.
	type teamState struct {
		generation int64
		allowed    bool
	}
	teams := map[int64]teamState{}
	before := req.GetBeforeLastMessageId()
	for {
		var rows []unreadConversationCandidate
		if err := s.db.WithContext(ctx).Raw(unreadConversationCandidatesSQL,
			userID, userID, userID, upper, userID, userID, userID, userID,
			userID, userID, upper, userID, before, before, 51).Scan(&rows).Error; err != nil {
			return nil, groupUnreadDBError(ctx)
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			if row.LastMessageID <= 0 || row.UnreadCount <= 0 || row.LastMessageID > upper ||
				(before > 0 && row.LastMessageID >= before) {
				return nil, groupUnreadDBError(ctx)
			}
			before = row.LastMessageID
			if row.ChatType == 2 {
				if row.TeamID <= 0 || row.GroupID <= 0 || row.PeerID != 0 || row.GroupName == "" || row.ClosedGeneration < 0 {
					return nil, groupUnreadDBError(ctx)
				}
				state, known := teams[row.TeamID]
				if !known {
					generation, err := s.directoryTeamGeneration(ctx, authorization, row.TeamID, userID)
					if status.Code(err) == codes.PermissionDenied {
						state = teamState{}
					} else if err != nil {
						return nil, err
					} else {
						state = teamState{generation: generation, allowed: true}
					}
					teams[row.TeamID] = state
				}
				if !state.allowed || state.generation <= row.ClosedGeneration {
					continue
				}
			} else if row.ChatType != 1 || row.PeerID <= 0 || row.GroupID != 0 || row.TeamID != 0 || row.GroupName != "" {
				return nil, groupUnreadDBError(ctx)
			}
			if len(result.Conversations) == limit {
				result.NextBeforeLastMessageId = result.Conversations[limit-1].LastMessageId
				break
			}
			result.Conversations = append(result.Conversations, &pb.UnreadConversation{
				ChatType: row.ChatType, TeamId: row.TeamID, GroupId: row.GroupID,
				PeerId: row.PeerID, GroupName: row.GroupName, LastMessageId: row.LastMessageID,
				LastMessageTimeUnixMs: row.CreatedAt.UnixMilli(),
				Preview:               unreadPreview(row.Content, row.ContentType), UnreadCount: row.UnreadCount,
			})
		}
		if result.NextBeforeLastMessageId > 0 || len(rows) < 51 {
			break
		}
	}
	// Confirm all selected group rows still exist and remain beyond their team
	// fence in one database round trip. This catches a leave that happened after
	// the candidate query even if the User check observed a later rejoin.
	groupIDs := make([]int64, 0, len(result.Conversations))
	for _, row := range result.Conversations {
		if row.ChatType == 2 {
			groupIDs = append(groupIDs, row.GroupId)
		}
	}
	if len(groupIDs) > 0 {
		var current []struct {
			GroupID          int64
			TeamID           int64
			ClosedGeneration int64
		}
		if err := s.db.WithContext(ctx).Table("group_members AS gm").
			Select("gm.group_id, g.team_id, COALESCE(f.closed_through_generation, 0) AS closed_generation").
			Joins("JOIN `groups` AS g ON g.id = gm.group_id").
			Joins("LEFT JOIN im_team_group_fences AS f ON f.team_id = g.team_id AND f.user_id = gm.user_id").
			Where("gm.user_id = ? AND gm.group_id IN ?", userID, groupIDs).Find(&current).Error; err != nil {
			return nil, groupUnreadDBError(ctx)
		}
		valid := make(map[int64]int64, len(groupIDs))
		for _, row := range current {
			if valid[row.GroupID] != 0 || row.TeamID <= 0 || teams[row.TeamID].generation <= row.ClosedGeneration {
				return nil, status.Error(codes.PermissionDenied, "team group membership changed")
			}
			valid[row.GroupID] = row.TeamID
		}
		for _, row := range result.Conversations {
			if row.ChatType == 2 && valid[row.GroupId] != row.TeamId {
				return nil, status.Error(codes.PermissionDenied, "team group membership changed")
			}
		}
	}
	// Recheck each team whose data will leave this RPC after the database read.
	// A leave/rejoin changes generation even when the team ID stays the same.
	checked := map[int64]bool{}
	for _, row := range result.Conversations {
		if row.ChatType != 2 || checked[row.TeamId] {
			continue
		}
		checked[row.TeamId] = true
		if err := s.directoryTeamUnchanged(ctx, authorization, row.TeamId, userID, teams[row.TeamId].generation); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func unreadPreview(content string, contentType int32) string {
	if contentType != 1 {
		return "[消息]"
	}
	var b strings.Builder
	count := 0
	for _, r := range content {
		if count == 120 {
			break
		}
		if !unicode.IsPrint(r) {
			r = ' '
		}
		b.WriteRune(r)
		count++
	}
	return b.String()
}
