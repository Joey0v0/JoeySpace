package agent

import (
	"context"
	"errors"
	"testing"

	impb "github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type draftLoadFunc func(context.Context, int64, int64) (taskDraftRun, error)

func (f draftLoadFunc) loadDraftForInitiator(ctx context.Context, runID, actorID int64) (taskDraftRun, error) {
	return f(ctx, runID, actorID)
}

type draftAccessFunc func(context.Context, *impb.CheckTeamGroupAccessRequest) error

func (f draftAccessFunc) CheckTeamGroupAccess(ctx context.Context, req *impb.CheckTeamGroupAccessRequest, _ ...grpc.CallOption) (*impb.CheckTeamGroupAccessResponse, error) {
	if err := f(ctx, req); err != nil {
		return nil, err
	}
	return &impb.CheckTeamGroupAccessResponse{}, nil
}

func testDraftAccessReader(t *testing.T, actorID int64, load draftLoadFunc, check draftAccessFunc) *draftAccessReader {
	t.Helper()
	return &draftAccessReader{
		identity: &draftIdentityResolver{users: draftUserFunc(func(ctx context.Context) (*userpb.GetUserInfoResponse, error) {
			md, _ := metadata.FromOutgoingContext(ctx)
			if values := md.Get("authorization"); len(values) != 1 || values[0] != "Bearer user-token" {
				t.Fatalf("identity token = %v", values)
			}
			return &userpb.GetUserInfoResponse{Id: actorID}, nil
		})},
		members: draftMemberFunc(func(context.Context, *userpb.CheckTeamMemberByIDRequest) (*userpb.CheckTeamMemberByIDResponse, error) {
			return &userpb.CheckTeamMemberByIDResponse{}, nil
		}),
		store: load,
		im:    check,
	}
}

func TestDraftAccessReadsOnlyCurrentInitiatorInCurrentGroup(t *testing.T) {
	r := testDraftAccessReader(t, 400,
		func(_ context.Context, runID, actorID int64) (taskDraftRun, error) {
			if runID != 9001 || actorID != 400 {
				t.Fatalf("load scope = %d, %d", runID, actorID)
			}
			return taskDraftRun{Revision: 1, ID: runID, Scope: draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}, Draft: taskDraft{Title: "修复缓存"}}, nil
		},
		func(ctx context.Context, req *impb.CheckTeamGroupAccessRequest) error {
			md, _ := metadata.FromOutgoingContext(ctx)
			if req.GetTeamId() != 200 || req.GetGroupId() != 300 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" {
				t.Fatalf("IM scope = %v, token = %v", req, md.Get("authorization"))
			}
			return nil
		})
	run, err := r.load(context.Background(), "user-token", 9001)
	if err != nil || run.Draft.Title != "修复缓存" {
		t.Fatalf("draft = %+v, %v", run, err)
	}
}

func TestDraftAccessDoesNotCheckGroupForOtherUsersDraft(t *testing.T) {
	r := testDraftAccessReader(t, 401,
		func(_ context.Context, _, actorID int64) (taskDraftRun, error) {
			if actorID != 401 {
				t.Fatalf("actor = %d", actorID)
			}
			return taskDraftRun{}, status.Error(codes.NotFound, "draft run not found")
		},
		func(context.Context, *impb.CheckTeamGroupAccessRequest) error {
			t.Fatal("IM checked for an inaccessible draft")
			return nil
		})
	run, err := r.load(context.Background(), "user-token", 9001)
	if run.ID != 0 || status.Code(err) != codes.NotFound {
		t.Fatalf("other user = %+v, %v", run, err)
	}
}

func TestDraftAccessHidesDraftWhenMembershipOrScopeFails(t *testing.T) {
	for _, code := range []codes.Code{codes.PermissionDenied, codes.NotFound, codes.Unauthenticated} {
		r := testDraftAccessReader(t, 400,
			func(context.Context, int64, int64) (taskDraftRun, error) {
				return taskDraftRun{Revision: 1, ID: 9001, Scope: draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}, Draft: taskDraft{Title: "private draft"}}, nil
			},
			func(context.Context, *impb.CheckTeamGroupAccessRequest) error {
				return status.Error(code, "access denied")
			})
		run, err := r.load(context.Background(), "user-token", 9001)
		if run.ID != 0 || run.Draft.Title != "" || status.Code(err) != code {
			t.Fatalf("code %v: %+v, %v", code, run, err)
		}
	}
}

func TestDraftAccessMasksBackendFailureAndInvalidStoredScope(t *testing.T) {
	r := testDraftAccessReader(t, 400,
		func(context.Context, int64, int64) (taskDraftRun, error) {
			return taskDraftRun{Revision: 1, ID: 9001, Scope: draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}}, nil
		},
		func(context.Context, *impb.CheckTeamGroupAccessRequest) error { return errors.New("private IM detail") })
	run, err := r.load(context.Background(), "user-token", 9001)
	if run.ID != 0 || status.Code(err) != codes.Unavailable || status.Convert(err).Message() == "private IM detail" {
		t.Fatalf("IM failure = %+v, %v", run, err)
	}
	r.store = draftLoadFunc(func(context.Context, int64, int64) (taskDraftRun, error) {
		return taskDraftRun{Revision: 1, ID: 9001, Scope: draftRunScope{TeamID: 0, GroupID: 300, InitiatorID: 400}}, nil
	})
	run, err = r.load(context.Background(), "user-token", 9001)
	if run.ID != 0 || status.Code(err) != codes.NotFound {
		t.Fatalf("bad stored scope = %+v, %v", run, err)
	}
}
