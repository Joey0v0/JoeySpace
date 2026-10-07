package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type userClientFunc func(context.Context, *pb.GetUserInfoRequest) (*pb.GetUserInfoResponse, error)

func (f userClientFunc) LeaveTeam(context.Context, *pb.LeaveTeamRequest, ...grpc.CallOption) (*pb.TeamLeaveOperationResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used in demo tests")
}

func (f userClientFunc) GetTeamLeaveOperation(context.Context, *pb.GetTeamLeaveOperationRequest, ...grpc.CallOption) (*pb.TeamLeaveOperationResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used in demo tests")
}

func (f userClientFunc) Register(context.Context, *pb.RegisterRequest, ...grpc.CallOption) (*pb.RegisterResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used in demo tests")
}

func (f userClientFunc) CreateTeam(context.Context, *pb.CreateTeamRequest, ...grpc.CallOption) (*pb.CreateTeamResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used in demo tests")
}

func (f userClientFunc) AddTeamMember(context.Context, *pb.AddTeamMemberRequest, ...grpc.CallOption) (*pb.AddTeamMemberResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used in demo tests")
}

func (f userClientFunc) ListTeamMembers(context.Context, *pb.ListTeamMembersRequest, ...grpc.CallOption) (*pb.ListTeamMembersResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used in demo tests")
}

func (f userClientFunc) SetTeamMemberRole(context.Context, *pb.SetTeamMemberRoleRequest, ...grpc.CallOption) (*pb.SetTeamMemberRoleResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used in demo tests")
}

func (f userClientFunc) AuthorizeTeamGroupCreation(context.Context, *pb.AuthorizeTeamGroupCreationRequest, ...grpc.CallOption) (*pb.AuthorizeTeamGroupCreationResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used in demo tests")
}

func (f userClientFunc) CheckTeamMember(context.Context, *pb.CheckTeamMemberRequest, ...grpc.CallOption) (*pb.CheckTeamMemberResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used in demo tests")
}

func (f userClientFunc) CheckTeamMemberByID(context.Context, *pb.CheckTeamMemberByIDRequest, ...grpc.CallOption) (*pb.CheckTeamMemberByIDResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used in demo tests")
}

func (f userClientFunc) ResolveTeamMember(context.Context, *pb.ResolveTeamMemberRequest, ...grpc.CallOption) (*pb.ResolveTeamMemberResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used in demo tests")
}

func (f userClientFunc) Login(context.Context, *pb.LoginRequest, ...grpc.CallOption) (*pb.LoginResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used in demo tests")
}

func (f userClientFunc) GetMyInfo(context.Context, *pb.GetMyInfoRequest, ...grpc.CallOption) (*pb.GetUserInfoResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used in demo tests")
}

func (f userClientFunc) GetUserInfo(ctx context.Context, req *pb.GetUserInfoRequest, _ ...grpc.CallOption) (*pb.GetUserInfoResponse, error) {
	return f(ctx, req)
}

func TestInvalidUserIDDoesNotCallRPC(t *testing.T) {
	client := userClientFunc(func(context.Context, *pb.GetUserInfoRequest) (*pb.GetUserInfoResponse, error) {
		t.Fatal("invalid HTTP input must not reach RPC")
		return nil, nil
	})
	for _, query := range []string{"", "user_id=", "user_id=abc", "user_id=0", "user_id=-1", "user_id=9223372036854775808", "user_id=1&user_id=2", "user_id=1&bad=%zz"} {
		t.Run(query, func(t *testing.T) {
			w := httptest.NewRecorder()
			getUserInfoHandler(client)(w, httptest.NewRequest("GET", "/demo/user/info?"+query, nil))
			if w.Code != 400 || !strings.Contains(w.Body.String(), `"code":10001`) {
				t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestUserInfoComesFromRPC(t *testing.T) {
	r := httptest.NewRequest("GET", "/demo/user/info?user_id=42", nil)
	client := userClientFunc(func(ctx context.Context, req *pb.GetUserInfoRequest) (*pb.GetUserInfoResponse, error) {
		if req.GetUserId() != 42 || ctx != r.Context() {
			t.Fatal("HTTP user ID and context must be passed to RPC")
		}
		// 用不同于演示用户的数据，确认 API 没有自己拼出固定资料。
		return &pb.GetUserInfoResponse{Id: 42, Username: "rpc_result", Nickname: "from RPC"}, nil
	})
	w := httptest.NewRecorder()
	getUserInfoHandler(client)(w, r)
	var body response
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || body.Code != 0 || body.Data == nil || body.Data.ID != 42 || body.Data.Username != "rpc_result" || body.Data.Nickname != "from RPC" {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
	}
}

func TestRPCErrorsBecomeHTTPErrors(t *testing.T) {
	for _, tc := range []struct {
		code         codes.Code
		httpStatus   int
		businessCode int
	}{
		{codes.InvalidArgument, 400, 10001},
		{codes.Unauthenticated, 401, 10002},
		{codes.PermissionDenied, 403, 20004},
		{codes.NotFound, 404, 20002},
		{codes.Unavailable, 503, 10005},
		{codes.DeadlineExceeded, 504, 10005},
		{codes.Internal, 502, 10005},
	} {
		t.Run(tc.code.String(), func(t *testing.T) {
			client := userClientFunc(func(context.Context, *pb.GetUserInfoRequest) (*pb.GetUserInfoResponse, error) {
				return nil, status.Error(tc.code, "private RPC detail")
			})
			w := httptest.NewRecorder()
			getUserInfoHandler(client)(w, httptest.NewRequest("GET", "/demo/user/info?user_id=1", nil))
			var body response
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.httpStatus || body.Code != tc.businessCode || body.Data != nil || strings.Contains(w.Body.String(), "private RPC detail") {
				t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
