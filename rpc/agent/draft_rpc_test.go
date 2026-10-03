package agent

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func testTaskDraftClient(t *testing.T, reader *draftAccessReader, confirmers ...*draftConfirmer) pb.AgentClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	impl := NewServer(nil)
	impl.draftReader = reader
	if len(confirmers) > 0 {
		impl.confirmer = confirmers[0]
	}
	pb.RegisterAgentServer(server, impl)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///draft-test", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewAgentClient(conn)
}

func draftRPCContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer user-token")), cancel
}

func TestGetTaskDraftReturnsOnlyAuthorizedDraftOverRPC(t *testing.T) {
	reader := testDraftAccessReader(t, 400,
		func(_ context.Context, runID, actorID int64) (taskDraftRun, error) {
			if runID != 9001 || actorID != 400 {
				t.Fatalf("load scope = %d, %d", runID, actorID)
			}
			return taskDraftRun{Revision: 1,
				ID: 9001, Scope: draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400},
				Status: draftWaitingConfirmation,
				Draft:  taskDraft{Title: "修复缓存", Description: "复核", AssigneeID: 500, DueAtUnixMs: 1000, SourceMessageID: 600, AssigneeName: "张三", AssigneeResolution: assigneeMatched},
			}, nil
		},
		func(_ context.Context, req *impb.CheckTeamGroupAccessRequest) error {
			if req.GetTeamId() != 200 || req.GetGroupId() != 300 {
				t.Fatalf("IM scope = %v", req)
			}
			return nil
		})
	ctx, cancel := draftRPCContext()
	defer cancel()
	resp, err := testTaskDraftClient(t, reader).GetTaskDraft(ctx, &pb.GetTaskDraftRequest{RunId: 9001})
	if err != nil || resp.GetRunId() != 9001 || resp.GetTeamId() != 200 || resp.GetGroupId() != 300 ||
		resp.GetStatus() != "waiting_confirmation" || resp.GetDraft().GetTitle() != "修复缓存" ||
		resp.GetDraft().GetAssigneeId() != 500 || resp.GetDraft().GetDueAtUnixMs() != 1000 ||
		resp.GetDraft().GetSourceMessageId() != 600 || resp.GetDraft().GetAssigneeName() != "张三" || resp.GetDraft().GetAssigneeResolution() != "matched" {
		t.Fatalf("draft response = %v, %v", resp, err)
	}
}

func TestGetTaskDraftRejectsUnauthenticatedAndUnavailable(t *testing.T) {
	client := testTaskDraftClient(t, nil)
	req := &pb.GetTaskDraftRequest{RunId: 9001}
	if resp, err := client.GetTaskDraft(context.Background(), req); resp != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token = %v, %v", resp, err)
	}
	ctx, cancel := draftRPCContext()
	defer cancel()
	if resp, err := client.GetTaskDraft(ctx, &pb.GetTaskDraftRequest{}); resp != nil || status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid ID = %v, %v", resp, err)
	}
	if resp, err := client.GetTaskDraft(ctx, req); resp != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("unconfigured reader = %v, %v", resp, err)
	}
}

func TestGetTaskDraftDoesNotReturnDataAfterIMDenial(t *testing.T) {
	reader := testDraftAccessReader(t, 400,
		func(context.Context, int64, int64) (taskDraftRun, error) {
			return taskDraftRun{Revision: 1, ID: 9001, Scope: draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}, Draft: taskDraft{Title: "private"}}, nil
		},
		func(context.Context, *impb.CheckTeamGroupAccessRequest) error {
			return status.Error(codes.PermissionDenied, "left team")
		})
	ctx, cancel := draftRPCContext()
	defer cancel()
	resp, err := testTaskDraftClient(t, reader).GetTaskDraft(ctx, &pb.GetTaskDraftRequest{RunId: 9001})
	if resp != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("denied draft = %v, %v", resp, err)
	}
}
