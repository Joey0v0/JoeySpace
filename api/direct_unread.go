package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/im/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type directHTTPResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data,omitempty"`
}

type directMessageData struct {
	ID              int64  `json:"id,string"`
	MsgID           string `json:"msg_id"`
	FromID          int64  `json:"from_id,string"`
	ToID            int64  `json:"to_id,string"`
	ContentType     int32  `json:"content_type"`
	Content         string `json:"content"`
	CreatedAtUnixMs int64  `json:"created_at_unix_ms"`
}

type directHistoryData struct {
	Messages            []directMessageData `json:"messages"`
	NextBeforeMessageID int64               `json:"next_before_message_id,string"`
}

type directUnreadData struct {
	PeerID      int64    `json:"peer_id,string"`
	UnreadCount int64    `json:"unread_count,string"`
	MessageIDs  []string `json:"message_ids,omitempty"`
}

func directScope(w http.ResponseWriter, r *http.Request, allowQuery bool) (string, int64, bool) {
	token, ok := offlineBearerToken(r)
	if !ok {
		directError(w, http.StatusUnauthorized, errcode.ErrUnAuth, "login required")
		return "", 0, false
	}
	peerID, valid := parseTaskNotificationDecimal(pathvar.Vars(r)["peer_id"])
	if !valid || peerID <= 0 || (!allowQuery && (r.URL.RawQuery != "" || r.URL.ForceQuery)) {
		directError(w, http.StatusBadRequest, errcode.ErrBadRequest, "invalid direct message request")
		return "", 0, false
	}
	return token, peerID, true
}

func directContext(r *http.Request, token string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token)), cancel
}

func listDirectMessagesHandler(client pb.IMClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, peerID, ok := directScope(w, r, true)
		if !ok {
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || r.URL.ForceQuery || len(query) > 2 {
			directError(w, http.StatusBadRequest, errcode.ErrBadRequest, "invalid pagination")
			return
		}
		for key, values := range query {
			if (key != "before_message_id" && key != "limit") || len(values) != 1 {
				directError(w, http.StatusBadRequest, errcode.ErrBadRequest, "invalid pagination")
				return
			}
		}
		var before int64
		if raw, exists := query["before_message_id"]; exists {
			before, ok = parseTaskNotificationDecimal(raw[0])
			if !ok {
				directError(w, http.StatusBadRequest, errcode.ErrBadRequest, "invalid pagination")
				return
			}
		}
		var limit int64
		if raw, exists := query["limit"]; exists {
			limit, ok = parseTaskNotificationDecimal(raw[0])
			if !ok || limit < 1 || limit > 100 {
				directError(w, http.StatusBadRequest, errcode.ErrBadRequest, "invalid pagination")
				return
			}
		}
		if client == nil {
			directRPCError(w, status.Error(codes.Unavailable, ""))
			return
		}
		ctx, cancel := directContext(r, token)
		defer cancel()
		result, err := client.ListDirectMessages(ctx, &pb.ListDirectMessagesRequest{PeerId: peerID, BeforeMessageId: before, Limit: int32(limit)})
		if err != nil {
			directRPCError(w, err)
			return
		}
		if result == nil || result.GetNextBeforeMessageId() < 0 || len(result.GetMessages()) > 100 {
			directError(w, http.StatusBadGateway, errcode.ErrInternal, "invalid IM service response")
			return
		}
		data := directHistoryData{Messages: make([]directMessageData, 0, len(result.GetMessages())), NextBeforeMessageID: result.GetNextBeforeMessageId()}
		for _, message := range result.GetMessages() {
			if message == nil || message.GetId() <= 0 || message.GetFromId() <= 0 || message.GetToId() <= 0 || (message.GetFromId() != peerID && message.GetToId() != peerID) {
				directError(w, http.StatusBadGateway, errcode.ErrInternal, "invalid IM service response")
				return
			}
			data.Messages = append(data.Messages, directMessageData{ID: message.Id, MsgID: message.MsgId, FromID: message.FromId, ToID: message.ToId, ContentType: message.ContentType, Content: message.Content, CreatedAtUnixMs: message.CreatedAtUnixMs})
		}
		httpx.WriteJson(w, http.StatusOK, directHTTPResponse{Code: errcode.Success, Msg: "success", Data: data})
	}
}

func getDirectUnreadHandler(client pb.IMClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, peerID, ok := directScope(w, r, false)
		if !ok {
			return
		}
		if client == nil {
			directRPCError(w, status.Error(codes.Unavailable, ""))
			return
		}
		ctx, cancel := directContext(r, token)
		defer cancel()
		result, err := client.GetDirectUnread(ctx, &pb.GetDirectUnreadRequest{PeerId: peerID})
		if err != nil {
			directRPCError(w, err)
			return
		}
		if result == nil || result.PeerId != peerID || result.UnreadCount < 0 {
			directError(w, http.StatusBadGateway, errcode.ErrInternal, "invalid IM service response")
			return
		}
		httpx.WriteJson(w, http.StatusOK, directHTTPResponse{Code: errcode.Success, Msg: "success", Data: directUnreadData{PeerID: peerID, UnreadCount: result.UnreadCount}})
	}
}

func markDirectMessagesReadHandler(client pb.IMClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, peerID, ok := directScope(w, r, false)
		if !ok {
			return
		}
		var body struct {
			MessageIDs []string `json:"message_ids"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&body) != nil || decoder.Decode(&struct{}{}) != io.EOF || len(body.MessageIDs) == 0 || len(body.MessageIDs) > 100 {
			directError(w, http.StatusBadRequest, errcode.ErrBadRequest, "invalid direct read request")
			return
		}
		ids := make([]int64, 0, len(body.MessageIDs))
		requested := make(map[int64]struct{}, len(body.MessageIDs))
		for _, raw := range body.MessageIDs {
			id, valid := parseTaskNotificationDecimal(raw)
			if !valid || id <= 0 {
				directError(w, http.StatusBadRequest, errcode.ErrBadRequest, "invalid direct read request")
				return
			}
			if _, exists := requested[id]; !exists {
				requested[id] = struct{}{}
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
		if client == nil {
			directRPCError(w, status.Error(codes.Unavailable, ""))
			return
		}
		ctx, cancel := directContext(r, token)
		defer cancel()
		result, err := client.MarkDirectMessagesRead(ctx, &pb.MarkDirectMessagesReadRequest{PeerId: peerID, MessageIds: ids})
		if err != nil {
			directRPCError(w, err)
			return
		}
		if result == nil || result.PeerId != peerID || result.UnreadCount < 0 || len(result.MessageIds) != len(ids) {
			directError(w, http.StatusBadGateway, errcode.ErrInternal, "invalid IM service response")
			return
		}
		for _, id := range result.MessageIds {
			if id <= 0 {
				directError(w, http.StatusBadGateway, errcode.ErrInternal, "invalid IM service response")
				return
			}
			if _, exists := requested[id]; !exists {
				directError(w, http.StatusBadGateway, errcode.ErrInternal, "invalid IM service response")
				return
			}
			delete(requested, id)
		}
		textIDs := make([]string, len(ids))
		for i, id := range ids {
			textIDs[i] = strconv.FormatInt(id, 10)
		}
		httpx.WriteJson(w, http.StatusOK, directHTTPResponse{Code: errcode.Success, Msg: "success", Data: directUnreadData{PeerID: peerID, UnreadCount: result.UnreadCount, MessageIDs: textIDs}})
	}
}

func directError(w http.ResponseWriter, httpStatus, code int, message string) {
	httpx.WriteJson(w, httpStatus, directHTTPResponse{Code: code, Msg: message})
}

func directRPCError(w http.ResponseWriter, err error) {
	httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "IM service error"
	switch status.Code(err) {
	case codes.InvalidArgument:
		httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid direct message request"
	case codes.Unauthenticated:
		httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
	case codes.PermissionDenied:
		httpStatus, code, message = http.StatusForbidden, errcode.ErrForbidden, "direct message access denied"
	case codes.NotFound:
		httpStatus, code, message = http.StatusNotFound, errcode.ErrBadRequest, "direct messages not found"
	case codes.Unavailable:
		httpStatus, message = http.StatusServiceUnavailable, "IM service unavailable"
	case codes.DeadlineExceeded:
		httpStatus, message = http.StatusGatewayTimeout, "IM service timeout"
	}
	directError(w, httpStatus, code, message)
}
