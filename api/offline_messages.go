package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/im/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type offlineMessagesResponse struct {
	Code int                   `json:"code"`
	Msg  string                `json:"msg"`
	Data *[]offlineMessageData `json:"data,omitempty"`
}

type offlineMessageData struct {
	ID          int64     `json:"id,string"`
	MsgID       string    `json:"msg_id"`
	FromID      int64     `json:"from_id,string"`
	SenderType  int32     `json:"sender_type"`
	InitiatorID int64     `json:"initiator_id,string"`
	ToID        int64     `json:"to_id,string"`
	ChatType    int32     `json:"chat_type"`
	ContentType int32     `json:"content_type"`
	Content     string    `json:"content"`
	CreatedAt   time.Time `json:"created_at"`
}

type offlineAckRequest struct {
	MessageIDs []string `json:"message_ids"`
}

func offlineBearerToken(r *http.Request) (string, bool) {
	headers := r.Header.Values("Authorization")
	if len(headers) != 1 {
		return "", false
	}
	parts := strings.Fields(headers[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	return parts[1], true
}

func listOfflineMessagesHandler(client pb.IMClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := offlineBearerToken(r)
		if !ok {
			httpx.WriteJson(w, http.StatusUnauthorized, offlineMessagesResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer "+token))
		result, err := client.ListOfflineMessages(ctx, &pb.ListOfflineMessagesRequest{})
		if err != nil {
			httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "IM service error"
			switch status.Code(err) {
			case codes.Unauthenticated:
				httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
			case codes.Unavailable:
				httpStatus, message = http.StatusServiceUnavailable, "IM service unavailable"
			case codes.DeadlineExceeded:
				httpStatus, message = http.StatusGatewayTimeout, "IM service timeout"
			}
			httpx.WriteJson(w, httpStatus, offlineMessagesResponse{Code: code, Msg: message})
			return
		}
		messages := make([]offlineMessageData, 0, len(result.GetMessages()))
		for _, message := range result.GetMessages() {
			senderType := message.GetSenderType()
			if senderType == 0 {
				senderType = 1 // Compatibility with IM RPC before sender metadata.
			}
			messages = append(messages, offlineMessageData{
				ID: message.GetId(), MsgID: message.GetMsgId(), FromID: message.GetFromId(), ToID: message.GetToId(),
				SenderType: senderType, InitiatorID: message.GetInitiatorId(),
				ChatType: message.GetChatType(), ContentType: message.GetContentType(),
				Content: message.GetContent(), CreatedAt: message.GetCreatedAt().AsTime(),
			})
		}
		httpx.WriteJson(w, http.StatusOK, offlineMessagesResponse{Code: errcode.Success, Msg: "success", Data: &messages})
	}
}

func ackOfflineMessagesHandler(client pb.IMClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := offlineBearerToken(r)
		if !ok {
			httpx.WriteJson(w, http.StatusUnauthorized, offlineMessagesResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		var body offlineAckRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		if err := decoder.Decode(&body); err != nil {
			httpx.WriteJson(w, http.StatusBadRequest, offlineMessagesResponse{Code: errcode.ErrBadRequest, Msg: "invalid message IDs"})
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF || len(body.MessageIDs) == 0 || len(body.MessageIDs) > 1000 {
			httpx.WriteJson(w, http.StatusBadRequest, offlineMessagesResponse{Code: errcode.ErrBadRequest, Msg: "invalid message IDs"})
			return
		}
		messageIDs := make([]int64, 0, len(body.MessageIDs))
		for _, rawID := range body.MessageIDs {
			messageID, err := strconv.ParseInt(rawID, 10, 64)
			if err != nil || messageID <= 0 {
				httpx.WriteJson(w, http.StatusBadRequest, offlineMessagesResponse{Code: errcode.ErrBadRequest, Msg: "invalid message IDs"})
				return
			}
			messageIDs = append(messageIDs, messageID)
		}
		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer "+token))
		_, err := client.AckOfflineMessages(ctx, &pb.AckOfflineMessagesRequest{MessageIds: messageIDs})
		if err != nil {
			httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "IM service error"
			switch status.Code(err) {
			case codes.InvalidArgument:
				httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid message IDs"
			case codes.Unauthenticated:
				httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
			case codes.Unavailable:
				httpStatus, message = http.StatusServiceUnavailable, "IM service unavailable"
			case codes.DeadlineExceeded:
				httpStatus, message = http.StatusGatewayTimeout, "IM service timeout"
			}
			httpx.WriteJson(w, httpStatus, offlineMessagesResponse{Code: code, Msg: message})
			return
		}
		httpx.WriteJson(w, http.StatusOK, offlineMessagesResponse{Code: errcode.Success, Msg: "success"})
	}
}
