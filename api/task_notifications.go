package main

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
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

type taskNotificationLister interface {
	ListTaskNotifications(context.Context, *pb.ListTaskNotificationsRequest, ...grpc.CallOption) (*pb.ListTaskNotificationsResponse, error)
}

type taskNotificationsResponse struct {
	Code int                    `json:"code"`
	Msg  string                 `json:"msg"`
	Data *taskNotificationsData `json:"data,omitempty"`
}

type taskNotificationsData struct {
	Notifications            []taskNotificationItem `json:"notifications"`
	NextBeforeNotificationID int64                  `json:"next_before_notification_id,string"`
}

type taskNotificationItem struct {
	NotificationID  int64 `json:"notification_id,string"`
	TaskID          int64 `json:"task_id,string"`
	ActorID         int64 `json:"actor_id,string"`
	FromStatus      int32 `json:"from_status"`
	ToStatus        int32 `json:"to_status"`
	CreatedAtUnixMs int64 `json:"created_at_unix_ms"`
}

// Decimal IDs must not accept signs, whitespace, or other numeric notation.
func parseTaskNotificationDecimal(value string) (int64, bool) {
	if value == "" {
		return 0, false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(value, 10, 64)
	return id, err == nil
}

func listTaskNotificationsHandler(client taskNotificationLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeError := func(httpStatus, code int, message string) {
			httpx.WriteJson(w, httpStatus, taskNotificationsResponse{Code: code, Msg: message})
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
		teamID, valid := parseTaskNotificationDecimal(pathvar.Vars(r)["team_id"])
		if !valid || teamID <= 0 {
			writeError(http.StatusBadRequest, errcode.ErrBadRequest, "invalid team ID")
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			writeError(http.StatusBadRequest, errcode.ErrBadRequest, "invalid pagination")
			return
		}
		for key, values := range query {
			if (key != "limit" && key != "before_notification_id") || len(values) != 1 {
				writeError(http.StatusBadRequest, errcode.ErrBadRequest, "invalid pagination")
				return
			}
		}
		var beforeID int64
		if values, exists := query["before_notification_id"]; exists {
			beforeID, valid = parseTaskNotificationDecimal(values[0])
			if !valid {
				writeError(http.StatusBadRequest, errcode.ErrBadRequest, "invalid pagination")
				return
			}
		}
		limit := int64(20)
		if values, exists := query["limit"]; exists {
			limit, valid = parseTaskNotificationDecimal(values[0])
			if !valid || limit < 1 || limit > 100 {
				writeError(http.StatusBadRequest, errcode.ErrBadRequest, "invalid pagination")
				return
			}
		}
		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer "+parts[1]))
		result, err := client.ListTaskNotifications(ctx, &pb.ListTaskNotificationsRequest{TeamId: teamID, BeforeNotificationId: beforeID, Limit: int32(limit)})
		if err != nil {
			httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "task service error"
			switch status.Code(err) {
			case codes.InvalidArgument:
				httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid task notification list request"
			case codes.Unauthenticated:
				httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
			case codes.PermissionDenied:
				httpStatus, code, message = http.StatusForbidden, errcode.ErrForbidden, "team membership required"
			case codes.Unavailable:
				httpStatus, message = http.StatusServiceUnavailable, "task service unavailable"
			case codes.DeadlineExceeded:
				httpStatus, message = http.StatusGatewayTimeout, "task service timeout"
			}
			writeError(httpStatus, code, message)
			return
		}
		if !validTaskNotificationResult(result, beforeID, int(limit)) {
			writeError(http.StatusBadGateway, errcode.ErrInternal, "invalid task service response")
			return
		}
		data := &taskNotificationsData{Notifications: make([]taskNotificationItem, 0, len(result.Notifications)), NextBeforeNotificationID: result.NextBeforeNotificationId}
		for _, item := range result.Notifications {
			data.Notifications = append(data.Notifications, taskNotificationItem{NotificationID: item.NotificationId, TaskID: item.TaskId, ActorID: item.ActorId, FromStatus: item.FromStatus, ToStatus: item.ToStatus, CreatedAtUnixMs: item.CreatedAtUnixMs})
		}
		httpx.WriteJson(w, http.StatusOK, taskNotificationsResponse{Code: errcode.Success, Msg: "success", Data: data})
	}
}

func validTaskNotificationResult(result *pb.ListTaskNotificationsResponse, beforeID int64, limit int) bool {
	if result == nil || len(result.Notifications) > limit || result.NextBeforeNotificationId < 0 {
		return false
	}
	previousID := beforeID
	for _, item := range result.Notifications {
		if item == nil || item.NotificationId <= 0 || item.TaskId <= 0 || item.ActorId <= 0 ||
			(previousID > 0 && item.NotificationId >= previousID) ||
			item.FromStatus < 0 || item.FromStatus > 2 || item.ToStatus < 0 || item.ToStatus > 2 || item.FromStatus == item.ToStatus ||
			item.CreatedAtUnixMs <= 0 || item.CreatedAtUnixMs > 253402300799999 {
			return false
		}
		previousID = item.NotificationId
	}
	return result.NextBeforeNotificationId == 0 || (len(result.Notifications) == limit && result.NextBeforeNotificationId == previousID)
}
