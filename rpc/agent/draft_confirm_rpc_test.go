package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type confirmationStoreStub struct {
	freeze   func(context.Context, int64, int64, string, string) (taskDraftRun, error)
	complete func(context.Context, int64, int64, string, int64) (taskDraftRun, error)
}

func (s confirmationStoreStub) freezeDraft(ctx context.Context, id, actor int64, title, description string, revision int64, reviewedID, reviewedDue *int64, reviewedResolution string) (taskDraftRun, error) {
	return s.freeze(ctx, id, actor, title, description)
}

func (s confirmationStoreStub) completeDraft(ctx context.Context, id, actor int64, key string, taskID int64) (taskDraftRun, error) {
	return s.complete(ctx, id, actor, key, taskID)
}

type draftTaskCreateFunc func(context.Context, *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error)

func (f draftTaskCreateFunc) CreateTask(ctx context.Context, req *taskpb.CreateTaskRequest, _ ...grpc.CallOption) (*taskpb.CreateTaskResponse, error) {
	return f(ctx, req)
}

func confirmDraftRequest() *pb.ConfirmTaskDraftRequest {
	return &pb.ConfirmTaskDraftRequest{ExpectedRevision: 1, RunId: 9001, ExpectedTitle: "修复缓存", ExpectedDescription: "复核", ExpectedDueAtUnixMs: draftID(1000)}
}

func TestConfirmNamedDraftRequiresAssigneeReviewBeforeFreezeOrTask(t *testing.T) {
	run := confirmationRun()
	run.Draft.AssigneeName = "张三"
	run.Draft.AssigneeResolution = assigneeMatched
	reader := confirmationReader(t, func(context.Context, int64, int64) (taskDraftRun, error) { return run, nil }, nil)
	confirmer := &draftConfirmer{store: confirmationStoreStub{freeze: func(context.Context, int64, int64, string, string) (taskDraftRun, error) {
		t.Error("named draft reached old freeze")
		return run, nil
	}}, tasks: draftTaskCreateFunc(func(context.Context, *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
		t.Error("named draft created task without review")
		return nil, nil
	})}
	ctx, cancel := draftRPCContext()
	defer cancel()
	response, err := testTaskDraftClient(t, reader, confirmer).ConfirmTaskDraft(ctx, confirmDraftRequest())
	if response != nil || status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("old confirmation: %v, %v", response, err)
	}
}

func confirmationReader(t *testing.T, load draftLoadFunc, check draftAccessFunc) *draftAccessReader {
	if check == nil {
		check = func(context.Context, *impb.CheckTeamGroupAccessRequest) error { return nil }
	}
	return testDraftAccessReader(t, 400, load, check)
}

// Keep storage across new RPC servers to model response loss/process restart;
// this tests orchestration, not actual MySQL durability or Task's unique index.
func TestConfirmTaskDraftReusesFrozenRequestAfterTaskResponseLossAndRestart(t *testing.T) {
	run := confirmationRun()
	store := confirmationStoreStub{
		freeze: func(_ context.Context, id, actor int64, title, description string) (taskDraftRun, error) {
			if id != 9001 || actor != 400 || title != run.Draft.Title || description != run.Draft.Description {
				t.Errorf("confirmation scope = %d, %d, %q, %q", id, actor, title, description)
			}
			if run.Status == draftWaitingConfirmation {
				run.Status, run.TaskRequestKey = draftCreating, "agent-task-9001-0"
			}
			return run, nil
		},
		complete: func(_ context.Context, id, actor int64, key string, taskID int64) (taskDraftRun, error) {
			if id != 9001 || actor != 400 || key != "agent-task-9001-0" || taskID != 9223372036854775806 {
				t.Errorf("completion = %d, %d, %q, %d", id, actor, key, taskID)
			}
			run.Status, run.TaskID = draftSucceeded, taskID
			return run, nil
		},
	}
	load := func(context.Context, int64, int64) (taskDraftRun, error) { return run, nil }
	var taskCalls int
	tasks := draftTaskCreateFunc(func(ctx context.Context, req *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
		taskCalls++
		md, _ := metadata.FromOutgoingContext(ctx)
		if values := md.Get("authorization"); len(values) != 1 || values[0] != "Bearer user-token" {
			t.Errorf("Task authorization = %v", values)
		}
		if values := md.Get("idempotency-key"); len(values) != 1 || values[0] != "agent-task-9001-0" {
			t.Errorf("Task request key = %v", values)
		}
		if req.GetTeamId() != 200 || req.GetTitle() != "修复缓存" || req.GetDescription() != "复核" ||
			req.GetAssigneeId() != 500 || req.GetDueAtUnixMs() != 1000 ||
			req.GetSourceGroupId() != 300 || req.GetSourceMessageId() != 600 || run.Status != draftCreating {
			t.Errorf("Task called before freeze or with wrong fields: %v, %+v", req, run)
		}
		if taskCalls == 1 {
			// A Task committed, but its response failed to reach Agent.
			return nil, status.Error(codes.DeadlineExceeded, "private transport detail")
		}
		return &taskpb.CreateTaskResponse{TaskId: 9223372036854775806}, nil
	})
	ctx, cancel := draftRPCContext()
	defer cancel()
	// A caller-supplied key must not reach Task.
	ctx = metadata.AppendToOutgoingContext(ctx, "idempotency-key", "browser-chosen-key")
	client := testTaskDraftClient(t, confirmationReader(t, load, nil), &draftConfirmer{store: store, tasks: tasks})
	resp, err := client.ConfirmTaskDraft(ctx, confirmDraftRequest())
	if resp != nil || status.Code(err) != codes.DeadlineExceeded || run.Status != draftCreating || run.TaskID != 0 {
		t.Fatalf("response loss = %v, %v, %+v", resp, err, run)
	}
	// New server/configuration, same persistent record and original key.
	client = testTaskDraftClient(t, confirmationReader(t, load, nil), &draftConfirmer{store: store, tasks: tasks})
	resp, err = client.ConfirmTaskDraft(ctx, confirmDraftRequest())
	if err != nil || resp.GetTaskId() != 9223372036854775806 || resp.GetStatus() != "succeeded" || taskCalls != 2 {
		t.Fatalf("retry = %v, %v, task calls %d", resp, err, taskCalls)
	}
	resp, err = client.ConfirmTaskDraft(ctx, confirmDraftRequest())
	if err != nil || resp.GetTaskId() != run.TaskID || taskCalls != 2 {
		t.Fatalf("duplicate confirm = %v, %v, task calls %d", resp, err, taskCalls)
	}
	resp, err = client.GetTaskDraft(ctx, &pb.GetTaskDraftRequest{RunId: 9001})
	if err != nil || resp.GetTaskId() != run.TaskID || resp.GetStatus() != "succeeded" {
		t.Fatalf("read result = %v, %v", resp, err)
	}
}

func TestConfirmTaskDraftRetriesSameTaskAfterAgentResultWriteFailure(t *testing.T) {
	run := confirmationRun()
	run.Status, run.TaskRequestKey = draftCreating, "agent-task-9001-0"
	var writes, calls int
	store := confirmationStoreStub{
		freeze: func(context.Context, int64, int64, string, string) (taskDraftRun, error) { return run, nil },
		complete: func(context.Context, int64, int64, string, int64) (taskDraftRun, error) {
			writes++
			if writes == 1 {
				return taskDraftRun{}, status.Error(codes.Unavailable, "draft storage unavailable")
			}
			run.Status, run.TaskID = draftSucceeded, 888
			return run, nil
		},
	}
	tasks := draftTaskCreateFunc(func(ctx context.Context, req *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
		calls++
		md, _ := metadata.FromOutgoingContext(ctx)
		if md.Get("idempotency-key")[0] != "agent-task-9001-0" || req.GetTitle() != run.Draft.Title {
			t.Errorf("changed retry: %v, %v", md, req)
		}
		return &taskpb.CreateTaskResponse{TaskId: 888}, nil
	})
	client := testTaskDraftClient(t, confirmationReader(t, func(context.Context, int64, int64) (taskDraftRun, error) {
		return run, nil
	}, nil), &draftConfirmer{store: store, tasks: tasks})
	ctx, cancel := draftRPCContext()
	defer cancel()
	if resp, err := client.ConfirmTaskDraft(ctx, confirmDraftRequest()); resp != nil || status.Code(err) != codes.Unavailable || run.Status != draftCreating {
		t.Fatalf("failed result save = %v, %v, %+v", resp, err, run)
	}
	resp, err := client.ConfirmTaskDraft(ctx, confirmDraftRequest())
	if err != nil || resp.GetTaskId() != 888 || calls != 2 || writes != 2 {
		t.Fatalf("result save retry = %v, %v, calls=%d, writes=%d", resp, err, calls, writes)
	}
}

func TestConfirmTaskDraftRejectsBeforeTaskWhenAccessOrFreezeFails(t *testing.T) {
	for _, tc := range []struct {
		name      string
		loadErr   error
		accessErr error
		freezeErr error
		want      codes.Code
	}{
		{"other initiator", status.Error(codes.NotFound, "not found"), nil, nil, codes.NotFound},
		{"left group", nil, status.Error(codes.PermissionDenied, "left group"), nil, codes.PermissionDenied},
		{"stale text", nil, nil, status.Error(codes.Aborted, "stale"), codes.Aborted},
		{"freeze failure", nil, nil, status.Error(codes.Unavailable, "database unavailable"), codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := confirmationReader(t, func(context.Context, int64, int64) (taskDraftRun, error) {
				return confirmationRun(), tc.loadErr
			}, func(context.Context, *impb.CheckTeamGroupAccessRequest) error { return tc.accessErr })
			store := confirmationStoreStub{freeze: func(context.Context, int64, int64, string, string) (taskDraftRun, error) {
				if tc.freezeErr == nil {
					t.Error("unauthorized confirmation reached storage")
				}
				return taskDraftRun{}, tc.freezeErr
			}}
			tasks := draftTaskCreateFunc(func(context.Context, *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
				t.Error("rejected confirmation reached Task")
				return nil, errors.New("unexpected call")
			})
			ctx, cancel := draftRPCContext()
			defer cancel()
			resp, err := testTaskDraftClient(t, reader, &draftConfirmer{store: store, tasks: tasks}).ConfirmTaskDraft(ctx, confirmDraftRequest())
			if resp != nil || status.Code(err) != tc.want {
				t.Fatalf("rejected = %v, %v", resp, err)
			}
		})
	}
}

func TestConfirmTaskDraftTaskFailuresKeepFrozenRecordAndMaskDetails(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		resp *taskpb.CreateTaskResponse
		want codes.Code
	}{
		{"permission", status.Error(codes.PermissionDenied, "private detail"), nil, codes.PermissionDenied},
		{"key conflict", status.Error(codes.AlreadyExists, "private detail"), nil, codes.AlreadyExists},
		{"unavailable", errors.New("private detail"), nil, codes.Unavailable},
		{"deadline", status.Error(codes.DeadlineExceeded, "private detail"), nil, codes.DeadlineExceeded},
		{"empty result", nil, &taskpb.CreateTaskResponse{}, codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := confirmationRun()
			run.Status, run.TaskRequestKey = draftCreating, "agent-task-9001-0"
			store := confirmationStoreStub{
				freeze: func(context.Context, int64, int64, string, string) (taskDraftRun, error) { return run, nil },
				complete: func(context.Context, int64, int64, string, int64) (taskDraftRun, error) {
					t.Error("Task failure marked complete")
					return taskDraftRun{}, errors.New("unexpected completion")
				},
			}
			tasks := draftTaskCreateFunc(func(context.Context, *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
				return tc.resp, tc.err
			})
			reader := confirmationReader(t, func(context.Context, int64, int64) (taskDraftRun, error) { return run, nil }, nil)
			client := testTaskDraftClient(t, reader, &draftConfirmer{store: store, tasks: tasks})
			ctx, cancel := draftRPCContext()
			defer cancel()
			resp, err := client.ConfirmTaskDraft(ctx, confirmDraftRequest())
			if resp != nil || status.Code(err) != tc.want || strings.Contains(err.Error(), "private detail") {
				t.Fatalf("Task failure = %v, %v", resp, err)
			}
			resp, err = client.GetTaskDraft(ctx, &pb.GetTaskDraftRequest{RunId: 9001})
			if err != nil || resp.GetStatus() != "creating" || resp.GetTaskId() != 0 {
				t.Fatalf("pending after error = %v, %v", resp, err)
			}
		})
	}
}

func TestConfirmTaskDraftWithoutSourceDoesNotSendSourceGroup(t *testing.T) {
	run := confirmationRun()
	run.Status, run.TaskRequestKey, run.Draft.SourceMessageID = draftCreating, "agent-task-9001-0", 0
	store := confirmationStoreStub{
		freeze: func(context.Context, int64, int64, string, string) (taskDraftRun, error) { return run, nil },
		complete: func(context.Context, int64, int64, string, int64) (taskDraftRun, error) {
			run.Status, run.TaskID = draftSucceeded, 888
			return run, nil
		},
	}
	tasks := draftTaskCreateFunc(func(_ context.Context, req *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
		if req.GetSourceGroupId() != 0 || req.GetSourceMessageId() != 0 {
			t.Errorf("unexpected source = %v", req)
		}
		return &taskpb.CreateTaskResponse{TaskId: 888}, nil
	})
	ctx, cancel := draftRPCContext()
	defer cancel()
	reader := confirmationReader(t, func(context.Context, int64, int64) (taskDraftRun, error) { return run, nil }, nil)
	if resp, err := testTaskDraftClient(t, reader, &draftConfirmer{store: store, tasks: tasks}).ConfirmTaskDraft(ctx, confirmDraftRequest()); err != nil || resp.GetTaskId() != 888 {
		t.Fatalf("sourceless confirmation = %v, %v", resp, err)
	}
}

func TestConfirmTaskDraftValidatesRequestAndRequiresConfiguration(t *testing.T) {
	client := testTaskDraftClient(t, nil)
	if resp, err := client.ConfirmTaskDraft(context.Background(), confirmDraftRequest()); resp != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token = %v, %v", resp, err)
	}
	ctx, cancel := draftRPCContext()
	defer cancel()
	for _, req := range []*pb.ConfirmTaskDraftRequest{{}, {RunId: 9001}, {RunId: 9001, ExpectedTitle: strings.Repeat("字", 201)}} {
		if resp, err := client.ConfirmTaskDraft(ctx, req); resp != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid request = %v, %v", resp, err)
		}
	}
	if resp, err := client.ConfirmTaskDraft(ctx, confirmDraftRequest()); resp != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("missing confirmation dependencies = %v, %v", resp, err)
	}
}
