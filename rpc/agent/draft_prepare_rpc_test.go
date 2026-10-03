package agent

import (
	"context"
	"net"
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

func testPrepareDraftClient(t *testing.T, p *draftPreparer) pb.AgentClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	impl := NewServer(nil)
	impl.preparer = p
	pb.RegisterAgentServer(server, impl)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///prepare-draft-test", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewAgentClient(conn)
}

func preparedRPCContext(headers ...string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	return metadata.NewOutgoingContext(ctx, metadata.Pairs(headers...)), cancel
}

func TestPrepareTaskDraftOverRPCUsesOriginalTokenAndKey(t *testing.T) {
	client := testPrepareDraftClient(t, testDraftPreparer(t))
	ctx, cancel := preparedRPCContext("authorization", "Bearer user-token", "idempotency-key", "request-1")
	defer cancel()
	resp, err := client.PrepareTaskDraft(ctx, &pb.PrepareTaskDraftRequest{TeamId: 200, GroupId: 300, Instruction: "提取待办"})
	if err != nil || resp.GetRunId() <= 0 {
		t.Fatalf("prepare = %v, %v", resp, err)
	}
}

func TestPrepareTaskDraftRejectsInvalidHeadersAndScope(t *testing.T) {
	client := testPrepareDraftClient(t, testDraftPreparer(t))
	req := &pb.PrepareTaskDraftRequest{TeamId: 200, GroupId: 300, Instruction: "提取待办"}
	for _, tc := range []struct {
		headers []string
		req     *pb.PrepareTaskDraftRequest
		code    codes.Code
	}{
		{nil, req, codes.Unauthenticated},
		{[]string{"authorization", "Bearer user-token"}, req, codes.InvalidArgument},
		{[]string{"authorization", "Bearer user-token", "idempotency-key", "bad key"}, req, codes.InvalidArgument},
		{[]string{"authorization", "Bearer user-token", "idempotency-key", "request-1"}, &pb.PrepareTaskDraftRequest{GroupId: 300, Instruction: "提取待办"}, codes.InvalidArgument},
	} {
		ctx, cancel := preparedRPCContext(tc.headers...)
		resp, err := client.PrepareTaskDraft(ctx, tc.req)
		cancel()
		if resp != nil || status.Code(err) != tc.code {
			t.Fatalf("invalid request = %v, %v", resp, err)
		}
	}
}
