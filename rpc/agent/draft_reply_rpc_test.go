package agent

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type memoryReplyStore struct {
	record                         draftReplyRecord
	prepareErr, readErr, acceptErr error
}

func (s *memoryReplyStore) prepareReply(_ context.Context, run taskDraftRun) (draftReplyRecord, error) {
	if s.prepareErr != nil {
		return draftReplyRecord{}, s.prepareErr
	}
	expected, err := replyIntent(run)
	if err != nil {
		return draftReplyRecord{}, err
	}
	if s.record.RunID == 0 {
		s.record = expected
	}
	if !s.record.matches(run) {
		return draftReplyRecord{}, status.Error(codes.AlreadyExists, "conflict")
	}
	return s.record, nil
}
func (s *memoryReplyStore) loadReply(_ context.Context, _ taskDraftRun) (draftReplyRecord, bool, error) {
	return s.record, s.record.RunID > 0, s.readErr
}
func (s *memoryReplyStore) acceptReply(_ context.Context, _ draftReplyRecord) error {
	if s.acceptErr != nil {
		return s.acceptErr
	}
	s.record.Accepted = true
	return nil
}

type draftBotFunc func(context.Context, *impb.PostTaskCreatedCardRequest) (*impb.PostTaskCreatedCardResponse, error)

func (f draftBotFunc) PostTaskCreatedCard(ctx context.Context, req *impb.PostTaskCreatedCardRequest, _ ...grpc.CallOption) (*impb.PostTaskCreatedCardResponse, error) {
	return f(ctx, req)
}

func replyRPCClient(t *testing.T, impl *Server) pb.AgentClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	pb.RegisterAgentServer(server, impl)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///reply-test", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewAgentClient(conn)
}

// Same fake durable records across reconstructed services model response loss,
// not real database/process crash recovery.
func TestConfirmThenExplicitReplyRetryNeverRecreatesTask(t *testing.T) {
	for _, failure := range []string{"IM response lost", "Agent acceptance save lost"} {
		t.Run(failure, func(t *testing.T) {
			run := confirmationRun()
			store := &memoryReplyStore{}
			taskCalls, botCalls := 0, 0
			var firstContent string
			bot := draftBotFunc(func(ctx context.Context, req *impb.PostTaskCreatedCardRequest) (*impb.PostTaskCreatedCardResponse, error) {
				botCalls++
				md, _ := metadata.FromOutgoingContext(ctx)
				if md.Get("authorization")[0] != "Bearer user-token" || len(md.Get("idempotency-key")) != 0 ||
					run.Status != draftSucceeded || run.TaskID <= 0 || store.record.RunID != run.ID {
					t.Error("reply called before saved result/intent or with caller metadata")
				}
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > draftBotTimeout {
					t.Error("unbounded bot call")
				}
				if req.GetRunId() != 9001 || req.GetTeamId() != 200 || req.GetGroupId() != 300 || req.GetContent() != store.record.Content {
					t.Errorf("scope = %v", req)
				}
				if botCalls == 1 {
					firstContent = req.GetContent()
				} else if firstContent != req.GetContent() {
					t.Error("retry changed frozen content")
				}
				if botCalls == 1 && failure == "IM response lost" {
					return nil, status.Error(codes.DeadlineExceeded, "private transport")
				}
				return &impb.PostTaskCreatedCardResponse{MsgId: "bot-task:9001", Accepted: true}, nil
			})
			if failure == "Agent acceptance save lost" {
				store.acceptErr = status.Error(codes.Unavailable, "save failed")
			}
			makeServer := func() *Server {
				s := NewServer(nil)
				s.draftReader = confirmationReader(t, func(context.Context, int64, int64) (taskDraftRun, error) { return run, nil }, nil)
				s.confirmer = &draftConfirmer{store: confirmationStoreStub{
					freeze: func(context.Context, int64, int64, string, string) (taskDraftRun, error) {
						if run.Status != draftSucceeded {
							run.Status, run.TaskRequestKey = draftCreating, draftTaskRequestKey(run.ID)
						}
						return run, nil
					},
					complete: func(_ context.Context, _ int64, _ int64, _ string, id int64) (taskDraftRun, error) {
						run.Status, run.TaskID = draftSucceeded, id
						return run, nil
					},
				}, tasks: draftTaskCreateFunc(func(context.Context, *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
					taskCalls++
					return &taskpb.CreateTaskResponse{TaskId: 9007199254740993}, nil
				})}
				s.replier = &draftReplier{store: store, bot: bot}
				return s
			}
			ctx, cancel := draftRPCContext()
			defer cancel()
			ctx = metadata.AppendToOutgoingContext(ctx, "idempotency-key", "caller-key")
			client := replyRPCClient(t, makeServer())
			resp, err := client.ConfirmTaskDraft(ctx, confirmDraftRequest())
			if err != nil || resp.GetStatus() != "succeeded" || resp.GetTaskId() != run.TaskID || resp.GetReplyStatus() != "pending" || resp.GetReplyMsgId() != "bot-task:9001" || taskCalls != 1 || botCalls != 1 {
				t.Fatalf("confirm = %v, %v, task=%d bot=%d", resp, err, taskCalls, botCalls)
			}
			if _, err := client.ConfirmTaskDraft(ctx, confirmDraftRequest()); err != nil || taskCalls != 1 || botCalls != 1 {
				t.Fatalf("confirmation replay posted or created: %v", err)
			}
			resp, err = client.GetTaskDraft(ctx, &pb.GetTaskDraftRequest{RunId: 9001})
			if err != nil || resp.GetReplyStatus() != "pending" || botCalls != 1 {
				t.Fatalf("read = %v, %v", resp, err)
			}
			store.acceptErr = nil
			client = replyRPCClient(t, makeServer())
			resp, err = client.RetryTaskReply(ctx, &pb.GetTaskDraftRequest{RunId: 9001})
			if err != nil || resp.GetReplyStatus() != "accepted" || taskCalls != 1 || botCalls != 2 {
				t.Fatalf("retry = %v, %v, task=%d bot=%d", resp, err, taskCalls, botCalls)
			}
			resp, err = client.RetryTaskReply(ctx, &pb.GetTaskDraftRequest{RunId: 9001})
			if err != nil || resp.GetReplyStatus() != "accepted" || botCalls != 2 {
				t.Fatalf("accepted replay = %v, %v", resp, err)
			}
		})
	}
}

func TestRetryReplyChecksCurrentAccessBeforeAnySideEffect(t *testing.T) {
	for _, tc := range []struct {
		name               string
		run                taskDraftRun
		loadErr, accessErr error
		want               codes.Code
	}{
		{"other initiator", succeededReplyRun(), status.Error(codes.NotFound, "missing"), nil, codes.NotFound},
		{"left group", succeededReplyRun(), nil, status.Error(codes.PermissionDenied, "left"), codes.PermissionDenied},
		{"waiting", confirmationRun(), nil, nil, codes.FailedPrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer(nil)
			store := &memoryReplyStore{}
			s.draftReader = confirmationReader(t, func(context.Context, int64, int64) (taskDraftRun, error) { return tc.run, tc.loadErr }, func(context.Context, *impb.CheckTeamGroupAccessRequest) error { return tc.accessErr })
			s.replier = &draftReplier{store: store, bot: draftBotFunc(func(context.Context, *impb.PostTaskCreatedCardRequest) (*impb.PostTaskCreatedCardResponse, error) {
				t.Error("rejected retry reached IM")
				return nil, errors.New("unexpected")
			})}
			ctx, cancel := draftRPCContext()
			defer cancel()
			resp, err := replyRPCClient(t, s).RetryTaskReply(ctx, &pb.GetTaskDraftRequest{RunId: 9001})
			if resp != nil || status.Code(err) != tc.want || store.record.RunID != 0 {
				t.Fatalf("rejected = %v, %v, %+v", resp, err, store.record)
			}
		})
	}
}

func TestReplyReadFailureAndBadIMAcceptanceDoNotBecomeAccepted(t *testing.T) {
	run := succeededReplyRun()
	store := &memoryReplyStore{}
	for _, resp := range []*impb.PostTaskCreatedCardResponse{nil, {MsgId: "other", Accepted: true}, {MsgId: "bot-task:9001", Accepted: false}} {
		r := &draftReplier{store: store, bot: draftBotFunc(func(context.Context, *impb.PostTaskCreatedCardRequest) (*impb.PostTaskCreatedCardResponse, error) {
			return resp, nil
		})}
		if got, err := r.attempt(context.Background(), "user-token", run); status.Code(err) != codes.Unavailable || got.Accepted || store.record.Accepted {
			t.Fatalf("bad result = %+v, %v", got, err)
		}
	}
	store.readErr = status.Error(codes.Unavailable, "storage unavailable")
	s := NewServer(nil)
	s.replier = &draftReplier{store: store}
	if resp, err := s.draftResponseWithReply(context.Background(), run); resp != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("read error = %v, %v", resp, err)
	}
}

func TestSavedTaskSurvivesReplyIntentWriteFailure(t *testing.T) {
	run := succeededReplyRun()
	authorized := run
	authorized.Status, authorized.TaskID, authorized.TaskRequestKey = draftCreating, 0, draftTaskRequestKey(run.ID)
	s := NewServer(nil)
	s.draftReader = confirmationReader(t, func(context.Context, int64, int64) (taskDraftRun, error) { return authorized, nil }, nil)
	s.confirmer = &draftConfirmer{store: confirmationStoreStub{
		freeze: func(context.Context, int64, int64, string, string) (taskDraftRun, error) { return run, nil },
	}, tasks: draftTaskCreateFunc(func(context.Context, *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
		t.Error("saved task recreated")
		return nil, errors.New("unexpected")
	})}
	store := &memoryReplyStore{prepareErr: status.Error(codes.Unavailable, "storage unavailable")}
	s.replier = &draftReplier{store: store, bot: draftBotFunc(func(context.Context, *impb.PostTaskCreatedCardRequest) (*impb.PostTaskCreatedCardResponse, error) {
		t.Error("IM called without durable intent")
		return nil, errors.New("unexpected")
	})}
	ctx, cancel := draftRPCContext()
	defer cancel()
	resp, err := replyRPCClient(t, s).ConfirmTaskDraft(ctx, confirmDraftRequest())
	if err != nil || resp.GetStatus() != "succeeded" || resp.GetTaskId() != run.TaskID || resp.GetReplyStatus() != "unknown" || store.record.RunID != 0 {
		t.Fatalf("saved task = %v, %v", resp, err)
	}
	response, err := s.draftResponseWithReply(context.Background(), run)
	if err != nil || response.GetReplyStatus() != "not_started" {
		t.Fatalf("read after failed preparation = %v, %v", response, err)
	}
}

func TestReplyRetryRequiresTokenEvenForAcceptedRecord(t *testing.T) {
	s := NewServer(nil)
	resp, err := replyRPCClient(t, s).RetryTaskReply(context.Background(), &pb.GetTaskDraftRequest{RunId: 9001})
	if resp != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing Token = %v, %v", resp, err)
	}
	run := succeededReplyRun()
	record, _ := replyIntent(run)
	record.Accepted = true
	s.draftReader = confirmationReader(t, func(context.Context, int64, int64) (taskDraftRun, error) { return run, nil }, func(context.Context, *impb.CheckTeamGroupAccessRequest) error {
		return status.Error(codes.PermissionDenied, "left group")
	})
	s.replier = &draftReplier{store: &memoryReplyStore{record: record}, bot: draftBotFunc(func(context.Context, *impb.PostTaskCreatedCardRequest) (*impb.PostTaskCreatedCardResponse, error) {
		t.Error("lost-access replay reached IM")
		return nil, nil
	})}
	ctx, cancel := draftRPCContext()
	defer cancel()
	resp, err = replyRPCClient(t, s).RetryTaskReply(ctx, &pb.GetTaskDraftRequest{RunId: 9001})
	if resp != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("accepted replay lost access = %v, %v", resp, err)
	}
}
