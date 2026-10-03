package agent

import (
	"context"
	"testing"

	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type collectionLoadFunc func(context.Context, int64, int64) (taskDraftCollection, error)

func (f collectionLoadFunc) loadDraftCollectionForInitiator(ctx context.Context, id, actor int64) (taskDraftCollection, error) {
	return f(ctx, id, actor)
}
func (f collectionLoadFunc) loadDraftForInitiator(context.Context, int64, int64) (taskDraftRun, error) {
	return taskDraftRun{}, status.Error(codes.FailedPrecondition, "collection used single read")
}

func TestDraftCollectionRPCExplicitIndexAndCurrentGroupChecks(t *testing.T) {
	checks := 0
	denied := false
	reader := testDraftAccessReader(t, 400, nil, func(_ context.Context, req *impb.CheckTeamGroupAccessRequest) error {
		checks++
		if req.GetTeamId() != 200 || req.GetGroupId() != 300 {
			t.Fatalf("scope=%v", req)
		}
		if denied {
			return status.Error(codes.PermissionDenied, "left group")
		}
		return nil
	})
	scope := draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}
	reader.store = collectionLoadFunc(func(_ context.Context, id, actor int64) (taskDraftCollection, error) {
		if id != 9001 || actor != 400 {
			t.Fatalf("lookup=%d,%d", id, actor)
		}
		first := taskDraftRun{ID: id, Scope: scope, Revision: 1, Status: draftWaitingConfirmation, Draft: collectionStoreDraft()}
		second := first
		second.Draft.Title = "整理文档"
		return taskDraftCollection{ID: id, Scope: scope, Items: []taskDraftRun{first, second}}, nil
	})
	client := testTaskDraftClient(t, reader)
	ctx, cancel := draftRPCContext()
	defer cancel()
	response, err := client.GetTaskDraftCollection(ctx, &pb.GetTaskDraftRequest{RunId: 9001})
	if err != nil || response.GetItemCount() != 2 || len(response.Items) != 2 || response.Items[0].ItemIndex == nil || response.Items[0].GetItemIndex() != 0 || response.Items[1].GetItemIndex() != 1 || response.Items[0].ReplyStatus != "disabled" {
		t.Fatalf("collection=%v,%v", response, err)
	}
	for _, index := range []int32{0, 1} {
		response, err := client.GetTaskDraftItem(ctx, &pb.GetTaskDraftItemRequest{RunId: 9001, ItemIndex: &index})
		if err != nil || response.GetItemCount() != 2 || response.GetItem().GetItemIndex() != index || response.GetItem().Draft.Revision != 1 {
			t.Fatalf("item=%v,%v", response, err)
		}
	}
	if checks != 3 {
		t.Fatalf("current qualification checks=%d", checks)
	}
	if response, err := client.GetTaskDraftItem(ctx, &pb.GetTaskDraftItemRequest{RunId: 9001}); response != nil || status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing index=%v,%v", response, err)
	}
	index := int32(2)
	if response, err := client.GetTaskDraftItem(ctx, &pb.GetTaskDraftItemRequest{RunId: 9001, ItemIndex: &index}); response != nil || status.Code(err) != codes.NotFound {
		t.Fatalf("out-of-count=%v,%v", response, err)
	}
	denied = true
	if response, err := client.GetTaskDraftCollection(ctx, &pb.GetTaskDraftRequest{RunId: 9001}); response != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("revoked=%v,%v", response, err)
	}
}
