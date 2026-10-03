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

type teamGroupHistoryResponse struct {
	Code int                   `json:"code"`
	Msg  string                `json:"msg"`
	Data *teamGroupHistoryData `json:"data,omitempty"`
}

type teamGroupHistoryData struct {
	Messages            []teamGroupHistoryMessage `json:"messages"`
	NextBeforeMessageID int64                     `json:"next_before_message_id,string"`
}

type teamGroupHistoryMessage struct {
	ID              int64  `json:"id,string"`
	MsgID           string `json:"msg_id"`
	FromID          int64  `json:"from_id,string"`
	SenderType      int32  `json:"sender_type"`
	InitiatorID     int64  `json:"initiator_id,string"`
	ContentType     int32  `json:"content_type"`
	Content         string `json:"content"`
	CreatedAtUnixMs int64  `json:"created_at_unix_ms"`
}

func listTeamGroupMessagesHandler(client pb.IMClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		headers := r.Header.Values("Authorization")
		if len(headers) != 1 {
			httpx.WriteJson(w, http.StatusUnauthorized, teamGroupHistoryResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		parts := strings.Fields(headers[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			httpx.WriteJson(w, http.StatusUnauthorized, teamGroupHistoryResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		vars := pathvar.Vars(r)
		teamID, teamErr := strconv.ParseInt(vars["team_id"], 10, 64)
		groupID, groupErr := strconv.ParseInt(vars["group_id"], 10, 64)
		if teamErr != nil || groupErr != nil || teamID <= 0 || groupID <= 0 {
			httpx.WriteJson(w, http.StatusBadRequest, teamGroupHistoryResponse{Code: errcode.ErrBadRequest, Msg: "invalid team or group ID"})
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(query) > 2 {
			httpx.WriteJson(w, http.StatusBadRequest, teamGroupHistoryResponse{Code: errcode.ErrBadRequest, Msg: "invalid pagination"})
			return
		}
		for key := range query {
			if key != "before_message_id" && key != "limit" || len(query[key]) != 1 {
				httpx.WriteJson(w, http.StatusBadRequest, teamGroupHistoryResponse{Code: errcode.ErrBadRequest, Msg: "invalid pagination"})
				return
			}
		}
		var beforeID int64
		if values, ok := query["before_message_id"]; ok {
			beforeID, err = strconv.ParseInt(values[0], 10, 64)
			if err != nil || beforeID < 0 {
				httpx.WriteJson(w, http.StatusBadRequest, teamGroupHistoryResponse{Code: errcode.ErrBadRequest, Msg: "invalid pagination"})
				return
			}
		}
		var limit int64
		if values, ok := query["limit"]; ok {
			limit, err = strconv.ParseInt(values[0], 10, 32)
			if err != nil || limit < 1 || limit > 100 {
				httpx.WriteJson(w, http.StatusBadRequest, teamGroupHistoryResponse{Code: errcode.ErrBadRequest, Msg: "invalid pagination"})
				return
			}
		}
		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer "+parts[1]))
		result, err := client.ListTeamGroupMessages(ctx, &pb.ListTeamGroupMessagesRequest{TeamId: teamID, GroupId: groupID, BeforeMessageId: beforeID, Limit: int32(limit)})
		if err != nil {
			httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "IM service error"
			switch status.Code(err) {
			case codes.InvalidArgument:
				httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid team group history request"
			case codes.Unauthenticated:
				httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
			case codes.PermissionDenied:
				httpStatus, code, message = http.StatusForbidden, errcode.ErrForbidden, "group and team membership required"
			case codes.NotFound:
				httpStatus, code, message = http.StatusNotFound, errcode.ErrGroupNotFound, "team group not found"
			case codes.Unavailable:
				httpStatus, message = http.StatusServiceUnavailable, "IM service unavailable"
			case codes.DeadlineExceeded:
				httpStatus, message = http.StatusGatewayTimeout, "IM service timeout"
			}
			httpx.WriteJson(w, httpStatus, teamGroupHistoryResponse{Code: code, Msg: message})
			return
		}
		data := &teamGroupHistoryData{Messages: make([]teamGroupHistoryMessage, 0, len(result.GetMessages())), NextBeforeMessageID: result.GetNextBeforeMessageId()}
		for _, message := range result.GetMessages() {
			senderType := message.GetSenderType()
			if senderType == 0 {
				senderType = 1 // Older IM RPC responses are ordinary user messages.
			}
			data.Messages = append(data.Messages, teamGroupHistoryMessage{
				ID: message.GetId(), MsgID: message.GetMsgId(), FromID: message.GetFromId(),
				SenderType: senderType, InitiatorID: message.GetInitiatorId(),
				ContentType: message.GetContentType(), Content: message.GetContent(), CreatedAtUnixMs: message.GetCreatedAtUnixMs(),
			})
		}
		httpx.WriteJson(w, http.StatusOK, teamGroupHistoryResponse{Code: errcode.Success, Msg: "success", Data: data})
	}
}
