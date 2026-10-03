package agent

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/yjydist/go-im/rpc/agent/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type answerFunc func(context.Context, string, int64, int64, string) (string, error)

func (f answerFunc) Answer(ctx context.Context, token string, teamID, groupID int64, question string) (string, error) {
	return f(ctx, token, teamID, groupID, question)
}

func testAskClient(t *testing.T, answerer Answerer) pb.AgentClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	pb.RegisterAgentServer(server, NewServer(answerer))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewAgentClient(conn)
}

func askContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer user-token")), cancel
}

func TestAskForwardsAuthorizedScopeOverRPC(t *testing.T) {
	calls := 0
	client := testAskClient(t, answerFunc(func(ctx context.Context, token string, teamID, groupID int64, question string) (string, error) {
		calls++
		if token != "user-token" || teamID != 200 || groupID != 300 || question != "Summarize tasks" {
			t.Errorf("wrong answerer input: %q %d %d %q", token, teamID, groupID, question)
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > askTimeout {
			t.Errorf("missing Ask timeout: %v %v", deadline, ok)
		}
		return "Two tasks", nil
	}))
	ctx, cancel := askContext()
	defer cancel()
	resp, err := client.Ask(ctx, &pb.AskRequest{TeamId: 200, GroupId: 300, Question: " Summarize tasks "})
	if err != nil || resp.GetAnswer() != "Two tasks" || calls != 1 {
		t.Fatalf("Ask = %v, %v; calls = %d", resp, err, calls)
	}
}

func TestAskRejectsInvalidRequestBeforeAnswerer(t *testing.T) {
	client := testAskClient(t, answerFunc(func(context.Context, string, int64, int64, string) (string, error) {
		t.Fatal("invalid request reached answerer")
		return "", nil
	}))
	valid := &pb.AskRequest{TeamId: 200, GroupId: 300, Question: "summarize"}
	for _, tc := range []struct {
		name string
		ctx  context.Context
		req  *pb.AskRequest
		code codes.Code
	}{
		{"missing login", context.Background(), valid, codes.Unauthenticated},
		{"missing team", authorizedTestContext(), &pb.AskRequest{GroupId: 300, Question: "summarize"}, codes.InvalidArgument},
		{"missing group", authorizedTestContext(), &pb.AskRequest{TeamId: 200, Question: "summarize"}, codes.InvalidArgument},
		{"empty question", authorizedTestContext(), &pb.AskRequest{TeamId: 200, GroupId: 300, Question: "  "}, codes.InvalidArgument},
		{"long question", authorizedTestContext(), &pb.AskRequest{TeamId: 200, GroupId: 300, Question: strings.Repeat("中", 2001)}, codes.InvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(tc.ctx, time.Second)
			defer cancel()
			resp, err := client.Ask(ctx, tc.req)
			if resp != nil || status.Code(err) != tc.code {
				t.Fatalf("Ask = %v, %v", resp, err)
			}
		})
	}
}

func authorizedTestContext() context.Context {
	return metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer user-token"))
}

func TestAskPreservesPermissionDenialAndHasNoDefaultAnswerer(t *testing.T) {
	client := testAskClient(t, answerFunc(func(context.Context, string, int64, int64, string) (string, error) {
		return "", status.Error(codes.PermissionDenied, "group membership required")
	}))
	ctx, cancel := askContext()
	defer cancel()
	req := &pb.AskRequest{TeamId: 200, GroupId: 300, Question: "summarize"}
	resp, err := client.Ask(ctx, req)
	if resp != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("permission error = %v, %v", resp, err)
	}
	resp, err = testAskClient(t, nil).Ask(ctx, req)
	if resp != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("unconfigured answerer = %v, %v", resp, err)
	}
}

func TestAskHonorsItsOwnDeadline(t *testing.T) {
	server := NewServer(answerFunc(func(ctx context.Context, _ string, _, _ int64, _ string) (string, error) {
		deadline, ok := ctx.Deadline()
		if remaining := time.Until(deadline); !ok || remaining < askTimeout-time.Second || remaining > askTimeout {
			t.Errorf("Ask deadline = %v, present = %v", deadline, ok)
		}
		return "answer", nil
	}))
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer user-token"))
	resp, err := server.Ask(ctx, &pb.AskRequest{TeamId: 200, GroupId: 300, Question: "summarize"})
	if err != nil || resp.GetAnswer() != "answer" {
		t.Fatalf("Ask = %v, %v", resp, err)
	}
}
