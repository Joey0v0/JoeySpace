package main

import (
	"net/http"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type unreadConversationData struct {
	ChatType              int32  `json:"chat_type"`
	TeamID                int64  `json:"team_id,string"`
	GroupID               int64  `json:"group_id,string"`
	PeerID                int64  `json:"peer_id,string"`
	GroupName             string `json:"group_name"`
	DisplayName           string `json:"display_name"`
	LastMessageID         int64  `json:"last_message_id,string"`
	LastMessageTimeUnixMs int64  `json:"last_message_time_unix_ms"`
	Preview               string `json:"preview"`
	UnreadCount           int64  `json:"unread_count,string"`
	MentionUnreadCount    int64  `json:"mention_unread_count,string"`
}

func listMyUnreadConversationsHandler(im impb.IMClient, users userpb.UserClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := f2Token(w, r)
		if !ok {
			return
		}
		query, ok := f2Query(w, r, "snapshot_upper_message_id", "before_last_message_id", "limit", "mentions_only")
		if !ok {
			return
		}
		const maxID = int64(^uint64(0) >> 1)
		snapshot, validSnapshot := f2Decimal(query, "snapshot_upper_message_id", maxID)
		before, validBefore := f2Decimal(query, "before_last_message_id", maxID)
		limit, validLimit := f2Decimal(query, "limit", 50)
		mentionsOnly := false
		validMentions := true
		if query.Has("mentions_only") {
			mentionsOnly = query.Get("mentions_only") == "1"
			validMentions = mentionsOnly || query.Get("mentions_only") == "0"
		}
		if !validSnapshot || !validBefore || !validLimit || (query.Has("limit") && limit == 0) ||
			!validMentions || (snapshot == 0) != (before == 0) || (before > 0 && before > snapshot) {
			f2Error(w, http.StatusBadRequest, errcode.ErrBadRequest, "invalid pagination")
			return
		}
		if im == nil {
			f2RPCError(w, status.Error(codes.Unavailable, ""), "IM")
			return
		}
		ctx, cancel := directContext(r, token)
		defer cancel()
		result, err := im.ListMyUnreadConversations(ctx, &impb.ListMyUnreadConversationsRequest{
			SnapshotUpperMessageId: snapshot, BeforeLastMessageId: before, Limit: int32(limit), MentionsOnly: mentionsOnly,
		})
		if err != nil {
			f2RPCError(w, err, "IM")
			return
		}
		pageLimit := int(limit)
		if pageLimit == 0 {
			pageLimit = 20
		}
		if result == nil || result.GetSnapshotUpperMessageId() < snapshot ||
			(snapshot > 0 && result.GetSnapshotUpperMessageId() != snapshot) ||
			result.GetNextBeforeLastMessageId() < 0 || len(result.GetConversations()) > pageLimit {
			f2Error(w, http.StatusBadGateway, errcode.ErrInternal, "invalid IM service response")
			return
		}
		rows := make([]unreadConversationData, 0, len(result.Conversations))
		peers := make([]int64, 0, len(result.Conversations))
		seen := make(map[[2]int64]bool, len(result.Conversations))
		last := before
		for _, conversation := range result.Conversations {
			if conversation == nil || conversation.LastMessageId <= 0 ||
				conversation.LastMessageId > result.SnapshotUpperMessageId ||
				(last > 0 && conversation.LastMessageId >= last) ||
				conversation.LastMessageTimeUnixMs < 0 || conversation.UnreadCount < 0 ||
				conversation.MentionUnreadCount < 0 || conversation.MentionUnreadCount > conversation.UnreadCount {
				f2Error(w, http.StatusBadGateway, errcode.ErrInternal, "invalid IM service response")
				return
			}
			var key [2]int64
			switch conversation.ChatType {
			case 1:
				if mentionsOnly || conversation.PeerId <= 0 || conversation.TeamId != 0 || conversation.GroupId != 0 ||
					conversation.GroupName != "" || conversation.MentionUnreadCount != 0 {
					f2Error(w, http.StatusBadGateway, errcode.ErrInternal, "invalid IM service response")
					return
				}
				key = [2]int64{1, conversation.PeerId}
				peers = append(peers, conversation.PeerId)
			case 2:
				if conversation.TeamId <= 0 || conversation.GroupId <= 0 || conversation.PeerId != 0 ||
					conversation.GroupName == "" || (mentionsOnly && conversation.MentionUnreadCount == 0) {
					f2Error(w, http.StatusBadGateway, errcode.ErrInternal, "invalid IM service response")
					return
				}
				key = [2]int64{2, conversation.GroupId}
			default:
				f2Error(w, http.StatusBadGateway, errcode.ErrInternal, "invalid IM service response")
				return
			}
			if seen[key] {
				f2Error(w, http.StatusBadGateway, errcode.ErrInternal, "invalid IM service response")
				return
			}
			seen[key] = true
			rows = append(rows, unreadConversationData{
				ChatType: conversation.ChatType, TeamID: conversation.TeamId, GroupID: conversation.GroupId,
				PeerID: conversation.PeerId, GroupName: conversation.GroupName,
				LastMessageID: conversation.LastMessageId, LastMessageTimeUnixMs: conversation.LastMessageTimeUnixMs,
				Preview: conversation.Preview, UnreadCount: conversation.UnreadCount, MentionUnreadCount: conversation.MentionUnreadCount,
			})
			last = conversation.LastMessageId
		}
		if result.NextBeforeLastMessageId > 0 && (len(rows) == 0 || result.NextBeforeLastMessageId != last) {
			f2Error(w, http.StatusBadGateway, errcode.ErrInternal, "invalid IM service response")
			return
		}
		names, err := f2Names(ctx, users, peers)
		if err != nil {
			f2RPCError(w, err, "user")
			return
		}
		for i := range rows {
			if rows[i].ChatType == 1 {
				rows[i].DisplayName = names[rows[i].PeerID]
				if rows[i].DisplayName == "" {
					rows[i].DisplayName = "已停用成员"
				}
			}
		}
		httpx.WriteJson(w, http.StatusOK, f2Response{Code: errcode.Success, Msg: "success", Data: struct {
			Conversations           []unreadConversationData `json:"conversations"`
			SnapshotUpperMessageID  int64                    `json:"snapshot_upper_message_id,string"`
			NextBeforeLastMessageID int64                    `json:"next_before_last_message_id,string"`
		}{rows, result.SnapshotUpperMessageId, result.NextBeforeLastMessageId}})
	}
}
