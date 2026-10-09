package main

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/task/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type taskNotificationReadMarker interface {
	MarkTaskNotificationRead(context.Context, *pb.MarkTaskNotificationReadRequest, ...grpc.CallOption) (*pb.MarkTaskNotificationReadResponse, error)
}

type taskNotificationReadResponse struct {
	Code int                       `json:"code"`
	Msg  string                    `json:"msg"`
	Data *taskNotificationReadData `json:"data,omitempty"`
}

type taskNotificationReadData struct {
	NotificationID int64 `json:"notification_id,string"`
	ReadAtUnixMs   int64 `json:"read_at_unix_ms,string"`
}

func markTaskNotificationReadHandler(client taskNotificationReadMarker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeError := func(httpStatus, code int, message string) {
			httpx.WriteJson(w, httpStatus, taskNotificationReadResponse{Code: code, Msg: message})
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
		teamID, validTeam := parseTaskNotificationDecimal(vars["team_id"])
		notificationID, validNotification := parseTaskNotificationDecimal(vars["notification_id"])
		if !validTeam || teamID <= 0 || !validNotification || notificationID <= 0 {
			writeError(http.StatusBadRequest, errcode.ErrBadRequest, "invalid notification read request")
			return
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			writeError(http.StatusBadRequest, errcode.ErrBadRequest, "invalid notification read request")
			return
		}
		if r.ContentLength > 0 {
			writeError(http.StatusBadRequest, errcode.ErrBadRequest, "invalid notification read request")
			return
		}
		if r.Body != nil {
			body, err := io.ReadAll(io.LimitReader(r.Body, 1))
			if err != nil || len(body) != 0 {
				writeError(http.StatusBadRequest, errcode.ErrBadRequest, "invalid notification read request")
				return
			}
		}
		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer "+parts[1]))
		result, err := client.MarkTaskNotificationRead(ctx, &pb.MarkTaskNotificationReadRequest{TeamId: teamID, NotificationId: notificationID})
		if err != nil {
			httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "task service error"
			switch status.Code(err) {
			case codes.InvalidArgument:
				httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid notification read request"
			case codes.Unauthenticated:
				httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
			case codes.PermissionDenied:
				httpStatus, code, message = http.StatusForbidden, errcode.ErrForbidden, "team membership required"
			case codes.NotFound:
				httpStatus, code, message = http.StatusNotFound, errcode.ErrNotFound, "notification not found"
			case codes.Unavailable:
				httpStatus, message = http.StatusServiceUnavailable, "task service unavailable"
			case codes.DeadlineExceeded:
				httpStatus, message = http.StatusGatewayTimeout, "task service timeout"
			}
			writeError(httpStatus, code, message)
			return
		}
		if result == nil || result.NotificationId != notificationID || result.ReadAtUnixMs <= 0 || result.ReadAtUnixMs > 253402300799999 {
			writeError(http.StatusBadGateway, errcode.ErrInternal, "invalid task service response")
			return
		}
		httpx.WriteJson(w, http.StatusOK, taskNotificationReadResponse{
			Code: errcode.Success,
			Msg:  "success",
			Data: &taskNotificationReadData{NotificationID: result.NotificationId, ReadAtUnixMs: result.ReadAtUnixMs},
		})
	}
}
