package main

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/im/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type teamGroupMessageContextResponse struct {
	Code int                          `json:"code"`
	Msg  string                       `json:"msg"`
	Data *teamGroupMessageContextData `json:"data,omitempty"`
}
type teamGroupMessageContextData struct {
	Messages        []teamGroupMessageContextMessage `json:"messages"`
	TargetMessageID int64                            `json:"target_message_id,string"`
}
type teamGroupMessageContextMessage struct {
	teamGroupHistoryMessage
	CreatedAtUnixMs int64 `json:"created_at_unix_ms,string"`
}

func getTeamGroupMessageContextHandler(client pb.IMClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeError := func(httpStatus, code int, message string) {
			httpx.WriteJson(w, httpStatus, teamGroupMessageContextResponse{Code: code, Msg: message})
		}
		headers := r.Header.Values("Authorization")
		if len(headers) != 1 {
			writeError(http.StatusUnauthorized, errcode.ErrUnAuth, "login required")
			return
		}
		parts := strings.Fields(headers[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			writeError(http.StatusUnauthorized, errcode.ErrUnAuth, "login required")
			return
		}
		vars := pathvar.Vars(r)
		ids := make([]int64, 3)
		for i, key := range []string{"team_id", "group_id", "message_id"} {
			value := vars[key]
			valid := value != ""
			for _, c := range value {
				if c < '0' || c > '9' {
					valid = false
					break
				}
			}
			id, err := strconv.ParseInt(value, 10, 64)
			if !valid || err != nil || id <= 0 {
				writeError(http.StatusBadRequest, errcode.ErrBadRequest, "invalid message context IDs")
				return
			}
			ids[i] = id
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(query) != 0 {
			writeError(http.StatusBadRequest, errcode.ErrBadRequest, "invalid message context query")
			return
		}
		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer "+parts[1]))
		result, err := client.GetTeamGroupMessageContext(ctx, &pb.GetTeamGroupMessageContextRequest{TeamId: ids[0], GroupId: ids[1], MessageId: ids[2]})
		if err != nil {
			httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "IM service error"
			switch status.Code(err) {
			case codes.InvalidArgument:
				httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid message context request"
			case codes.Unauthenticated:
				httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
			case codes.PermissionDenied:
				httpStatus, code, message = http.StatusForbidden, errcode.ErrForbidden, "group and team membership required"
			case codes.NotFound:
				httpStatus, code, message = http.StatusNotFound, errcode.ErrGroupNotFound, "source message or team group not found"
			case codes.Unavailable:
				httpStatus, message = http.StatusServiceUnavailable, "IM service unavailable"
			case codes.DeadlineExceeded:
				httpStatus, message = http.StatusGatewayTimeout, "IM service timeout"
			}
			writeError(httpStatus, code, message)
			return
		}
		data, valid := messageContextData(result, ids[2])
		if !valid {
			writeError(http.StatusBadGateway, errcode.ErrInternal, "invalid IM service response")
			return
		}
		httpx.WriteJson(w, http.StatusOK, teamGroupMessageContextResponse{Code: errcode.Success, Msg: "success", Data: data})
	}
}

func messageContextData(result *pb.GetTeamGroupMessageContextResponse, target int64) (*teamGroupMessageContextData, bool) {
	if result == nil || result.TargetMessageId <= 0 || result.TargetMessageId != target || len(result.Messages) < 1 || len(result.Messages) > 41 {
		return nil, false
	}
	data := &teamGroupMessageContextData{TargetMessageID: target, Messages: make([]teamGroupMessageContextMessage, 0, len(result.Messages))}
	var previous int64
	targets := 0
	for _, msg := range result.Messages {
		if msg == nil || msg.Id <= previous || msg.FromId <= 0 || msg.SenderType < 0 || msg.SenderType > 2 || msg.ContentType < 0 || msg.CreatedAtUnixMs < 0 || msg.MsgId == "" {
			return nil, false
		}
		if msg.SenderType == 2 && msg.InitiatorId <= 0 || msg.SenderType != 2 && msg.InitiatorId != 0 {
			return nil, false
		}
		mentioned, valid := mentionIDStrings(msg.MentionedUserIds)
		if !valid {
			return nil, false
		}
		if msg.Id == target {
			targets++
		}
		previous = msg.Id
		sender := msg.SenderType
		if sender == 0 {
			sender = 1
		}
		data.Messages = append(data.Messages, teamGroupMessageContextMessage{teamGroupHistoryMessage: teamGroupHistoryMessage{ID: msg.Id, MsgID: msg.MsgId, FromID: msg.FromId, SenderType: sender, InitiatorID: msg.InitiatorId, ContentType: msg.ContentType, Content: msg.Content, MentionedUserIDs: mentioned}, CreatedAtUnixMs: msg.CreatedAtUnixMs})
	}
	if targets != 1 {
		return nil, false
	}
	return data, true
}
