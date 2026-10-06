package main

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yjydist/go-im/internal/rpcauth"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// This flow uses real Agent->IM and IM->User TLS connections. The User stub
// changes eligibility only after it has returned a valid candidate.
type resolverGenerationFlow struct {
	*triggerReadFlow
	scenario    string
	userCalls   atomic.Int32
	lookupCalls atomic.Int32
}

type resolverGenerationUserServer struct {
	userpb.UnimplementedUserTriggerServer
	flow *resolverGenerationFlow
}

func (s *resolverGenerationUserServer) scope(ctx context.Context, actorID, teamID int64) error {
	if err := rpcauth.RequireServiceIdentity(ctx, "im.go-im.internal"); err != nil {
		return err
	}
	md, _ := metadata.FromIncomingContext(ctx)
	if actorID != s.flow.outbox.ActorID || teamID != s.flow.outbox.TeamID ||
		len(md.Get("authorization")) != 0 || len(md.Get("actor-id")) != 0 || len(md.Get("team-id")) != 0 {
		s.flow.t.Error("User received caller Token, caller scope, or scope unrelated to saved trigger")
		return status.Error(codes.Internal, "private scope detail")
	}
	return nil
}

func (s *resolverGenerationUserServer) CheckTriggerTeamMember(ctx context.Context, req *userpb.CheckTriggerTeamMemberRequest) (*userpb.CheckTriggerTeamMemberResponse, error) {
	if err := s.scope(ctx, req.GetActorId(), req.GetTeamId()); err != nil {
		return nil, err
	}
	call := s.flow.userCalls.Add(1)
	if s.flow.lookupCalls.Load() != 0 {
		if call != 4 {
			s.flow.t.Errorf("unexpected User check after lookup: %d", call)
		}
		switch s.flow.scenario {
		case "revoked":
			return nil, status.Error(codes.PermissionDenied, "private revoked membership")
		case "changed generation":
			return &userpb.CheckTriggerTeamMemberResponse{ActorId: req.GetActorId(), TeamId: req.GetTeamId(), Generation: 2}, nil
		case "User failure":
			return nil, status.Error(codes.Internal, "private database detail")
		}
	}
	return &userpb.CheckTriggerTeamMemberResponse{ActorId: req.GetActorId(), TeamId: req.GetTeamId(), Generation: 1}, nil
}

func (s *resolverGenerationUserServer) ResolveTriggerTeamMember(ctx context.Context, req *userpb.ResolveTriggerTeamMemberRequest) (*userpb.ResolveTriggerTeamMemberResponse, error) {
	if err := s.scope(ctx, req.GetActorId(), req.GetTeamId()); err != nil {
		return nil, err
	}
	if s.flow.userCalls.Load() != 3 || req.GetName() != "张三" {
		s.flow.t.Error("candidate lookup did not follow protected context read")
		return nil, status.Error(codes.Internal, "private lookup order")
	}
	s.flow.lookupCalls.Add(1)
	return &userpb.ResolveTriggerTeamMemberResponse{ActorId: req.GetActorId(), TeamId: req.GetTeamId(), Name: req.GetName(),
		Candidates: []*userpb.TriggerTeamMemberCandidate{{UserId: 77, Username: "zhangsan", Nickname: "张三"}}}, nil
}

func newResolverGenerationFlow(t *testing.T, scenario string) *resolverGenerationFlow {
	t.Helper()
	im, mock := testIMServer(t)
	outbox, source := triggerContextFixture()
	source.Content = "@AI 整理任务 张三负责整理文档"
	outbox.Instruction = "张三负责整理文档"
	f := &resolverGenerationFlow{triggerReadFlow: &triggerReadFlow{t: t, mock: mock, outbox: outbox, source: source}, scenario: scenario}
	userFiles := triggerClientCertificates(t)
	creds, err := rpcauth.NewServiceServerCredentials(userFiles["user.go-im.internal"], "im.go-im.internal")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	userServer := grpc.NewServer(grpc.Creds(creds))
	userpb.RegisterUserTriggerServer(userServer, &resolverGenerationUserServer{flow: f})
	go func() { _ = userServer.Serve(listener) }()
	t.Cleanup(func() { userServer.Stop(); _ = listener.Close() })
	teams, err := newTriggerTeamClient(triggerTeamClientConfig{Addr: listener.Addr().String(), ServerDNSName: "user.go-im.internal", Files: userFiles["im.go-im.internal"]})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = teams.Close() })
	imFiles, agentFiles, _ := botTestCertificates(t)
	f.runtime, err = newIMTriggerRuntime(imTriggerConfig{ListenOn: "127.0.0.1:0", AgentDNSName: "agent.go-im.internal", Files: imFiles}, im.db, teams)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = f.runtime.server.Serve(f.runtime.listener) }()
	t.Cleanup(f.runtime.Stop)
	f.client = f.newClient(agentFiles)
	return f
}

func TestResolverGenerationFlowFinalFenceAfterCandidateLookup(t *testing.T) {
	for _, tc := range []struct {
		name string
		want codes.Code
	}{
		{"eligible", codes.OK},
		{"revoked", codes.PermissionDenied},
		{"changed generation", codes.PermissionDenied},
		{"User failure", codes.Unavailable},
		{"group closed", codes.PermissionDenied},
		{"group removed", codes.PermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newResolverGenerationFlow(t, tc.name)
			f.expectSource(true)
			expectTriggerContextHistory(f.mock, f.outbox, triggerContextMessageRows(f.source))
			expectTeamGroupReadFence(f.mock, f.outbox.GroupID, f.outbox.ActorID, f.outbox.TeamID, nil, true)
			switch tc.name {
			case "eligible":
				expectTeamGroupReadFence(f.mock, f.outbox.GroupID, f.outbox.ActorID, f.outbox.TeamID, nil, true)
			case "group closed":
				expectTeamGroupReadFence(f.mock, f.outbox.GroupID, f.outbox.ActorID, f.outbox.TeamID, int64(1), true)
			case "group removed":
				expectTeamGroupReadFence(f.mock, f.outbox.GroupID, f.outbox.ActorID, f.outbox.TeamID, nil, false)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer forged", "actor-id", "999", "team-id", "999"))
			result, err := f.client.ResolveMember(ctx, f.outbox.MessageID, "张三")
			if status.Code(err) != tc.want || err != nil && strings.Contains(err.Error(), "private") {
				t.Fatalf("response=%v error=%v want=%s", result, err, tc.want)
			}
			if tc.want == codes.OK {
				if result.GetActorId() != f.outbox.ActorID || result.GetTeamId() != f.outbox.TeamID || result.GetGroupId() != f.outbox.GroupID ||
					result.GetMessageId() != f.outbox.MessageID || result.GetRequestKey() != f.outbox.RequestKey() || result.GetName() != "张三" ||
					len(result.GetCandidates()) != 1 || result.GetCandidates()[0].GetUserId() != 77 {
					t.Fatalf("saved scope or qualified candidate lost: %#v", result)
				}
			} else if result != nil {
				t.Fatalf("partial candidate escaped final fence: %#v", result)
			}
			if f.userCalls.Load() != 4 || f.lookupCalls.Load() != 1 {
				t.Fatalf("unexpected protected call sequence: checks=%d lookups=%d", f.userCalls.Load(), f.lookupCalls.Load())
			}
		})
	}
}
