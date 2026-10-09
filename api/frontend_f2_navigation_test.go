package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	impb "github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type f2UserClient struct {
	userpb.UserClient
	list  func(context.Context, *userpb.ListMyTeamsRequest) (*userpb.ListMyTeamsResponse, error)
	names func(context.Context, *userpb.BatchGetConversationDisplayNamesRequest) (*userpb.BatchGetConversationDisplayNamesResponse, error)
}

func (c f2UserClient) ListMyTeams(ctx context.Context, req *userpb.ListMyTeamsRequest, _ ...grpc.CallOption) (*userpb.ListMyTeamsResponse, error) {
	return c.list(ctx, req)
}
func (c f2UserClient) BatchGetConversationDisplayNames(ctx context.Context, req *userpb.BatchGetConversationDisplayNamesRequest, _ ...grpc.CallOption) (*userpb.BatchGetConversationDisplayNamesResponse, error) {
	return c.names(ctx, req)
}

type f2IMClient struct {
	impb.IMClient
	groups func(context.Context, *impb.GetTeamGroupRequest) (*impb.GetTeamGroupResponse, error)
	list   func(context.Context, *impb.ListMyDirectConversationsRequest) (*impb.ListMyDirectConversationsResponse, error)
	detail func(context.Context, *impb.GetMyDirectConversationRequest) (*impb.GetMyDirectConversationResponse, error)
}

func (c f2IMClient) GetTeamGroup(ctx context.Context, req *impb.GetTeamGroupRequest, _ ...grpc.CallOption) (*impb.GetTeamGroupResponse, error) {
	return c.groups(ctx, req)
}
func (c f2IMClient) ListMyDirectConversations(ctx context.Context, req *impb.ListMyDirectConversationsRequest, _ ...grpc.CallOption) (*impb.ListMyDirectConversationsResponse, error) {
	return c.list(ctx, req)
}
func (c f2IMClient) GetMyDirectConversation(ctx context.Context, req *impb.GetMyDirectConversationRequest, _ ...grpc.CallOption) (*impb.GetMyDirectConversationResponse, error) {
	return c.detail(ctx, req)
}

func f2Request(path string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Authorization", "Bearer test-token")
	return r
}

func TestF2MyTeamsForwardsIdentityAndPreservesLargeIDs(t *testing.T) {
	client := f2UserClient{list: func(ctx context.Context, req *userpb.ListMyTeamsRequest) (*userpb.ListMyTeamsResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer test-token" {
			t.Fatalf("authorization: %v", got)
		}
		if req.GetAfterTeamId() != 9007199254740993 || req.GetLimit() != 2 {
			t.Fatalf("request: %v", req)
		}
		return &userpb.ListMyTeamsResponse{Teams: []*userpb.MyTeam{{TeamId: 9007199254740995, Name: "项目组", Role: 1}}, NextAfterTeamId: 9007199254740995}, nil
	}}
	w := httptest.NewRecorder()
	listMyTeamsHandler(client)(w, f2Request("/api/v1/teams?after_team_id=9007199254740993&limit=2"))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"team_id":"9007199254740995"`) || !strings.Contains(w.Body.String(), `"next_after_team_id":"9007199254740995"`) {
		t.Fatalf("response: %d %s", w.Code, w.Body.String())
	}
}

func TestF2TeamGroupDetailReturnsJoinedWithoutInventingReadAccess(t *testing.T) {
	client := f2IMClient{groups: func(ctx context.Context, req *impb.GetTeamGroupRequest) (*impb.GetTeamGroupResponse, error) {
		if req.GetTeamId() != 77 || req.GetGroupId() != 9007199254740993 {
			t.Fatalf("request: %v", req)
		}
		return &impb.GetTeamGroupResponse{Group: &impb.TeamGroup{GroupId: req.GroupId, Name: "讨论", OwnerId: 11, Joined: false}}, nil
	}}
	r := pathvar.WithVars(f2Request("/api/v1/teams/77/groups/9007199254740993"), map[string]string{"team_id": "77", "group_id": "9007199254740993"})
	w := httptest.NewRecorder()
	getTeamGroupHandler(client)(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"joined":false`) || !strings.Contains(w.Body.String(), `"group_id":"9007199254740993"`) {
		t.Fatalf("response: %d %s", w.Code, w.Body.String())
	}
}

func TestF2DirectDirectoryLooksUpOnlyIMProvenPeers(t *testing.T) {
	im := f2IMClient{list: func(_ context.Context, req *impb.ListMyDirectConversationsRequest) (*impb.ListMyDirectConversationsResponse, error) {
		if req.GetSnapshotUpperMessageId() != 9007199254740997 || req.GetBeforeLastMessageId() != 9007199254740995 || req.GetLimit() != 2 {
			t.Fatalf("request: %v", req)
		}
		return &impb.ListMyDirectConversationsResponse{Conversations: []*impb.DirectConversation{{PeerId: 9007199254740993, LastMessageId: 9007199254740994}}, SnapshotUpperMessageId: 9007199254740997}, nil
	}}
	users := f2UserClient{names: func(_ context.Context, req *userpb.BatchGetConversationDisplayNamesRequest) (*userpb.BatchGetConversationDisplayNamesResponse, error) {
		if len(req.GetUserIds()) != 1 || req.GetUserIds()[0] != 9007199254740993 {
			t.Fatalf("unproven peers requested: %v", req.GetUserIds())
		}
		return &userpb.BatchGetConversationDisplayNamesResponse{Users: []*userpb.ConversationDisplayName{{UserId: req.UserIds[0], DisplayName: "阿青"}}}, nil
	}}
	w := httptest.NewRecorder()
	listMyDirectConversationsHandler(im, users)(w, f2Request("/api/v1/me/direct-conversations?snapshot_upper_message_id=9007199254740997&before_last_message_id=9007199254740995&limit=2"))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"peer_id":"9007199254740993"`) || !strings.Contains(w.Body.String(), `"display_name":"阿青"`) || !strings.Contains(w.Body.String(), `"snapshot_upper_message_id":"9007199254740997"`) {
		t.Fatalf("response: %d %s", w.Code, w.Body.String())
	}
}

func TestF2UnknownDirectPeerDoesNotReadDisplayName(t *testing.T) {
	im := f2IMClient{detail: func(context.Context, *impb.GetMyDirectConversationRequest) (*impb.GetMyDirectConversationResponse, error) {
		return nil, status.Error(codes.NotFound, "private detail")
	}}
	users := f2UserClient{names: func(context.Context, *userpb.BatchGetConversationDisplayNamesRequest) (*userpb.BatchGetConversationDisplayNamesResponse, error) {
		t.Fatal("name lookup must follow IM proof")
		return nil, nil
	}}
	r := pathvar.WithVars(f2Request("/api/v1/me/direct-conversations/42"), map[string]string{"peer_id": "42"})
	w := httptest.NewRecorder()
	getMyDirectConversationHandler(im, users)(w, r)
	if w.Code != 404 || strings.Contains(w.Body.String(), "private detail") {
		t.Fatalf("response: %d %s", w.Code, w.Body.String())
	}
}

func TestF2InvalidInputsAndFailuresDoNotBecomeEmptyDirectory(t *testing.T) {
	client := f2UserClient{list: func(context.Context, *userpb.ListMyTeamsRequest) (*userpb.ListMyTeamsResponse, error) {
		return nil, status.Error(codes.Unavailable, "private detail")
	}}
	for _, tc := range []struct {
		path, token string
		want        int
	}{
		{"/api/v1/teams", "", 401},
		{"/api/v1/teams?user_id=2", "Bearer test-token", 400},
		{"/api/v1/teams?limit=101", "Bearer test-token", 400},
		{"/api/v1/teams", "Bearer test-token", 503},
	} {
		r := f2Request(tc.path)
		if tc.token == "" {
			r.Header.Del("Authorization")
		}
		w := httptest.NewRecorder()
		listMyTeamsHandler(client)(w, r)
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != tc.want || body["data"] != nil || strings.Contains(w.Body.String(), "private detail") {
			t.Fatalf("request %s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
}

func TestF2DirectDirectoryAcceptsMaximumInt64SnapshotAndRejectsBadCursor(t *testing.T) {
	const maxID = int64(9223372036854775807)
	im := f2IMClient{list: func(context.Context, *impb.ListMyDirectConversationsRequest) (*impb.ListMyDirectConversationsResponse, error) {
		return &impb.ListMyDirectConversationsResponse{Conversations: []*impb.DirectConversation{{PeerId: 41, LastMessageId: maxID}}, SnapshotUpperMessageId: maxID}, nil
	}}
	users := f2UserClient{names: func(context.Context, *userpb.BatchGetConversationDisplayNamesRequest) (*userpb.BatchGetConversationDisplayNamesResponse, error) {
		return &userpb.BatchGetConversationDisplayNamesResponse{}, nil
	}}
	w := httptest.NewRecorder()
	listMyDirectConversationsHandler(im, users)(w, f2Request("/api/v1/me/direct-conversations"))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"last_message_id":"9223372036854775807"`) || !strings.Contains(w.Body.String(), `"display_name":"已停用成员"`) {
		t.Fatalf("max ID response: %d %s", w.Code, w.Body.String())
	}
	im.list = func(context.Context, *impb.ListMyDirectConversationsRequest) (*impb.ListMyDirectConversationsResponse, error) {
		return &impb.ListMyDirectConversationsResponse{Conversations: []*impb.DirectConversation{{PeerId: 41, LastMessageId: 15}}, SnapshotUpperMessageId: 20, NextBeforeLastMessageId: 14}, nil
	}
	w = httptest.NewRecorder()
	listMyDirectConversationsHandler(im, users)(w, f2Request("/api/v1/me/direct-conversations"))
	if w.Code != 502 || strings.Contains(w.Body.String(), `"conversations"`) {
		t.Fatalf("bad cursor response: %d %s", w.Code, w.Body.String())
	}
}

func TestF2DirectDirectoryDoesNotMaskUserNameFailure(t *testing.T) {
	im := f2IMClient{list: func(context.Context, *impb.ListMyDirectConversationsRequest) (*impb.ListMyDirectConversationsResponse, error) {
		return &impb.ListMyDirectConversationsResponse{Conversations: []*impb.DirectConversation{{PeerId: 41, LastMessageId: 15}}, SnapshotUpperMessageId: 20}, nil
	}}
	users := f2UserClient{names: func(context.Context, *userpb.BatchGetConversationDisplayNamesRequest) (*userpb.BatchGetConversationDisplayNamesResponse, error) {
		return nil, status.Error(codes.DeadlineExceeded, "private detail")
	}}
	w := httptest.NewRecorder()
	listMyDirectConversationsHandler(im, users)(w, f2Request("/api/v1/me/direct-conversations"))
	if w.Code != 504 || strings.Contains(w.Body.String(), `"conversations"`) || strings.Contains(w.Body.String(), "private detail") {
		t.Fatalf("name failure response: %d %s", w.Code, w.Body.String())
	}
}
