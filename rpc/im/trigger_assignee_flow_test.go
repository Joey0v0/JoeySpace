package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type triggerAssigneeFlow struct {
	*triggerReadFlow
	lookupCalls atomic.Int32
	mode        atomic.Int32 // 0 unique; 1 empty; 2 ambiguous; 3 large/truncated; 4 bad echo; 5 failure
}

type assigneeFlowUserServer struct {
	userpb.UnimplementedUserTriggerServer
	flow *triggerAssigneeFlow
}

func (s *assigneeFlowUserServer) check(ctx context.Context, actorID, teamID int64) error {
	if err := rpcauth.RequireServiceIdentity(ctx, "im.go-im.internal"); err != nil {
		return err
	}
	md, _ := metadata.FromIncomingContext(ctx)
	if len(md.Get("authorization")) != 0 || len(md.Get("actor-id")) != 0 || len(md.Get("team-id")) != 0 ||
		actorID != s.flow.outbox.ActorID || teamID != s.flow.outbox.TeamID {
		s.flow.t.Error("User received caller metadata or scope unrelated to saved source")
		return status.Error(codes.Internal, "bad scope")
	}
	return nil
}

func (s *assigneeFlowUserServer) CheckTriggerTeamMember(ctx context.Context, req *userpb.CheckTriggerTeamMemberRequest) (*userpb.CheckTriggerTeamMemberResponse, error) {
	if err := s.check(ctx, req.GetActorId(), req.GetTeamId()); err != nil {
		return nil, err
	}
	s.flow.userCalls.Add(1)
	return &userpb.CheckTriggerTeamMemberResponse{ActorId: req.GetActorId(), TeamId: req.GetTeamId(), Generation: 1}, nil
}

func (s *assigneeFlowUserServer) ResolveTriggerTeamMember(ctx context.Context, req *userpb.ResolveTriggerTeamMemberRequest) (*userpb.ResolveTriggerTeamMemberResponse, error) {
	if err := s.check(ctx, req.GetActorId(), req.GetTeamId()); err != nil {
		return nil, err
	}
	s.flow.lookupCalls.Add(1)
	response := &userpb.ResolveTriggerTeamMemberResponse{ActorId: req.GetActorId(), TeamId: req.GetTeamId(), Name: req.GetName(),
		Candidates: []*userpb.TriggerTeamMemberCandidate{{UserId: 77, Username: "zhangsan", Nickname: req.GetName()}}}
	switch s.flow.mode.Load() {
	case 1:
		response.Candidates = nil
	case 2:
		response.Candidates = append(response.Candidates, &userpb.TriggerTeamMemberCandidate{UserId: 78, Username: req.GetName(), Nickname: "other"})
	case 3:
		response.Candidates = nil
		response.Truncated = true
		for n := 0; n < 20; n++ {
			response.Candidates = append(response.Candidates, &userpb.TriggerTeamMemberCandidate{UserId: int64(100 + n), Username: fmt.Sprintf("%02d%s", n, strings.Repeat("x", 62)), Nickname: req.GetName()})
		}
	case 4:
		response.ActorId++
	case 5:
		return nil, status.Error(codes.Internal, "private backend detail")
	}
	return response, nil
}

func newTriggerAssigneeFlow(t *testing.T) *triggerAssigneeFlow {
	t.Helper()
	im, mock := testIMServer(t)
	outbox, source := triggerContextFixture()
	source.Content = "@AI 整理任务 张三负责整理文档"
	outbox.Instruction = "张三负责整理文档"
	f := &triggerAssigneeFlow{triggerReadFlow: &triggerReadFlow{t: t, mock: mock, outbox: outbox, source: source}}
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
	userpb.RegisterUserTriggerServer(userServer, &assigneeFlowUserServer{flow: f})
	go func() { _ = userServer.Serve(listener) }()
	t.Cleanup(func() { userServer.Stop(); _ = listener.Close() })
	teams, err := newTriggerTeamClient(triggerTeamClientConfig{Addr: listener.Addr().String(), ServerDNSName: "user.go-im.internal", Files: userFiles["im.go-im.internal"]})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = teams.Close() })
	imFiles, agentFiles, otherFiles := botTestCertificates(t)
	f.otherFiles = otherFiles
	f.runtime, err = newIMTriggerRuntime(imTriggerConfig{ListenOn: "127.0.0.1:0", AgentDNSName: "agent.go-im.internal", Files: imFiles}, im.db, teams)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = f.runtime.server.Serve(f.runtime.listener) }()
	t.Cleanup(f.runtime.Stop)
	f.client = f.newClient(agentFiles)
	return f
}

func (f *triggerAssigneeFlow) expectLookup(finalGroup bool) {
	f.t.Helper()
	f.expectSource(true)
	expectTriggerContextHistory(f.mock, f.outbox, triggerContextMessageRows(f.source))
	expectTeamGroupReadFence(f.mock, f.outbox.GroupID, f.outbox.ActorID, f.outbox.TeamID, nil, true)
	if finalGroup {
		expectTeamGroupReadFence(f.mock, f.outbox.GroupID, f.outbox.ActorID, f.outbox.TeamID, nil, true)
	} else {
		expectTeamGroupReadFence(f.mock, f.outbox.GroupID, f.outbox.ActorID, f.outbox.TeamID, nil, false)
	}
}

func TestTriggerAssigneeFlowActualAgentIMUserTLSKeepsSourceScopeAndMatchingOutcomes(t *testing.T) {
	for _, mode := range []int32{0, 1, 2, 3} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			f := newTriggerAssigneeFlow(t)
			f.mode.Store(mode)
			name := "张三"
			if mode == 3 {
				name = strings.Repeat("张", 64)
				f.outbox.Instruction = name + "负责整理文档"
				f.source.Content = "@AI 整理任务 " + f.outbox.Instruction
			}
			f.expectLookup(true)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer fake", "actor-id", "99", "team-id", "999"))
			result, err := f.client.ResolveMember(ctx, f.outbox.MessageID, name)
			if err != nil || result.GetActorId() != f.outbox.ActorID || result.GetTeamId() != f.outbox.TeamID || result.GetGroupId() != f.outbox.GroupID ||
				result.GetMessageId() != f.outbox.MessageID || result.GetRequestKey() != f.outbox.RequestKey() || result.GetName() != name {
				t.Fatalf("saved scope was lost: %#v %v", result, err)
			}
			want := map[int32]int{0: 1, 1: 0, 2: 2, 3: 20}[mode]
			if len(result.GetCandidates()) != want || result.GetTruncated() != (mode == 3) || f.lookupCalls.Load() != 1 || f.userCalls.Load() != 4 {
				t.Fatalf("wrong lookup outcome %#v", result)
			}
			if mode == 3 && proto.Size(result) <= 4096 {
				t.Fatal("large candidate test did not exercise single-call 32KiB override")
			}
			if (&pb.ResolveTaskTriggerMemberRequest{}).ProtoReflect().Descriptor().Fields().Len() != 2 {
				t.Fatal("caller can submit unplanned scope")
			}
		})
	}
}

func TestTriggerAssigneeFlowRejectsUserFailureChangedEchoAndGroupRevocation(t *testing.T) {
	for _, scenario := range []string{"bad User echo", "User unavailable", "left group during lookup"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTriggerAssigneeFlow(t)
			f.expectSource(true)
			expectTriggerContextHistory(f.mock, f.outbox, triggerContextMessageRows(f.source))
			expectTeamGroupReadFence(f.mock, f.outbox.GroupID, f.outbox.ActorID, f.outbox.TeamID, nil, true)
			want := codes.Unavailable
			switch scenario {
			case "bad User echo":
				f.mode.Store(4)
			case "User unavailable":
				f.mode.Store(5)
			case "left group during lookup":
				want = codes.PermissionDenied
				expectTeamGroupReadFence(f.mock, f.outbox.GroupID, f.outbox.ActorID, f.outbox.TeamID, nil, false)
			}
			result, err := f.client.ResolveMember(context.Background(), f.outbox.MessageID, "张三")
			if result != nil || status.Code(err) != want || strings.Contains(err.Error(), "private") {
				t.Fatalf("unsafe rejected result %#v %v", result, err)
			}
			if f.lookupCalls.Load() != 1 {
				t.Fatal("lookup was retried or not reached")
			}
		})
	}
}

func TestTriggerAssigneeFlowRequiresSavedAuthorizedLiteralAndTrustedCertificate(t *testing.T) {
	for _, scenario := range []string{"invented name", "left group", "wrong certificate"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTriggerAssigneeFlow(t)
			client := f.client
			want := codes.PermissionDenied
			name := "张三"
			switch scenario {
			case "invented name":
				name = "李四"
				want = codes.InvalidArgument
				f.expectSource(true)
				expectTriggerContextHistory(f.mock, f.outbox, triggerContextMessageRows(f.source))
				expectTeamGroupReadFence(f.mock, f.outbox.GroupID, f.outbox.ActorID, f.outbox.TeamID, nil, true)
			case "left group":
				f.expectSource(false)
			case "wrong certificate":
				client = f.newClient(f.otherFiles)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			result, err := client.ResolveMember(ctx, f.outbox.MessageID, name)
			if result != nil || err == nil || scenario != "wrong certificate" && status.Code(err) != want {
				t.Fatalf("unverified source accepted %#v %v", result, err)
			}
			if f.lookupCalls.Load() != 0 {
				t.Fatal("lookup escaped source/identity guard")
			}
		})
	}
}
