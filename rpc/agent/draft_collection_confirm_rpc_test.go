package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// SQL atomicity is tested separately. This durable-intent substitute survives
// new RPC servers to exercise uncertain results and explicit retries.
type collectionConfirmationMemoryStore struct {
	mu          sync.Mutex
	collection  taskDraftCollection
	freezeErr   error
	completeErr error
	freezes     int
	completions int
}

func copyConfirmationCollection(c taskDraftCollection) taskDraftCollection {
	c.Items = append([]taskDraftRun(nil), c.Items...)
	return c
}
func (s *collectionConfirmationMemoryStore) snapshot() taskDraftCollection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return copyConfirmationCollection(s.collection)
}
func (s *collectionConfirmationMemoryStore) loadDraftForInitiator(context.Context, int64, int64) (taskDraftRun, error) {
	return taskDraftRun{}, status.Error(codes.FailedPrecondition, "collection used single load")
}
func (s *collectionConfirmationMemoryStore) loadDraftCollectionForInitiator(_ context.Context, id, actor int64) (taskDraftCollection, error) {
	c := s.snapshot()
	if c.ID != id || c.Scope.InitiatorID != actor {
		return taskDraftCollection{}, status.Error(codes.NotFound, "not found")
	}
	return c, nil
}
func (s *collectionConfirmationMemoryStore) freezeDraft(context.Context, int64, int64, string, string, int64, *int64, *int64, string) (taskDraftRun, error) {
	return taskDraftRun{}, errors.New("collection used single freeze")
}
func (s *collectionConfirmationMemoryStore) completeDraft(context.Context, int64, int64, string, int64) (taskDraftRun, error) {
	return taskDraftRun{}, errors.New("collection used single completion")
}
func (s *collectionConfirmationMemoryStore) freezeDraftCollectionItem(_ context.Context, authorized taskDraftCollection, index int32, review draftCollectionConfirmationReview) (taskDraftCollection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.freezes++
	if s.freezeErr != nil {
		return taskDraftCollection{}, s.freezeErr
	}
	if err := review.require(s.collection.Items[index]); err != nil {
		return taskDraftCollection{}, err
	}
	if !sameCollectionItemContent(authorized.Items[index], s.collection.Items[index]) {
		return taskDraftCollection{}, status.Error(codes.Aborted, "changed")
	}
	if s.collection.Items[index].Status == draftWaitingConfirmation {
		s.collection = freezeCollectionFixture(s.collection, index)
	}
	return copyConfirmationCollection(s.collection), nil
}
func (s *collectionConfirmationMemoryStore) completeDraftCollectionItem(_ context.Context, frozen taskDraftCollection, index int32, id int64) (taskDraftCollection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completions++
	if s.completeErr != nil {
		return taskDraftCollection{}, s.completeErr
	}
	if !sameCollectionItemContent(frozen.Items[index], s.collection.Items[index]) {
		return taskDraftCollection{}, status.Error(codes.Aborted, "changed")
	}
	s.collection.Items[index].Status, s.collection.Items[index].TaskID = draftSucceeded, id
	return copyConfirmationCollection(s.collection), nil
}

func collectionConfirmRequest(c taskDraftCollection, index int32) *pb.ConfirmTaskDraftItemRequest {
	r := collectionReview(c, index)
	return &pb.ConfirmTaskDraftItemRequest{RunId: c.ID, ItemIndex: &index, ExpectedRevision: r.Revision, ExpectedTitle: r.Title, ExpectedDescription: r.Description, ExpectedAssigneeId: &r.AssigneeID, ExpectedDueAtUnixMs: &r.DueAtUnixMs, ExpectedDeadlineResolution: r.DeadlineResolution}
}

func collectionConfirmationReader(t *testing.T, store *collectionConfirmationMemoryStore, check draftAccessFunc) *draftAccessReader {
	t.Helper()
	if check == nil {
		check = func(context.Context, *impb.CheckTeamGroupAccessRequest) error { return nil }
	}
	r := testDraftAccessReader(t, 400, nil, check)
	r.store = store
	return r
}

func TestConfirmCollectionRPCUncertainResultUsesFrozenKeyAfterRestartAndSuccessReplay(t *testing.T) {
	store := &collectionConfirmationMemoryStore{collection: confirmationCollectionFixture()}
	original := store.snapshot()
	groups, members, tasks := 0, 0, 0
	reader := collectionConfirmationReader(t, store, func(context.Context, *impb.CheckTeamGroupAccessRequest) error { groups++; return nil })
	reader.members = draftMemberFunc(func(_ context.Context, req *userpb.CheckTeamMemberByIDRequest) (*userpb.CheckTeamMemberByIDResponse, error) {
		members++
		if members > 1 {
			t.Error("frozen replay rechecked target member")
		}
		if req.GetTeamId() != 200 || req.GetUserId() != 501 {
			t.Errorf("member=%v", req)
		}
		return &userpb.CheckTeamMemberByIDResponse{}, nil
	})
	creator := draftTaskCreateFunc(func(ctx context.Context, req *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
		tasks++
		snapshot := store.snapshot()
		md, _ := metadata.FromOutgoingContext(ctx)
		deadline, ok := ctx.Deadline()
		if snapshot.Items[0].Status != draftCreating || snapshot.Items[0].TaskRequestKey != "agent-task-9001-0" || snapshot.Items[0].Draft != original.Items[0].Draft || snapshot.Items[1] != original.Items[1] || md.Get("authorization")[0] != "Bearer user-token" || md.Get("idempotency-key")[0] != "agent-task-9001-0" || !ok || time.Until(deadline) > draftTaskTimeout {
			t.Errorf("not frozen/authorized=%+v,%v", snapshot, md)
		}
		if req.GetTeamId() != 200 || req.GetTitle() != original.Items[0].Draft.Title || req.GetDescription() != original.Items[0].Draft.Description || req.GetAssigneeId() != 501 || req.GetDueAtUnixMs() != 1000 || req.GetSourceGroupId() != 300 || req.GetSourceMessageId() != 9007199254740993 {
			t.Errorf("changed creation=%v", req)
		}
		if tasks == 1 {
			return nil, status.Error(codes.DeadlineExceeded, "response lost")
		}
		return &taskpb.CreateTaskResponse{TaskId: 9007199254740997}, nil
	})
	confirmer := &draftConfirmer{store: store, tasks: creator}
	req := collectionConfirmRequest(original, 0)
	for attempt := 0; attempt < 3; attempt++ {
		client := testTaskDraftClient(t, reader, confirmer)
		ctx, cancel := draftRPCContext()
		response, err := client.ConfirmTaskDraftItem(ctx, req)
		cancel()
		if attempt == 0 {
			if response != nil || status.Code(err) != codes.DeadlineExceeded || store.snapshot().Items[0].Status != draftCreating {
				t.Fatalf("uncertain=%v,%v", response, err)
			}
			continue
		}
		if err != nil || response.GetItem().GetStatus() != "succeeded" || response.GetItem().GetTaskId() != 9007199254740997 || response.GetItem().GetDraft().GetRevision() != 1 || response.GetItem().GetReplyStatus() != "disabled" || response.GetItem().GetReplyMsgId() != "" || response.GetItem().GetItemIndex() != 0 {
			t.Fatalf("replay=%v,%v", response, err)
		}
	}
	if tasks != 2 || members != 1 || groups != 3 || store.freezes != 3 || store.completions != 1 {
		t.Fatalf("calls task=%d/member=%d/group=%d/freeze=%d/complete=%d", tasks, members, groups, store.freezes, store.completions)
	}
}

func TestConfirmCollectionRPCFailureNeverReportsOrUnfreezesSuccess(t *testing.T) {
	for _, name := range []string{"freeze failed", "empty task response", "task rejected", "result save failed"} {
		t.Run(name, func(t *testing.T) {
			store := &collectionConfirmationMemoryStore{collection: confirmationCollectionFixture()}
			calls := 0
			if name == "freeze failed" {
				store.freezeErr = status.Error(codes.Aborted, "changed")
			}
			if name == "result save failed" {
				store.completeErr = status.Error(codes.Unavailable, "DB unavailable")
			}
			creator := draftTaskCreateFunc(func(context.Context, *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
				calls++
				if name == "freeze failed" {
					t.Fatal("freeze failure called Task")
				}
				if name == "empty task response" {
					return nil, nil
				}
				if name == "task rejected" {
					return nil, status.Error(codes.AlreadyExists, "private conflict")
				}
				return &taskpb.CreateTaskResponse{TaskId: 7001}, nil
			})
			reader := collectionConfirmationReader(t, store, nil)
			confirmer := &draftConfirmer{store: store, tasks: creator}
			client := testTaskDraftClient(t, reader, confirmer)
			ctx, cancel := draftRPCContext()
			defer cancel()
			req := collectionConfirmRequest(store.snapshot(), 0)
			response, err := client.ConfirmTaskDraftItem(ctx, req)
			want := codes.Unavailable
			if name == "freeze failed" {
				want = codes.Aborted
			}
			if name == "task rejected" {
				want = codes.AlreadyExists
			}
			if response != nil || status.Code(err) != want || strings.Contains(err.Error(), "private conflict") {
				t.Fatalf("failure=%v,%v", response, err)
			}
			snapshot := store.snapshot()
			wantState := draftCreating
			if name == "freeze failed" {
				wantState = draftWaitingConfirmation
			}
			if snapshot.Items[0].Status != wantState || snapshot.Items[0].TaskID != 0 {
				t.Fatalf("false success/unfreeze=%+v", snapshot)
			}
			if name == "result save failed" {
				store.completeErr = nil
				response, err = client.ConfirmTaskDraftItem(ctx, req)
				if err != nil || response.GetItem().GetTaskId() != 7001 || calls != 2 {
					t.Fatalf("save retry=%v,%v", response, err)
				}
			}
		})
	}
}

func TestConfirmCollectionRPCReviewAndCurrentPermissionsRejectBeforeFreeze(t *testing.T) {
	for _, name := range []string{"unresolved assignee", "needs input", "stale revision", "wrong reviewed assignee", "wrong reviewed due", "wrong deadline state", "group revoked", "member revoked"} {
		t.Run(name, func(t *testing.T) {
			store := &collectionConfirmationMemoryStore{collection: confirmationCollectionFixture()}
			req := collectionConfirmRequest(store.snapshot(), 0)
			want := codes.Aborted
			switch name {
			case "unresolved assignee":
				store.collection.Items[0].Draft.AssigneeResolution = assigneeAmbiguous
				store.collection.Items[0].Draft.AssigneeID = 0
				req = collectionConfirmRequest(store.snapshot(), 0)
				want = codes.FailedPrecondition
			case "needs input":
				store.collection.Items[0].Draft = collectionEditFixture().Items[1].Draft
				store.collection.Items[0].Draft.AssigneeResolution = assigneeUnassigned
				req = collectionConfirmRequest(store.snapshot(), 0)
				want = codes.FailedPrecondition
			case "stale revision":
				req.ExpectedRevision++
			case "wrong reviewed assignee":
				req.ExpectedAssigneeId = draftID(502)
			case "wrong reviewed due":
				req.ExpectedDueAtUnixMs = draftID(2000)
			case "wrong deadline state":
				req.ExpectedDeadlineResolution = "selected"
			case "group revoked", "member revoked":
				want = codes.PermissionDenied
			}
			reader := collectionConfirmationReader(t, store, func(context.Context, *impb.CheckTeamGroupAccessRequest) error {
				if name == "group revoked" {
					return status.Error(codes.PermissionDenied, "left group")
				}
				return nil
			})
			reader.members = draftMemberFunc(func(context.Context, *userpb.CheckTeamMemberByIDRequest) (*userpb.CheckTeamMemberByIDResponse, error) {
				if name == "member revoked" {
					return nil, status.Error(codes.PermissionDenied, "disabled member")
				}
				t.Error("review rejection reached member lookup")
				return &userpb.CheckTeamMemberByIDResponse{}, nil
			})
			confirmer := &draftConfirmer{store: store, tasks: draftTaskCreateFunc(func(context.Context, *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
				t.Error("rejection reached Task")
				return nil, nil
			})}
			ctx, cancel := draftRPCContext()
			defer cancel()
			response, err := testTaskDraftClient(t, reader, confirmer).ConfirmTaskDraftItem(ctx, req)
			if response != nil || status.Code(err) != want || store.freezes != 0 {
				t.Fatalf("rejection=%v,%v freezes=%d", response, err, store.freezes)
			}
		})
	}
}

func TestConfirmCollectionRPCRequiresExplicitReviewAndItemIdentity(t *testing.T) {
	for _, mutate := range []func(*pb.ConfirmTaskDraftItemRequest){
		func(r *pb.ConfirmTaskDraftItemRequest) { r.ItemIndex = nil }, func(r *pb.ConfirmTaskDraftItemRequest) { index := int32(5); r.ItemIndex = &index }, func(r *pb.ConfirmTaskDraftItemRequest) { r.ExpectedRevision = 0 },
		func(r *pb.ConfirmTaskDraftItemRequest) { r.ExpectedAssigneeId = nil }, func(r *pb.ConfirmTaskDraftItemRequest) { r.ExpectedDueAtUnixMs = nil }, func(r *pb.ConfirmTaskDraftItemRequest) { r.ExpectedDeadlineResolution = "" },
		func(r *pb.ConfirmTaskDraftItemRequest) { r.ExpectedDueAtUnixMs = draftID(maxDraftDueAtUnixMs + 1) }, func(r *pb.ConfirmTaskDraftItemRequest) { r.ExpectedTitle = " padded " },
	} {
		req := collectionConfirmRequest(confirmationCollectionFixture(), 0)
		mutate(req)
		ctx, cancel := draftRPCContext()
		response, err := testTaskDraftClient(t, nil).ConfirmTaskDraftItem(ctx, req)
		cancel()
		if response != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("bad request=%v,%v", response, err)
		}
	}
}

func TestConfirmCollectionWithoutTaskSourceDoesNotUseRunReply(t *testing.T) {
	collection := confirmationCollectionFixture()
	collection.Items[1].Draft.SourceMessageID = 0
	store := &collectionConfirmationMemoryStore{collection: collection}
	reader := collectionConfirmationReader(t, store, nil)
	reader.members = draftMemberFunc(func(context.Context, *userpb.CheckTeamMemberByIDRequest) (*userpb.CheckTeamMemberByIDResponse, error) {
		t.Fatal("unassigned item checked target member")
		return nil, nil
	})
	server := NewServer(nil)
	server.draftReader = reader
	server.confirmer = &draftConfirmer{store: store, tasks: draftTaskCreateFunc(func(ctx context.Context, req *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetSourceGroupId() != 0 || req.GetSourceMessageId() != 0 || md.Get("idempotency-key")[0] != "agent-task-9001-1" {
			t.Errorf("source/key=%v,%v", req, md)
		}
		return &taskpb.CreateTaskResponse{TaskId: 7002}, nil
	})}
	// These dependencies are deliberately absent: collection confirmation must
	// only display not_started, never invoke the old run reply store or bot.
	server.replier = &draftReplier{}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer user-token"))
	response, err := server.ConfirmTaskDraftItem(ctx, collectionConfirmRequest(collection, 1))
	if err != nil || response.GetItem().GetTaskId() != 7002 || response.GetItem().GetReplyStatus() != "not_started" || response.GetItem().GetReplyMsgId() != "" {
		t.Fatalf("collection reply=%v,%v", response, err)
	}
}
