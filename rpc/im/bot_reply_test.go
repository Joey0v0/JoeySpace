package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type botPublisherFunc func(context.Context, botSendIntent) (string, error)

func (f botPublisherFunc) Publish(ctx context.Context, i botSendIntent) (string, error) {
	return f(ctx, i)
}

func botAgentContext(t *testing.T) context.Context {
	cert := &x509.Certificate{DNSNames: []string{"agent.go-im.internal"}}
	return peer.NewContext(historyContext(t), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
		Version: tls.VersionTLS13, PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}},
	}}})
}

func expectBotAccess(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(200))
	mock.ExpectQuery(regexp.QuoteMeta(historyGroupQuery)).WithArgs(int64(300), 1).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(200))
}

func expectBotProfile(mock sqlmock.Sqlmock, enabled bool) {
	state := 2
	if enabled {
		state = 1
	}
	mock.ExpectQuery(regexp.QuoteMeta(botProfileQuery)).WithArgs("task-assistant", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "code", "display_name", "status"}).AddRow(7, "task-assistant", "AI 助手", state))
}

func TestBotReplyChecksBothIdentitiesAndCurrentScopeBeforePublish(t *testing.T) {
	for _, name := range []string{"success", "no TLS", "no token", "invalid card", "invalid scope", "not group member", "left team", "wrong team", "disabled bot", "publisher failure"} {
		t.Run(name, func(t *testing.T) {
			im, mock := testIMServer(t)
			im.teamClient = teamCheckFunc(func(ctx context.Context, req *userpb.CheckTeamMemberRequest) error {
				md, _ := metadata.FromOutgoingContext(ctx)
				if req.GetTeamId() != 200 || len(md.Get("authorization")) != 1 {
					t.Fatal("lost original scope or Token")
				}
				if name == "left team" {
					return status.Error(codes.PermissionDenied, "left team")
				}
				return nil
			})
			i := validBotIntent(t)
			req := &pb.PostTaskCreatedCardRequest{RunId: i.RunID, TeamId: i.TeamID, GroupId: i.GroupID, Content: i.Content}
			ctx := botAgentContext(t)
			want := codes.OK
			switch name {
			case "no TLS":
				ctx = historyContext(t)
				want = codes.Unauthenticated
			case "no token":
				ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("user_id", "42", "agent_identity", "agent.go-im.internal"))
				want = codes.Unauthenticated
			case "invalid card":
				req.Content = `{"version":2,"task_id":"9","title":"bad"}`
				want = codes.InvalidArgument
			case "invalid scope":
				req.RunId = 0
				want = codes.InvalidArgument
			case "not group member":
				want = codes.PermissionDenied
			case "left team":
				want = codes.PermissionDenied
			case "wrong team":
				req.TeamId = 201
				want = codes.NotFound
			case "disabled bot":
				want = codes.FailedPrecondition
			case "publisher failure":
				want = codes.Unavailable
			}
			if name == "not group member" {
				mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).WillReturnRows(sqlmock.NewRows([]string{"team_id"}))
			} else if name == "left team" {
				mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(200))
			} else if name == "wrong team" {
				expectBotAccess(mock)
			} else if name == "success" || name == "disabled bot" || name == "publisher failure" {
				expectBotAccess(mock)
				expectBotProfile(mock, name != "disabled bot")
			}
			calls := 0
			s := &botReplyServer{im: im, botCode: "task-assistant", agentDNSName: "agent.go-im.internal", publisher: botPublisherFunc(func(ctx context.Context, got botSendIntent) (string, error) {
				calls++
				if got != i {
					t.Fatalf("caller did not determine trusted identity: %+v", got)
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("missing request budget")
				}
				if name == "publisher failure" {
					return "", status.Error(codes.Unavailable, "uncertain")
				}
				return "bot-task:9", nil
			})}
			response, err := s.PostTaskCreatedCard(ctx, req)
			if status.Code(err) != want {
				t.Fatalf("response=%v err=%v want=%v", response, err, want)
			}
			if name == "success" {
				if !response.GetAccepted() || response.GetMsgId() != "bot-task:9" || calls != 1 {
					t.Fatal(response)
				}
			} else {
				if response != nil || name != "publisher failure" && calls != 0 {
					t.Fatalf("unauthorized or failed send: %v calls=%d", response, calls)
				}
			}
		})
	}
}
