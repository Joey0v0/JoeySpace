package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	impb "github.com/yjydist/go-im/rpc/im/pb"
	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const f4LargeID int64 = 9007199254740993

func f4Authorized(ctx context.Context) bool {
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get("authorization")
	return len(values) == 1 && values[0] == "Bearer test-token"
}

type f4TaskServer struct{ taskpb.UnimplementedTaskServer }

func (f4TaskServer) ListMyTasks(ctx context.Context, req *taskpb.ListMyTasksRequest) (*taskpb.ListMyTasksResponse, error) {
	if !f4Authorized(ctx) {
		return nil, status.Error(codes.Unauthenticated, "private token")
	}
	if req.TeamId != f4LargeID || req.View != 0 || req.Limit != 1 {
		return nil, status.Error(codes.InvalidArgument, "wrong filters")
	}
	return &taskpb.ListMyTasksResponse{Tasks: []*taskpb.TaskItem{f4TaskItem()}}, nil
}
func (f4TaskServer) GetTask(ctx context.Context, req *taskpb.GetTaskRequest) (*taskpb.GetTaskResponse, error) {
	if !f4Authorized(ctx) {
		return nil, status.Error(codes.Unauthenticated, "private token")
	}
	if req.TeamId != f4LargeID {
		return nil, status.Error(codes.PermissionDenied, "private team")
	}
	if req.TaskId != f4LargeID+1 {
		return nil, status.Error(codes.NotFound, "private task")
	}
	return &taskpb.GetTaskResponse{Task: f4TaskItem(), CanUpdateStatus: true}, nil
}
func (f4TaskServer) SetTaskStatus(ctx context.Context, req *taskpb.SetTaskStatusRequest) (*taskpb.SetTaskStatusResponse, error) {
	if !f4Authorized(ctx) {
		return nil, status.Error(codes.Unauthenticated, "private token")
	}
	if req.TeamId != f4LargeID || req.TaskId != f4LargeID+1 || req.Status != 1 || req.ExpectedStatus == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid status")
	}
	if *req.ExpectedStatus != 0 {
		return nil, status.Error(codes.Aborted, "private current status")
	}
	return &taskpb.SetTaskStatusResponse{}, nil
}
func (f4TaskServer) ListMyTaskNotifications(ctx context.Context, req *taskpb.ListMyTaskNotificationsRequest) (*taskpb.ListMyTaskNotificationsResponse, error) {
	if !f4Authorized(ctx) {
		return nil, status.Error(codes.Unauthenticated, "private token")
	}
	if req.TeamId != f4LargeID || req.Limit != 1 {
		return nil, status.Error(codes.InvalidArgument, "wrong filters")
	}
	return &taskpb.ListMyTaskNotificationsResponse{Notifications: []*taskpb.TaskNotificationItem{{
		NotificationId: f4LargeID + 4, TeamId: f4LargeID, TaskId: f4LargeID + 1, ActorId: f4LargeID + 2,
		TaskTitle: "处理讨论事项", FromStatus: 0, ToStatus: 1, CurrentStatus: 1, CreatedAtUnixMs: 1791097200123,
	}}, UnreadCount: f4LargeID}, nil
}
func (f4TaskServer) MarkTaskNotificationRead(ctx context.Context, req *taskpb.MarkTaskNotificationReadRequest) (*taskpb.MarkTaskNotificationReadResponse, error) {
	if !f4Authorized(ctx) {
		return nil, status.Error(codes.Unauthenticated, "private token")
	}
	if req.TeamId != f4LargeID {
		return nil, status.Error(codes.PermissionDenied, "private team")
	}
	if req.NotificationId != f4LargeID+4 {
		return nil, status.Error(codes.NotFound, "private notification")
	}
	return &taskpb.MarkTaskNotificationReadResponse{NotificationId: req.NotificationId, ReadAtUnixMs: 1791097201123}, nil
}
func f4TaskItem() *taskpb.TaskItem {
	return &taskpb.TaskItem{TaskId: f4LargeID + 1, TeamId: f4LargeID, Title: "处理讨论事项", CreatorId: f4LargeID + 2,
		AssigneeId: f4LargeID + 3, Status: 0, SourceGroupId: f4LargeID + 5, SourceMessageId: f4LargeID + 6}
}

type f4UserServer struct{ userpb.UnimplementedUserServer }

func (f4UserServer) BatchGetMyTeamNames(ctx context.Context, req *userpb.BatchGetMyTeamNamesRequest) (*userpb.BatchGetMyTeamNamesResponse, error) {
	if !f4Authorized(ctx) {
		return nil, status.Error(codes.Unauthenticated, "private token")
	}
	if len(req.TeamIds) != 1 || req.TeamIds[0] != f4LargeID {
		return nil, status.Error(codes.InvalidArgument, "unproven team")
	}
	return &userpb.BatchGetMyTeamNamesResponse{Teams: []*userpb.MyTeamName{{TeamId: f4LargeID, Name: "研发组"}}}, nil
}
func (f4UserServer) BatchGetTeamMemberDisplayNames(ctx context.Context, req *userpb.BatchGetTeamMemberDisplayNamesRequest) (*userpb.BatchGetTeamMemberDisplayNamesResponse, error) {
	if !f4Authorized(ctx) {
		return nil, status.Error(codes.Unauthenticated, "private token")
	}
	if req.TeamId != f4LargeID {
		return nil, status.Error(codes.PermissionDenied, "private team")
	}
	users := make([]*userpb.TeamMemberDisplayName, 0, len(req.UserIds))
	for _, id := range req.UserIds {
		if id != f4LargeID+2 && id != f4LargeID+3 {
			return nil, status.Error(codes.InvalidArgument, "unproven user")
		}
		users = append(users, &userpb.TeamMemberDisplayName{UserId: id, DisplayName: "小林"})
	}
	return &userpb.BatchGetTeamMemberDisplayNamesResponse{Users: users}, nil
}

type f4IMServer struct{ impb.UnimplementedIMServer }

func (f4IMServer) GetTeamGroupMessageContext(ctx context.Context, req *impb.GetTeamGroupMessageContextRequest) (*impb.GetTeamGroupMessageContextResponse, error) {
	if !f4Authorized(ctx) {
		return nil, status.Error(codes.Unauthenticated, "private token")
	}
	if req.TeamId != f4LargeID || req.GroupId != f4LargeID+5 {
		return nil, status.Error(codes.PermissionDenied, "private group")
	}
	if req.MessageId != f4LargeID+6 {
		return nil, status.Error(codes.NotFound, "private message")
	}
	return &impb.GetTeamGroupMessageContextResponse{TargetMessageId: req.MessageId, Messages: []*impb.TeamGroupMessage{{
		Id: req.MessageId, MsgId: "source-1", FromId: f4LargeID + 2, SenderType: 1, ContentType: 1,
		Content: "请处理这一项", CreatedAtUnixMs: 1791097200123,
	}}}, nil
}

func f4Connect(t *testing.T, address string) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}
func f4HTTP(t *testing.T, handler http.HandlerFunc, method, path, body string, vars map[string]string, want int, parts ...string) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer test-token")
	if vars != nil {
		r = pathvar.WithVars(r, vars)
	}
	w := httptest.NewRecorder()
	handler(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: got %d, want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	for _, part := range parts {
		if !strings.Contains(w.Body.String(), part) {
			t.Fatalf("%s %s: missing %q: %s", method, path, part, w.Body.String())
		}
	}
	if want != 200 && strings.Contains(w.Body.String(), "private") {
		t.Fatalf("private RPC error leaked: %s", w.Body.String())
	}
}

func TestF4HTTPThroughTCPGRPCTaskContextAndNotification(t *testing.T) {
	taskAddr := startFlowRPC(t, func(s *grpc.Server) { taskpb.RegisterTaskServer(s, f4TaskServer{}) })
	userAddr := startFlowRPC(t, func(s *grpc.Server) { userpb.RegisterUserServer(s, f4UserServer{}) })
	imAddr := startFlowRPC(t, func(s *grpc.Server) { impb.RegisterIMServer(s, f4IMServer{}) })
	tasks := taskpb.NewTaskClient(f4Connect(t, taskAddr))
	users := userpb.NewUserClient(f4Connect(t, userAddr))
	ims := impb.NewIMClient(f4Connect(t, imAddr))
	teamTask := map[string]string{"team_id": "9007199254740993", "task_id": "9007199254740994"}
	f4HTTP(t, listMyTasksHandler(tasks, users), "GET", "/api/v1/tasks?view=open&team_id=9007199254740993&limit=1", "", nil, 200,
		`"task_id":"9007199254740994"`, `"team_name":"研发组"`, `"creator_name":"小林"`)
	f4HTTP(t, getTaskDetailHandler(tasks, users), "GET", "/api/v1/teams/9007199254740993/tasks/9007199254740994", "", teamTask, 200,
		`"can_update_status":true`, `"source_message_id":"9007199254740999"`)
	f4HTTP(t, getTaskDetailHandler(tasks, users), "GET", "/api/v1/teams/8/tasks/9007199254740994", "", map[string]string{"team_id": "8", "task_id": "9007199254740994"}, 403)
	f4HTTP(t, setTaskStatusHandler(tasks), "PUT", "/api/v1/teams/9007199254740993/tasks/9007199254740994/status", `{"status":1,"expected_status":0}`, teamTask, 200)
	f4HTTP(t, setTaskStatusHandler(tasks), "PUT", "/api/v1/teams/9007199254740993/tasks/9007199254740994/status", `{"status":1,"expected_status":2}`, teamTask, 409)
	f4HTTP(t, setTaskStatusHandler(tasks), "PUT", "/api/v1/teams/9007199254740993/tasks/9007199254740994/status", `{"status":1}`, teamTask, 400)
	f4HTTP(t, getTeamGroupMessageContextHandler(ims), "GET", "/api/v1/teams/9007199254740993/groups/9007199254740998/messages/9007199254740999/context", "", map[string]string{
		"team_id": "9007199254740993", "group_id": "9007199254740998", "message_id": "9007199254740999"}, 200,
		`"target_message_id":"9007199254740999"`, `"content":"请处理这一项"`)
	f4HTTP(t, listMyTaskNotificationsHandler(tasks, users), "GET", "/api/v1/task-notifications?team_id=9007199254740993&limit=1", "", nil, 200,
		`"notification_id":"9007199254740997"`, `"unread_count":"9007199254740993"`, `"actor_name":"小林"`)
	f4HTTP(t, markTaskNotificationReadHandler(tasks), "PUT", "/api/v1/teams/9007199254740993/task-notifications/9007199254740997/read", "", map[string]string{
		"team_id": "9007199254740993", "notification_id": "9007199254740997"}, 200, `"read_at_unix_ms":"1791097201123"`)
	f4HTTP(t, markTaskNotificationReadHandler(tasks), "PUT", "/api/v1/teams/9007199254740993/task-notifications/9007199254740998/read", "", map[string]string{
		"team_id": "9007199254740993", "notification_id": "9007199254740998"}, 404)
}
