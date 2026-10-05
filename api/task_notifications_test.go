package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/task/pb"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type taskNotificationsFunc func(context.Context, *pb.ListTaskNotificationsRequest) (*pb.ListTaskNotificationsResponse, error)

func (f taskNotificationsFunc) ListTaskNotifications(ctx context.Context, req *pb.ListTaskNotificationsRequest, _ ...grpc.CallOption) (*pb.ListTaskNotificationsResponse, error) {
	return f(ctx, req)
}

func taskNotificationsHTTPRequest(teamID, query string, headers ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/teams/"+teamID+"/task-notifications"+query, nil)
	r = pathvar.WithVars(r, map[string]string{"team_id": teamID})
	for _, header := range headers {
		r.Header.Add("Authorization", header)
	}
	return r
}

func validHTTPTaskNotification(id int64) *pb.TaskNotificationItem {
	return &pb.TaskNotificationItem{NotificationId: id, TaskId: 9007199254741001, ActorId: 9007199254741003, FromStatus: 0, ToStatus: 1, CreatedAtUnixMs: 1791097200123}
}

func TestListTaskNotificationsHTTPForwardsCursorAndStringIDs(t *testing.T) {
	client := taskNotificationsFunc(func(ctx context.Context, req *pb.ListTaskNotificationsRequest) (*pb.ListTaskNotificationsResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.TeamId != 9007199254740993 || req.BeforeNotificationId != 9007199254740999 || req.Limit != 2 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer original-token" {
			t.Fatalf("wrong RPC request or token: %v, %v", req, md)
		}
		return &pb.ListTaskNotificationsResponse{Notifications: []*pb.TaskNotificationItem{validHTTPTaskNotification(9007199254740997), validHTTPTaskNotification(9007199254740995)}, NextBeforeNotificationId: 9007199254740995}, nil
	})
	w := httptest.NewRecorder()
	listTaskNotificationsHandler(client)(w, taskNotificationsHTTPRequest("9007199254740993", "?before_notification_id=9007199254740999&limit=2", "Bearer original-token"))
	var body struct {
		Code int `json:"code"`
		Data struct {
			Notifications []struct {
				NotificationID  string `json:"notification_id"`
				TaskID          string `json:"task_id"`
				ActorID         string `json:"actor_id"`
				FromStatus      int32  `json:"from_status"`
				ToStatus        int32  `json:"to_status"`
				CreatedAtUnixMs int64  `json:"created_at_unix_ms"`
			} `json:"notifications"`
			NextBeforeNotificationID string `json:"next_before_notification_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || body.Code != errcode.Success || len(body.Data.Notifications) != 2 || body.Data.NextBeforeNotificationID != "9007199254740995" {
		t.Fatalf("notification list: %d %s", w.Code, w.Body.String())
	}
	item := body.Data.Notifications[0]
	if item.NotificationID != "9007199254740997" || item.TaskID != "9007199254741001" || item.ActorID != "9007199254741003" || item.FromStatus != 0 || item.ToStatus != 1 || item.CreatedAtUnixMs != 1791097200123 {
		t.Fatalf("notification IDs and fields: %s", w.Body.String())
	}
}

func TestListTaskNotificationsHTTPDefaultsEmptyAndFinalPages(t *testing.T) {
	for _, tc := range []struct {
		name   string
		query  string
		before int64
		limit  int32
		items  []*pb.TaskNotificationItem
	}{
		{"default empty page", "", 0, 20, nil},
		{"explicit zero cursor", "?before_notification_id=0&limit=1", 0, 1, []*pb.TaskNotificationItem{validHTTPTaskNotification(10)}},
		{"partial final page", "?before_notification_id=20&limit=2", 20, 2, []*pb.TaskNotificationItem{validHTTPTaskNotification(10)}},
		{"maximum page size", "?limit=100", 0, 100, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := taskNotificationsFunc(func(_ context.Context, req *pb.ListTaskNotificationsRequest) (*pb.ListTaskNotificationsResponse, error) {
				if req.BeforeNotificationId != tc.before || req.Limit != tc.limit {
					t.Fatalf("pagination: %v", req)
				}
				return &pb.ListTaskNotificationsResponse{Notifications: tc.items}, nil
			})
			w := httptest.NewRecorder()
			listTaskNotificationsHandler(client)(w, taskNotificationsHTTPRequest("200", tc.query, "Bearer token"))
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"next_before_notification_id":"0"`) || (len(tc.items) == 0 && !strings.Contains(w.Body.String(), `"notifications":[]`)) {
				t.Fatalf("final/empty page: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestListTaskNotificationsHTTPRejectsInvalidInputBeforeRPC(t *testing.T) {
	client := taskNotificationsFunc(func(context.Context, *pb.ListTaskNotificationsRequest) (*pb.ListTaskNotificationsResponse, error) {
		t.Fatal("invalid input reached RPC")
		return nil, nil
	})
	for _, tc := range []struct {
		teamID, query string
		headers       []string
		want          int
	}{
		{"200", "", nil, 401},
		{"200", "", []string{"Bearer a", "Bearer b"}, 401},
		{"200", "", []string{"Basic token"}, 401},
		{"200", "", []string{"Bearer"}, 401},
		{"200", "", []string{"Bearer a b"}, 401},
		{"200", "", []string{"Bearer a, Bearer b"}, 401},
		{"0", "", []string{"Bearer token"}, 400},
		{"-1", "", []string{"Bearer token"}, 400},
		{"bad", "", []string{"Bearer token"}, 400},
		{"+200", "", []string{"Bearer token"}, 400},
		{"9223372036854775808", "", []string{"Bearer token"}, 400},
		{"200", "?before_notification_id=-1", []string{"Bearer token"}, 400},
		{"200", "?before_notification_id=abc", []string{"Bearer token"}, 400},
		{"200", "?before_notification_id=", []string{"Bearer token"}, 400},
		{"200", "?before_notification_id=%2B1", []string{"Bearer token"}, 400},
		{"200", "?before_notification_id=9223372036854775808", []string{"Bearer token"}, 400},
		{"200", "?before_notification_id=1&before_notification_id=2", []string{"Bearer token"}, 400},
		{"200", "?limit=0", []string{"Bearer token"}, 400},
		{"200", "?limit=101", []string{"Bearer token"}, 400},
		{"200", "?limit=", []string{"Bearer token"}, 400},
		{"200", "?limit=-1", []string{"Bearer token"}, 400},
		{"200", "?limit=1.0", []string{"Bearer token"}, 400},
		{"200", "?limit=1e1", []string{"Bearer token"}, 400},
		{"200", "?limit=1&limit=2", []string{"Bearer token"}, 400},
		{"200", "?recipient_id=123", []string{"Bearer token"}, 400},
		{"200", "?unknown=1", []string{"Bearer token"}, 400},
		{"200", "?limit=%zz", []string{"Bearer token"}, 400},
		{"200", "?limit=1;before_notification_id=2", []string{"Bearer token"}, 400},
	} {
		t.Run(tc.teamID+tc.query+strings.Join(tc.headers, "/"), func(t *testing.T) {
			w := httptest.NewRecorder()
			listTaskNotificationsHandler(client)(w, taskNotificationsHTTPRequest(tc.teamID, tc.query, tc.headers...))
			if w.Code != tc.want || strings.Contains(w.Body.String(), "token") || strings.Contains(w.Body.String(), `"data"`) {
				t.Fatalf("invalid input: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestListTaskNotificationsHTTPMapsFailuresSafely(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
		code int
	}{
		{"invalid", status.Error(codes.InvalidArgument, "private-token database detail"), 400, errcode.ErrBadRequest},
		{"unauthenticated", status.Error(codes.Unauthenticated, "private-token database detail"), 401, errcode.ErrUnAuth},
		{"left team", status.Error(codes.PermissionDenied, "private-token database detail"), 403, errcode.ErrForbidden},
		{"unavailable", status.Error(codes.Unavailable, "private-token database detail"), 503, errcode.ErrInternal},
		{"timeout", status.Error(codes.DeadlineExceeded, "private-token database detail"), 504, errcode.ErrInternal},
		{"internal", status.Error(codes.Internal, "private-token database detail"), 502, errcode.ErrInternal},
		{"unknown", errors.New("private-token database detail"), 502, errcode.ErrInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := taskNotificationsFunc(func(context.Context, *pb.ListTaskNotificationsRequest) (*pb.ListTaskNotificationsResponse, error) {
				return nil, tc.err
			})
			w := httptest.NewRecorder()
			listTaskNotificationsHandler(client)(w, taskNotificationsHTTPRequest("200", "", "Bearer private-token"))
			var body taskNotificationsResponse
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.want || body.Code != tc.code || strings.Contains(w.Body.String(), "private-token") || strings.Contains(w.Body.String(), "database detail") || strings.Contains(w.Body.String(), `"data"`) {
				t.Fatalf("RPC failure: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestListTaskNotificationsHTTPRejectsInvalidResults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func(*pb.ListTaskNotificationsResponse) *pb.ListTaskNotificationsResponse
	}{
		{"nil response", func(*pb.ListTaskNotificationsResponse) *pb.ListTaskNotificationsResponse { return nil }},
		{"nil item", func(r *pb.ListTaskNotificationsResponse) *pb.ListTaskNotificationsResponse {
			r.Notifications[0] = nil
			return r
		}},
		{"too many items", func(r *pb.ListTaskNotificationsResponse) *pb.ListTaskNotificationsResponse {
			r.Notifications = append(r.Notifications, validHTTPTaskNotification(7))
			return r
		}},
		{"zero notification ID", func(r *pb.ListTaskNotificationsResponse) *pb.ListTaskNotificationsResponse {
			r.Notifications[0].NotificationId = 0
			return r
		}},
		{"negative notification ID", func(r *pb.ListTaskNotificationsResponse) *pb.ListTaskNotificationsResponse {
			r.Notifications[0].NotificationId = -1
			return r
		}},
		{"zero task ID", func(r *pb.ListTaskNotificationsResponse) *pb.ListTaskNotificationsResponse {
			r.Notifications[0].TaskId = 0
			return r
		}},
		{"negative actor ID", func(r *pb.ListTaskNotificationsResponse) *pb.ListTaskNotificationsResponse {
			r.Notifications[0].ActorId = -1
			return r
		}},
		{"cursor boundary", func(r *pb.ListTaskNotificationsResponse) *pb.ListTaskNotificationsResponse {
			r.Notifications[0].NotificationId = 20
			return r
		}},
		{"duplicate IDs", func(r *pb.ListTaskNotificationsResponse) *pb.ListTaskNotificationsResponse {
			r.Notifications[1].NotificationId = 10
			return r
		}},
		{"ascending IDs", func(r *pb.ListTaskNotificationsResponse) *pb.ListTaskNotificationsResponse {
			r.Notifications[1].NotificationId = 11
			return r
		}},
		{"invalid from status", func(r *pb.ListTaskNotificationsResponse) *pb.ListTaskNotificationsResponse {
			r.Notifications[0].FromStatus = -1
			return r
		}},
		{"invalid to status", func(r *pb.ListTaskNotificationsResponse) *pb.ListTaskNotificationsResponse {
			r.Notifications[0].ToStatus = 3
			return r
		}},
		{"unchanged status", func(r *pb.ListTaskNotificationsResponse) *pb.ListTaskNotificationsResponse {
			r.Notifications[0].ToStatus = 0
			return r
		}},
		{"zero time", func(r *pb.ListTaskNotificationsResponse) *pb.ListTaskNotificationsResponse {
			r.Notifications[0].CreatedAtUnixMs = 0
			return r
		}},
		{"negative time", func(r *pb.ListTaskNotificationsResponse) *pb.ListTaskNotificationsResponse {
			r.Notifications[0].CreatedAtUnixMs = -1
			return r
		}},
		{"overflow time", func(r *pb.ListTaskNotificationsResponse) *pb.ListTaskNotificationsResponse {
			r.Notifications[0].CreatedAtUnixMs = 253402300800000
			return r
		}},
		{"negative next", func(r *pb.ListTaskNotificationsResponse) *pb.ListTaskNotificationsResponse {
			r.NextBeforeNotificationId = -1
			return r
		}},
		{"wrong next", func(r *pb.ListTaskNotificationsResponse) *pb.ListTaskNotificationsResponse {
			r.NextBeforeNotificationId = 9
			return r
		}},
		{"partial page with next", func(r *pb.ListTaskNotificationsResponse) *pb.ListTaskNotificationsResponse {
			r.Notifications = r.Notifications[:1]
			r.NextBeforeNotificationId = 10
			return r
		}},
		{"empty page with next", func(r *pb.ListTaskNotificationsResponse) *pb.ListTaskNotificationsResponse {
			r.Notifications = nil
			return r
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.modify(&pb.ListTaskNotificationsResponse{Notifications: []*pb.TaskNotificationItem{validHTTPTaskNotification(10), validHTTPTaskNotification(8)}, NextBeforeNotificationId: 8})
			client := taskNotificationsFunc(func(context.Context, *pb.ListTaskNotificationsRequest) (*pb.ListTaskNotificationsResponse, error) {
				return result, nil
			})
			w := httptest.NewRecorder()
			listTaskNotificationsHandler(client)(w, taskNotificationsHTTPRequest("200", "?before_notification_id=20&limit=2", "Bearer private-token"))
			if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), `"data"`) || strings.Contains(w.Body.String(), "private-token") {
				t.Fatalf("invalid RPC result: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
