package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	impb "github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type f2FlowUser struct {
	userpb.UnimplementedUserServer
	namesCalls int
}

func (s *f2FlowUser) ListMyTeams(ctx context.Context, _ *userpb.ListMyTeamsRequest) (*userpb.ListMyTeamsResponse, error) {
	if !f2FlowAuthorized(ctx) {
		return nil, status.Error(codes.Unauthenticated, "missing token")
	}
	return &userpb.ListMyTeamsResponse{Teams: []*userpb.MyTeam{{TeamId: 9007199254740993, Name: "研发组", Role: 0}}}, nil
}

func (s *f2FlowUser) BatchGetConversationDisplayNames(ctx context.Context, req *userpb.BatchGetConversationDisplayNamesRequest) (*userpb.BatchGetConversationDisplayNamesResponse, error) {
	if !f2FlowAuthorized(ctx) {
		return nil, status.Error(codes.Unauthenticated, "missing token")
	}
	s.namesCalls++
	if len(req.GetUserIds()) != 1 || req.UserIds[0] != 9007199254740995 {
		return nil, status.Error(codes.InvalidArgument, "unproven peer")
	}
	return &userpb.BatchGetConversationDisplayNamesResponse{Users: []*userpb.ConversationDisplayName{{UserId: req.UserIds[0], DisplayName: "小林"}}}, nil
}

type f2FlowIM struct{ impb.UnimplementedIMServer }

func (s *f2FlowIM) ListMyDirectConversations(ctx context.Context, _ *impb.ListMyDirectConversationsRequest) (*impb.ListMyDirectConversationsResponse, error) {
	if !f2FlowAuthorized(ctx) {
		return nil, status.Error(codes.Unauthenticated, "missing token")
	}
	return &impb.ListMyDirectConversationsResponse{Conversations: []*impb.DirectConversation{{PeerId: 9007199254740995, LastMessageId: 9007199254740997}}, SnapshotUpperMessageId: 9007199254740997}, nil
}

func (s *f2FlowIM) GetMyDirectConversation(ctx context.Context, _ *impb.GetMyDirectConversationRequest) (*impb.GetMyDirectConversationResponse, error) {
	if !f2FlowAuthorized(ctx) {
		return nil, status.Error(codes.Unauthenticated, "missing token")
	}
	return nil, status.Error(codes.NotFound, "unknown peer")
}

func f2FlowAuthorized(ctx context.Context) bool {
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get("authorization")
	return len(values) == 1 && values[0] == "Bearer test-token"
}

func TestF2HTTPRealGRPCMetadataAndPeerProof(t *testing.T) {
	userImpl := &f2FlowUser{}
	userAddr := startFlowRPC(t, func(server *grpc.Server) { userpb.RegisterUserServer(server, userImpl) })
	imAddr := startFlowRPC(t, func(server *grpc.Server) { impb.RegisterIMServer(server, &f2FlowIM{}) })
	userConn, err := grpc.NewClient(userAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = userConn.Close() })
	imConn, err := grpc.NewClient(imAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = imConn.Close() })
	users, ims := userpb.NewUserClient(userConn), impb.NewIMClient(imConn)

	teams := httptest.NewRecorder()
	listMyTeamsHandler(users)(teams, f2Request("/api/v1/teams"))
	if teams.Code != 200 || !strings.Contains(teams.Body.String(), `"team_id":"9007199254740993"`) {
		t.Fatalf("teams: %d %s", teams.Code, teams.Body.String())
	}

	list := httptest.NewRecorder()
	listMyDirectConversationsHandler(ims, users)(list, f2Request("/api/v1/me/direct-conversations"))
	if list.Code != 200 || !strings.Contains(list.Body.String(), `"peer_id":"9007199254740995"`) || !strings.Contains(list.Body.String(), `"display_name":"小林"`) || userImpl.namesCalls != 1 {
		t.Fatalf("direct list: %d %s, names calls=%d", list.Code, list.Body.String(), userImpl.namesCalls)
	}

	unknown := httptest.NewRecorder()
	r := pathvar.WithVars(f2Request("/api/v1/me/direct-conversations/42"), map[string]string{"peer_id": "42"})
	getMyDirectConversationHandler(ims, users)(unknown, r)
	if unknown.Code != 404 || userImpl.namesCalls != 1 || strings.Contains(unknown.Body.String(), "unknown peer") {
		t.Fatalf("unknown: %d %s, names calls=%d", unknown.Code, unknown.Body.String(), userImpl.namesCalls)
	}
}
