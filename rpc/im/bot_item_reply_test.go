package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func botItemRequest(i botSendIntent, index int32) *pb.PostTaskCreatedCardItemRequest {
	return &pb.PostTaskCreatedCardItemRequest{RunId: i.RunID, ItemIndex: &index, TeamId: i.TeamID, GroupId: i.GroupID, Content: i.Content}
}

func TestBotItemReplyUsesExactItemIdentityAndOriginalToken(t *testing.T) {
	for _, runID := range []int64{9, 9007199254740993, math.MaxInt64} {
		for _, index := range []int32{0, 1, 4} {
			i := validBotIntent(t)
			i.RunID, i.ItemIndex = runID, index
			msgID, err := model.BotTaskItemMsgID(runID, index)
			if err != nil {
				t.Fatal(err)
			}
			t.Run(msgID, func(t *testing.T) {
				im, mock := testIMServer(t)
				ctx := botAgentContext(t)
				incoming, _ := metadata.FromIncomingContext(ctx)
				checks := 0
				im.teamClient = teamCheckFunc(func(ctx context.Context, req *userpb.CheckTeamMemberRequest) error {
					checks++
					outgoing, _ := metadata.FromOutgoingContext(ctx)
					if req.GetTeamId() != i.TeamID || len(outgoing.Get("authorization")) != 1 || outgoing.Get("authorization")[0] != incoming.Get("authorization")[0] {
						t.Fatal("original Token or team was changed")
					}
					return nil
				})
				expectBotAccess(mock)
				expectBotProfile(mock, true)
				calls := 0
				s := &botReplyServer{im: im, botCode: "task-assistant", agentDNSName: "agent.go-im.internal", publisher: botPublisherFunc(func(ctx context.Context, got botSendIntent) (string, error) {
					calls++
					if got != i {
						t.Fatalf("trusted intent changed: got=%+v want=%+v", got, i)
					}
					if _, ok := ctx.Deadline(); !ok {
						t.Fatal("missing request budget")
					}
					return msgID, nil
				})}
				response, err := s.PostTaskCreatedCardItem(ctx, botItemRequest(i, index))
				if err != nil || !response.GetAccepted() || response.GetMsgId() != msgID || calls != 1 || checks != 1 {
					t.Fatalf("response=%v err=%v publish=%d membership=%d", response, err, calls, checks)
				}
			})
		}
	}
}

func TestBotItemReplyRejectsMalformedRequestBeforeDependencies(t *testing.T) {
	for _, name := range []string{"nil", "missing index", "negative index", "index five", "max index", "zero run", "negative run", "zero team", "zero group", "invalid card", "unknown card field"} {
		t.Run(name, func(t *testing.T) {
			im, _ := testIMServer(t)
			i := validBotIntent(t)
			req := botItemRequest(i, 1)
			switch name {
			case "nil":
				req = nil
			case "missing index":
				req.ItemIndex = nil
			case "negative index":
				*req.ItemIndex = -1
			case "index five":
				*req.ItemIndex = 5
			case "max index":
				*req.ItemIndex = math.MaxInt32
			case "zero run":
				req.RunId = 0
			case "negative run":
				req.RunId = -1
			case "zero team":
				req.TeamId = 0
			case "zero group":
				req.GroupId = 0
			case "invalid card":
				req.Content = `{"version":2,"task_id":"9","title":"bad"}`
			case "unknown card field":
				req.Content = `{"version":1,"task_id":"9","title":"bad","unknown":true}`
			}
			s := &botReplyServer{im: im, botCode: "task-assistant", agentDNSName: "agent.go-im.internal", publisher: botPublisherFunc(func(context.Context, botSendIntent) (string, error) {
				t.Fatal("invalid request published")
				return "", nil
			})}
			for _, check := range []struct {
				ctx  context.Context
				want codes.Code
			}{{historyContext(t), codes.Unauthenticated}, {botAgentContext(t), codes.InvalidArgument}} {
				response, err := s.PostTaskCreatedCardItem(check.ctx, req)
				if response != nil || status.Code(err) != check.want {
					t.Fatalf("response=%v err=%v want=%v", response, err, check.want)
				}
			}
		})
	}
}

func TestBotItemReplyRequiresVerifiedExactSANAndTLS13(t *testing.T) {
	for _, name := range []string{"wrong SAN", "CN only", "unverified", "TLS12"} {
		t.Run(name, func(t *testing.T) {
			im, _ := testIMServer(t)
			cert := &x509.Certificate{DNSNames: []string{"agent.go-im.internal"}}
			state := tls.ConnectionState{Version: tls.VersionTLS13, PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}
			switch name {
			case "wrong SAN":
				cert.DNSNames = []string{"other.agent.go-im.internal"}
			case "CN only":
				cert.DNSNames = nil
				cert.Subject = pkix.Name{CommonName: "agent.go-im.internal"}
			case "unverified":
				state.VerifiedChains = nil
			case "TLS12":
				state.Version = tls.VersionTLS12
			}
			ctx := peer.NewContext(historyContext(t), &peer.Peer{AuthInfo: credentials.TLSInfo{State: state}})
			s := &botReplyServer{im: im, agentDNSName: "agent.go-im.internal", publisher: botPublisherFunc(func(context.Context, botSendIntent) (string, error) {
				t.Fatal("unauthenticated transport published")
				return "", nil
			})}
			response, err := s.PostTaskCreatedCardItem(ctx, botItemRequest(validBotIntent(t), 1))
			if response != nil || status.Code(err) != codes.Unauthenticated {
				t.Fatalf("response=%v err=%v", response, err)
			}
		})
	}
}

func TestBotItemReplyRechecksCurrentPermissionsAndEnabledBot(t *testing.T) {
	for _, name := range []string{"no token", "not group member", "left team", "wrong team", "disabled bot"} {
		t.Run(name, func(t *testing.T) {
			im, mock := testIMServer(t)
			im.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error {
				if name == "left team" {
					return status.Error(codes.PermissionDenied, "left team")
				}
				return nil
			})
			req := botItemRequest(validBotIntent(t), 4)
			ctx := botAgentContext(t)
			want := codes.PermissionDenied
			switch name {
			case "no token":
				ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("user_id", "42", "agent_identity", "agent.go-im.internal"))
				want = codes.Unauthenticated
			case "not group member":
				mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).WillReturnRows(sqlmock.NewRows([]string{"team_id"}))
			case "left team":
				mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(200))
			case "wrong team":
				req.TeamId = 201
				expectBotAccess(mock)
				want = codes.NotFound
			case "disabled bot":
				expectBotAccess(mock)
				expectBotProfile(mock, false)
				want = codes.FailedPrecondition
			}
			s := &botReplyServer{im: im, botCode: "task-assistant", agentDNSName: "agent.go-im.internal", publisher: botPublisherFunc(func(context.Context, botSendIntent) (string, error) {
				t.Fatal("unauthorized item published")
				return "", nil
			})}
			response, err := s.PostTaskCreatedCardItem(ctx, req)
			if response != nil || status.Code(err) != want {
				t.Fatalf("response=%v err=%v want=%v", response, err, want)
			}
		})
	}
}

func TestBotItemReplyRejectsPublisherIdentityMismatch(t *testing.T) {
	for _, wrongID := range []string{"", "bot-task:9", "bot-task:9:4", "bot-task:10:1"} {
		t.Run(wrongID, func(t *testing.T) {
			im, mock := testIMServer(t)
			im.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error { return nil })
			expectBotAccess(mock)
			expectBotProfile(mock, true)
			calls := 0
			s := &botReplyServer{im: im, botCode: "task-assistant", agentDNSName: "agent.go-im.internal", publisher: botPublisherFunc(func(context.Context, botSendIntent) (string, error) {
				calls++
				return wrongID, nil
			})}
			response, err := s.PostTaskCreatedCardItem(botAgentContext(t), botItemRequest(validBotIntent(t), 1))
			if response != nil || status.Code(err) != codes.Internal || calls != 1 {
				t.Fatalf("response=%v err=%v calls=%d", response, err, calls)
			}
		})
	}
}

func TestBotItemReplyAcceptedReplayReauthorizesAndLegacyZeroMatches(t *testing.T) {
	im, mock := testIMServer(t)
	im.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error { return nil })
	i := validBotIntent(t)
	for n := 0; n < 2; n++ {
		expectBotAccess(mock)
		expectBotProfile(mock, true)
	}
	mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).WillReturnRows(sqlmock.NewRows([]string{"team_id"}))
	calls := 0
	s := &botReplyServer{im: im, botCode: "task-assistant", agentDNSName: "agent.go-im.internal", publisher: botPublisherFunc(func(_ context.Context, got botSendIntent) (string, error) {
		calls++
		if got != i {
			t.Fatalf("legacy/new item zero differ: %+v", got)
		}
		return "bot-task:9", nil
	})}
	ctx := botAgentContext(t)
	legacy, err := s.PostTaskCreatedCard(ctx, &pb.PostTaskCreatedCardRequest{RunId: i.RunID, TeamId: i.TeamID, GroupId: i.GroupID, Content: i.Content})
	if err != nil || !legacy.GetAccepted() {
		t.Fatalf("legacy=%v err=%v", legacy, err)
	}
	req := botItemRequest(i, 0)
	response, err := s.PostTaskCreatedCardItem(ctx, req)
	if err != nil || !response.GetAccepted() || response.GetMsgId() != legacy.GetMsgId() {
		t.Fatalf("item=%v err=%v legacy=%v", response, err, legacy)
	}
	response, err = s.PostTaskCreatedCardItem(ctx, req)
	if response != nil || status.Code(err) != codes.PermissionDenied || calls != 2 {
		t.Fatalf("revoked replay=%v err=%v publish=%d", response, err, calls)
	}
}
