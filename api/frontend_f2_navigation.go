package main

import (
	"context"
	"net/http"
	"net/url"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type f2Response struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data,omitempty"`
}

func f2Error(w http.ResponseWriter, httpStatus, code int, message string) {
	httpx.WriteJson(w, httpStatus, f2Response{Code: code, Msg: message})
}

func f2RPCError(w http.ResponseWriter, err error, service string) {
	httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, service+" service error"
	switch status.Code(err) {
	case codes.InvalidArgument:
		httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid navigation request"
	case codes.Unauthenticated:
		httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
	case codes.PermissionDenied:
		httpStatus, code, message = http.StatusForbidden, errcode.ErrForbidden, "access denied"
	case codes.NotFound:
		httpStatus, code, message = http.StatusNotFound, errcode.ErrBadRequest, "conversation not found"
	case codes.Unavailable:
		httpStatus, message = http.StatusServiceUnavailable, service+" service unavailable"
	case codes.DeadlineExceeded:
		httpStatus, message = http.StatusGatewayTimeout, service+" service timeout"
	}
	f2Error(w, httpStatus, code, message)
}

func f2Token(w http.ResponseWriter, r *http.Request) (string, bool) {
	token, ok := offlineBearerToken(r)
	if !ok {
		f2Error(w, http.StatusUnauthorized, errcode.ErrUnAuth, "login required")
	}
	return token, ok
}

func f2Query(w http.ResponseWriter, r *http.Request, allowed ...string) (url.Values, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || r.URL.ForceQuery {
		f2Error(w, http.StatusBadRequest, errcode.ErrBadRequest, "invalid pagination")
		return nil, false
	}
	for key, values := range query {
		valid := false
		for _, name := range allowed {
			if key == name {
				valid = true
				break
			}
		}
		if !valid || len(values) != 1 {
			f2Error(w, http.StatusBadRequest, errcode.ErrBadRequest, "invalid pagination")
			return nil, false
		}
	}
	return query, true
}

func f2Decimal(query url.Values, name string, max int64) (int64, bool) {
	values, exists := query[name]
	if !exists {
		return 0, true
	}
	value, ok := parseTaskNotificationDecimal(values[0])
	return value, ok && value <= max
}

func f2Limit(query url.Values) (int32, bool) {
	limit, ok := f2Decimal(query, "limit", 100)
	if !ok || (query.Has("limit") && limit == 0) {
		return 0, false
	}
	return int32(limit), true
}

type f2TeamData struct {
	TeamID int64  `json:"team_id,string"`
	Name   string `json:"name"`
	Role   int32  `json:"role"`
}

func listMyTeamsHandler(client userpb.UserClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := f2Token(w, r)
		if !ok {
			return
		}
		query, ok := f2Query(w, r, "after_team_id", "limit")
		if !ok {
			return
		}
		after, valid := f2Decimal(query, "after_team_id", int64(^uint64(0)>>1))
		limit, validLimit := f2Limit(query)
		if !valid || !validLimit {
			f2Error(w, 400, errcode.ErrBadRequest, "invalid pagination")
			return
		}
		if client == nil {
			f2RPCError(w, status.Error(codes.Unavailable, ""), "user")
			return
		}
		ctx, cancel := directContext(r, token)
		defer cancel()
		result, err := client.ListMyTeams(ctx, &userpb.ListMyTeamsRequest{AfterTeamId: after, Limit: limit})
		if err != nil {
			f2RPCError(w, err, "user")
			return
		}
		if result == nil || result.GetNextAfterTeamId() < 0 || len(result.GetTeams()) > 100 {
			f2Error(w, 502, errcode.ErrInternal, "invalid user service response")
			return
		}
		teams := make([]f2TeamData, 0, len(result.GetTeams()))
		last := after
		for _, team := range result.GetTeams() {
			if team == nil || team.GetTeamId() <= last || team.GetName() == "" || team.GetRole() < 0 || team.GetRole() > 2 {
				f2Error(w, 502, errcode.ErrInternal, "invalid user service response")
				return
			}
			teams = append(teams, f2TeamData{TeamID: team.TeamId, Name: team.Name, Role: team.Role})
			last = team.TeamId
		}
		pageLimit := int(limit)
		if pageLimit == 0 {
			pageLimit = 20
		}
		if len(teams) > pageLimit || (result.NextAfterTeamId > 0 && (len(teams) == 0 || result.NextAfterTeamId != last)) {
			f2Error(w, 502, errcode.ErrInternal, "invalid user service response")
			return
		}
		httpx.WriteJson(w, 200, f2Response{Code: errcode.Success, Msg: "success", Data: struct {
			Teams           []f2TeamData `json:"teams"`
			NextAfterTeamID int64        `json:"next_after_team_id,string"`
		}{teams, result.NextAfterTeamId}})
	}
}

type f2GroupData struct {
	GroupID int64  `json:"group_id,string"`
	Name    string `json:"name"`
	OwnerID int64  `json:"owner_id,string"`
	Joined  bool   `json:"joined"`
}

func getTeamGroupHandler(client impb.IMClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := f2Token(w, r)
		if !ok {
			return
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			f2Error(w, 400, errcode.ErrBadRequest, "invalid group request")
			return
		}
		vars := pathvar.Vars(r)
		teamID, teamOK := parseTaskNotificationDecimal(vars["team_id"])
		groupID, groupOK := parseTaskNotificationDecimal(vars["group_id"])
		if !teamOK || !groupOK || teamID <= 0 || groupID <= 0 {
			f2Error(w, 400, errcode.ErrBadRequest, "invalid group ID")
			return
		}
		if client == nil {
			f2RPCError(w, status.Error(codes.Unavailable, ""), "IM")
			return
		}
		ctx, cancel := directContext(r, token)
		defer cancel()
		result, err := client.GetTeamGroup(ctx, &impb.GetTeamGroupRequest{TeamId: teamID, GroupId: groupID})
		if err != nil {
			f2RPCError(w, err, "IM")
			return
		}
		group := result.GetGroup()
		if group == nil || group.GetGroupId() != groupID || group.GetOwnerId() <= 0 || group.GetName() == "" {
			f2Error(w, 502, errcode.ErrInternal, "invalid IM service response")
			return
		}
		httpx.WriteJson(w, 200, f2Response{Code: errcode.Success, Msg: "success", Data: struct {
			Group f2GroupData `json:"group"`
		}{f2GroupData{group.GroupId, group.Name, group.OwnerId, group.Joined}}})
	}
}

type f2DirectData struct {
	PeerID        int64  `json:"peer_id,string"`
	DisplayName   string `json:"display_name"`
	LastMessageID int64  `json:"last_message_id,string"`
}

func f2Names(ctx context.Context, client userpb.UserClient, peers []int64) (map[int64]string, error) {
	names := make(map[int64]string, len(peers))
	if len(peers) == 0 {
		return names, nil
	}
	if client == nil {
		return nil, status.Error(codes.Unavailable, "user service unavailable")
	}
	result, err := client.BatchGetConversationDisplayNames(ctx, &userpb.BatchGetConversationDisplayNamesRequest{UserIds: peers})
	if err != nil {
		return nil, err
	}
	if result == nil || len(result.GetUsers()) > len(peers) {
		return nil, status.Error(codes.Internal, "invalid display names")
	}
	allowed := make(map[int64]bool, len(peers))
	for _, peer := range peers {
		allowed[peer] = true
	}
	for _, user := range result.GetUsers() {
		if user == nil || !allowed[user.GetUserId()] || names[user.GetUserId()] != "" || user.GetDisplayName() == "" {
			return nil, status.Error(codes.Internal, "invalid display names")
		}
		names[user.UserId] = user.DisplayName
	}
	return names, nil
}

func f2NamedConversation(c *impb.DirectConversation, names map[int64]string) f2DirectData {
	name := names[c.PeerId]
	if name == "" {
		name = "已停用成员"
	}
	return f2DirectData{c.PeerId, name, c.LastMessageId}
}

func listMyDirectConversationsHandler(im impb.IMClient, users userpb.UserClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := f2Token(w, r)
		if !ok {
			return
		}
		query, ok := f2Query(w, r, "snapshot_upper_message_id", "before_last_message_id", "limit")
		if !ok {
			return
		}
		const maxID = int64(^uint64(0) >> 1)
		snapshot, validSnapshot := f2Decimal(query, "snapshot_upper_message_id", maxID)
		before, validBefore := f2Decimal(query, "before_last_message_id", maxID)
		limit, validLimit := f2Limit(query)
		if !validSnapshot || !validBefore || !validLimit || (snapshot == 0) != (before == 0) || (before > 0 && before > snapshot) {
			f2Error(w, 400, errcode.ErrBadRequest, "invalid pagination")
			return
		}
		if im == nil {
			f2RPCError(w, status.Error(codes.Unavailable, ""), "IM")
			return
		}
		ctx, cancel := directContext(r, token)
		defer cancel()
		result, err := im.ListMyDirectConversations(ctx, &impb.ListMyDirectConversationsRequest{SnapshotUpperMessageId: snapshot, BeforeLastMessageId: before, Limit: limit})
		if err != nil {
			f2RPCError(w, err, "IM")
			return
		}
		if result == nil || result.GetSnapshotUpperMessageId() < snapshot || result.GetNextBeforeLastMessageId() < 0 || len(result.GetConversations()) > 100 {
			f2Error(w, 502, errcode.ErrInternal, "invalid IM service response")
			return
		}
		if snapshot > 0 && result.GetSnapshotUpperMessageId() != snapshot {
			f2Error(w, 502, errcode.ErrInternal, "invalid IM service response")
			return
		}
		peers := make([]int64, 0, len(result.Conversations))
		seen := make(map[int64]bool, len(result.Conversations))
		last := before
		for _, c := range result.Conversations {
			if c == nil || c.PeerId <= 0 || seen[c.PeerId] || c.LastMessageId <= 0 || (last > 0 && c.LastMessageId >= last) || c.LastMessageId > result.SnapshotUpperMessageId {
				f2Error(w, 502, errcode.ErrInternal, "invalid IM service response")
				return
			}
			seen[c.PeerId] = true
			peers = append(peers, c.PeerId)
			last = c.LastMessageId
		}
		pageLimit := int(limit)
		if pageLimit == 0 {
			pageLimit = 20
		}
		if len(result.Conversations) > pageLimit {
			f2Error(w, 502, errcode.ErrInternal, "invalid IM service response")
			return
		}
		if result.NextBeforeLastMessageId > 0 && (len(result.Conversations) == 0 || result.NextBeforeLastMessageId != last) {
			f2Error(w, 502, errcode.ErrInternal, "invalid IM service response")
			return
		}
		names, err := f2Names(ctx, users, peers)
		if err != nil {
			f2RPCError(w, err, "user")
			return
		}
		data := make([]f2DirectData, 0, len(peers))
		for _, c := range result.Conversations {
			data = append(data, f2NamedConversation(c, names))
		}
		httpx.WriteJson(w, 200, f2Response{Code: errcode.Success, Msg: "success", Data: struct {
			Conversations           []f2DirectData `json:"conversations"`
			SnapshotUpperMessageID  int64          `json:"snapshot_upper_message_id,string"`
			NextBeforeLastMessageID int64          `json:"next_before_last_message_id,string"`
		}{data, result.SnapshotUpperMessageId, result.NextBeforeLastMessageId}})
	}
}

func getMyDirectConversationHandler(im impb.IMClient, users userpb.UserClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := f2Token(w, r)
		if !ok {
			return
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			f2Error(w, 400, errcode.ErrBadRequest, "invalid conversation request")
			return
		}
		peer, valid := parseTaskNotificationDecimal(pathvar.Vars(r)["peer_id"])
		if !valid || peer <= 0 {
			f2Error(w, 400, errcode.ErrBadRequest, "invalid peer ID")
			return
		}
		if im == nil {
			f2RPCError(w, status.Error(codes.Unavailable, ""), "IM")
			return
		}
		ctx, cancel := directContext(r, token)
		defer cancel()
		result, err := im.GetMyDirectConversation(ctx, &impb.GetMyDirectConversationRequest{PeerId: peer})
		if err != nil {
			f2RPCError(w, err, "IM")
			return
		}
		if result == nil || result.GetConversation() == nil || result.Conversation.PeerId != peer || result.Conversation.LastMessageId <= 0 {
			f2Error(w, 502, errcode.ErrInternal, "invalid IM service response")
			return
		}
		names, err := f2Names(ctx, users, []int64{peer})
		if err != nil {
			f2RPCError(w, err, "user")
			return
		}
		httpx.WriteJson(w, 200, f2Response{Code: errcode.Success, Msg: "success", Data: struct {
			Conversation f2DirectData `json:"conversation"`
		}{f2NamedConversation(result.Conversation, names)}})
	}
}
