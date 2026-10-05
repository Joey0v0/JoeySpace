package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type taskTriggerStatusLoadFunc func(context.Context, int64) (taskTriggerStatus, error)

func (f taskTriggerStatusLoadFunc) loadTaskTriggerStatus(ctx context.Context, messageID int64) (taskTriggerStatus, error) {
	return f(ctx, messageID)
}

type statusRPCFixture struct {
	server         *Server
	source         *impb.ReadTaskTriggerContextResponse
	state          taskTriggerStatus
	actor          int64
	events         []string
	readHook       func(context.Context, *impb.ReadTaskTriggerContextResponse) (*impb.ReadTaskTriggerContextResponse, error)
	statusHook     func(context.Context) (taskTriggerStatus, error)
	collectionHook func(taskDraftCollection) (taskDraftCollection, error)
	groupHook      func(context.Context) error
	identityHook   func(context.Context) (*userpb.GetUserInfoResponse, error)
}

func newStatusRPCFixture(t *testing.T) *statusRPCFixture {
	t.Helper()
	f := &statusRPCFixture{source: validTriggerClientResponse(triggerClientSourceID), state: taskTriggerStatus{MessageID: triggerClientSourceID, Status: TriggerInboxQueued}}
	f.actor = f.source.ActorId
	f.server = NewServer(nil)
	f.server.draftReader = &draftAccessReader{
		identity: &draftIdentityResolver{users: draftUserFunc(func(ctx context.Context) (*userpb.GetUserInfoResponse, error) {
			f.events = append(f.events, "identity")
			if md, _ := metadata.FromOutgoingContext(ctx); !reflect.DeepEqual(md.Get("authorization"), []string{"Bearer user-token"}) {
				t.Fatal("original identity token was changed")
			}
			if f.identityHook != nil {
				return f.identityHook(ctx)
			}
			return &userpb.GetUserInfoResponse{Id: f.actor}, nil
		})},
		store: collectionLoadFunc(func(_ context.Context, runID, actorID int64) (taskDraftCollection, error) {
			f.events = append(f.events, "collection")
			if runID != f.state.RunID || actorID != f.actor {
				t.Fatal("collection identity changed")
			}
			scope := draftRunScope{TeamID: f.source.TeamId, GroupID: f.source.GroupId, InitiatorID: f.source.ActorId}
			collection := taskDraftCollection{ID: runID, Scope: scope, Items: []taskDraftRun{{ID: runID, Scope: scope, Revision: 1, Status: draftWaitingConfirmation, Draft: collectionStoreDraft()}}}
			if f.collectionHook != nil {
				return f.collectionHook(collection)
			}
			return collection, nil
		}),
		im: draftAccessFunc(func(ctx context.Context, _ *impb.CheckTeamGroupAccessRequest) error {
			f.events = append(f.events, "group")
			if f.groupHook != nil {
				return f.groupHook(ctx)
			}
			return nil
		}),
	}
	f.server.triggerStatus = &taskTriggerStatusReader{
		source: &TriggerContextClient{rpc: triggerContextRPCFunc(func(ctx context.Context, req *impb.ReadTaskTriggerContextRequest) (*impb.ReadTaskTriggerContextResponse, error) {
			f.events = append(f.events, "source")
			if md, ok := metadata.FromOutgoingContext(ctx); !ok || len(md) != 0 || req.MessageId != f.state.MessageID {
				t.Fatalf("identity escaped to internal source RPC: %v %v", req, md)
			}
			response := proto.Clone(f.source).(*impb.ReadTaskTriggerContextResponse)
			if f.readHook != nil {
				return f.readHook(ctx, response)
			}
			return response, nil
		})},
		store: taskTriggerStatusLoadFunc(func(ctx context.Context, messageID int64) (taskTriggerStatus, error) {
			f.events = append(f.events, "status")
			if messageID != f.state.MessageID {
				t.Fatal("inbox query identity changed")
			}
			if f.statusHook != nil {
				return f.statusHook(ctx)
			}
			return f.state, nil
		}),
	}
	return f
}

func statusRPCContext() context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer user-token", "actor-id", "1", "team-id", "2", "group-id", "3"))
}

func TestTaskTriggerStatusReturnsOnlyFourAuthorizedStatesAndCompletedResult(t *testing.T) {
	for _, state := range []string{TriggerInboxQueued, TriggerInboxRunning, TriggerInboxExhausted, TriggerInboxCompleted} {
		t.Run(state, func(t *testing.T) {
			f := newStatusRPCFixture(t)
			f.state.Status = state
			if state == TriggerInboxCompleted {
				f.state.RunID = 9007199254741099
			}
			response, err := f.server.GetTaskTriggerStatus(statusRPCContext(), &pb.GetTaskTriggerStatusRequest{MessageId: f.state.MessageID})
			if err != nil || response.GetMessageId() != f.state.MessageID || response.GetTeamId() != f.source.TeamId || response.GetGroupId() != f.source.GroupId || response.GetStatus() != state || response.GetRunId() != f.state.RunID {
				t.Fatalf("response=%v err=%v", response, err)
			}
			want := []string{"identity", "source", "status"}
			if state == TriggerInboxCompleted {
				want = append(want, "identity", "collection", "group")
			}
			if !reflect.DeepEqual(f.events, want) || response.ProtoReflect().Descriptor().Fields().Len() != 5 {
				t.Fatalf("status leaked internal fields or bypassed authorization: %v %v", response, f.events)
			}
		})
	}
}

func TestTaskTriggerStatusRejectsOtherActorsAndMissingOrRevokedSourcesBeforeInbox(t *testing.T) {
	for _, name := range []string{"other actor", "missing source", "left group", "left team", "invalid source", "identity denied"} {
		t.Run(name, func(t *testing.T) {
			f := newStatusRPCFixture(t)
			want := codes.PermissionDenied
			switch name {
			case "other actor":
				f.actor++
				want = codes.NotFound
			case "missing source":
				want = codes.NotFound
				f.readHook = func(context.Context, *impb.ReadTaskTriggerContextResponse) (*impb.ReadTaskTriggerContextResponse, error) {
					return nil, status.Error(codes.NotFound, "private missing")
				}
			case "invalid source":
				want = codes.Unavailable
				f.source.ActorId = 0
			case "identity denied":
				f.identityHook = func(context.Context) (*userpb.GetUserInfoResponse, error) {
					return nil, status.Error(codes.PermissionDenied, "private identity")
				}
			default:
				f.readHook = func(context.Context, *impb.ReadTaskTriggerContextResponse) (*impb.ReadTaskTriggerContextResponse, error) {
					return nil, status.Error(codes.PermissionDenied, "private membership")
				}
			}
			response, err := f.server.GetTaskTriggerStatus(statusRPCContext(), &pb.GetTaskTriggerStatusRequest{MessageId: f.state.MessageID})
			if response != nil || status.Code(err) != want || strings.Contains(err.Error(), "private") {
				t.Fatalf("unauthorized result=%v %v", response, err)
			}
			for _, event := range f.events {
				if event == "status" || event == "collection" {
					t.Fatalf("unauthorized request queried result: %v", f.events)
				}
			}
		})
	}
}

func TestTaskTriggerStatusMissingInboxAndDamagedSuccessNeverExposeRun(t *testing.T) {
	for _, name := range []string{"missing", "SQL", "wrong message", "unknown state", "queued result", "completed zero"} {
		t.Run(name, func(t *testing.T) {
			f := newStatusRPCFixture(t)
			want := codes.Unavailable
			f.statusHook = func(context.Context) (taskTriggerStatus, error) {
				r := f.state
				switch name {
				case "missing":
					return taskTriggerStatus{}, status.Error(codes.NotFound, "private missing inbox")
				case "SQL":
					return taskTriggerStatus{}, errors.New("private SQL")
				case "wrong message":
					r.MessageID++
				case "unknown state":
					r.Status = "failed"
				case "queued result":
					r.RunID = 42
				case "completed zero":
					r.Status = TriggerInboxCompleted
				}
				return r, nil
			}
			if name == "missing" {
				want = codes.NotFound
			}
			response, err := f.server.GetTaskTriggerStatus(statusRPCContext(), &pb.GetTaskTriggerStatusRequest{MessageId: f.state.MessageID})
			if response != nil || status.Code(err) != want || strings.Contains(err.Error(), "private") {
				t.Fatalf("bad inbox response=%v %v", response, err)
			}
		})
	}
}

func TestTaskTriggerStatusCompletedRechecksCollectionScopeAndCurrentAccess(t *testing.T) {
	for _, name := range []string{"wrong run", "wrong actor", "wrong team", "wrong group", "missing run", "single mode", "group revoked"} {
		t.Run(name, func(t *testing.T) {
			f := newStatusRPCFixture(t)
			f.state.Status, f.state.RunID = TriggerInboxCompleted, 9007199254741099
			want := codes.Unavailable
			f.collectionHook = func(r taskDraftCollection) (taskDraftCollection, error) {
				switch name {
				case "wrong run":
					r.ID++
				case "wrong actor":
					r.Scope.InitiatorID++
				case "wrong team":
					r.Scope.TeamID++
				case "wrong group":
					r.Scope.GroupID++
				case "missing run":
					return taskDraftCollection{}, status.Error(codes.NotFound, "private run")
				case "single mode":
					return taskDraftCollection{}, status.Error(codes.FailedPrecondition, "private single")
				}
				return r, nil
			}
			if name == "wrong run" || name == "wrong actor" || name == "missing run" {
				want = codes.NotFound
			}
			if name == "group revoked" {
				want = codes.PermissionDenied
				f.groupHook = func(context.Context) error { return status.Error(codes.PermissionDenied, "private group") }
			}
			response, err := f.server.GetTaskTriggerStatus(statusRPCContext(), &pb.GetTaskTriggerStatusRequest{MessageId: f.state.MessageID})
			if response != nil || status.Code(err) != want || strings.Contains(err.Error(), "private") {
				t.Fatalf("invalid completed association=%v %v", response, err)
			}
		})
	}
}

func TestTaskTriggerStatusAuthenticationConfigurationAndCancellation(t *testing.T) {
	f := newStatusRPCFixture(t)
	request := &pb.GetTaskTriggerStatusRequest{MessageId: f.state.MessageID}
	for _, ctx := range []context.Context{context.Background(), metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer fake token")), metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer one", "authorization", "Bearer two"))} {
		if response, err := f.server.GetTaskTriggerStatus(ctx, request); response != nil || status.Code(err) != codes.Unauthenticated {
			t.Fatalf("invalid bearer=%v %v", response, err)
		}
	}
	for _, req := range []*pb.GetTaskTriggerStatusRequest{nil, {}, {MessageId: -1}} {
		if response, err := f.server.GetTaskTriggerStatus(statusRPCContext(), req); response != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid request=%v %v", response, err)
		}
	}
	if response, err := f.server.GetTaskTriggerStatus(nil, request); response != nil || status.Code(err) != codes.InvalidArgument {
		t.Fatalf("nil context=%v %v", response, err)
	}
	if len(f.events) != 0 {
		t.Fatalf("invalid inputs contacted dependencies: %v", f.events)
	}
	draftStore, _ := testDraftStore(t)
	client := f.server.triggerStatus.source.(*TriggerContextClient)
	f.server.ConfigureTaskTriggerStatus(client, NewTriggerInboxStore(draftStore.db))
	if f.server.triggerStatus == nil || f.server.triggerStatus.source != client {
		t.Fatal("configured source was not reused")
	}
	f.server.ConfigureTaskTriggerStatus(nil, NewTriggerInboxStore(draftStore.db))
	if response, err := f.server.GetTaskTriggerStatus(statusRPCContext(), request); response != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("default off=%v %v", response, err)
	}
	f = newStatusRPCFixture(t)
	ctx, cancel := context.WithCancel(statusRPCContext())
	cancel()
	if response, err := f.server.GetTaskTriggerStatus(ctx, request); response != nil || status.Code(err) != codes.Canceled || len(f.events) != 0 {
		t.Fatalf("canceled=%v %v", response, err)
	}
	ctx, cancel = context.WithCancel(statusRPCContext())
	defer cancel()
	f.statusHook = func(context.Context) (taskTriggerStatus, error) { cancel(); return f.state, nil }
	if response, err := f.server.GetTaskTriggerStatus(ctx, request); response != nil || status.Code(err) != codes.Canceled {
		t.Fatalf("late cancellation=%v %v", response, err)
	}
}
