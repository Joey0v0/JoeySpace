package agent

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestSkipCollectionRPCUnresolvedCandidateAndSameVersionReplayNeedNoTaskConfiguration(t *testing.T) {
	store, mock := testDraftStore(t)
	original := collectionEditFixture()
	current := original
	checks := 0
	reader := testDraftAccessReader(t, 400, nil, func(ctx context.Context, req *impb.CheckTeamGroupAccessRequest) error {
		checks++
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 200 || req.GetGroupId() != 300 || md.Get("authorization")[0] != "Bearer user-token" {
			t.Fatalf("scope/token=%v,%v", req, md)
		}
		return nil
	})
	reader.store = store
	reader.members = draftMemberFunc(func(context.Context, *userpb.CheckTeamMemberByIDRequest) (*userpb.CheckTeamMemberByIDResponse, error) {
		t.Fatal("skip queried target member")
		return nil, nil
	})
	client := testTaskDraftClient(t, reader) // Neither Task nor model is configured.
	index := int32(1)
	req := &pb.SkipTaskDraftItemRequest{RunId: 9001, ItemIndex: &index, ExpectedRevision: 1}
	for attempt := 0; attempt < 2; attempt++ {
		mock.ExpectQuery(regexp.QuoteMeta(selectDraftCollectionForInitiator)).WithArgs(int64(9001), int64(400)).WillReturnRows(collectionEditRows(current))
		expectCollectionLock(mock, current)
		if attempt == 0 {
			mock.ExpectExec(regexp.QuoteMeta(skipDraftCollectionItemSQL)).WithArgs("skipped", int64(9001), int64(1), int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
		}
		mock.ExpectCommit()
		ctx, cancel := draftRPCContext()
		response, err := client.SkipTaskDraftItem(ctx, req)
		cancel()
		if err != nil || response.GetRunId() != 9001 || response.GetTeamId() != 200 || response.GetGroupId() != 300 || response.GetItemCount() != 2 || response.GetItem().ItemIndex == nil || response.GetItem().GetItemIndex() != 1 || response.GetItem().GetStatus() != "skipped" || response.GetItem().GetTaskId() != 0 || response.GetItem().GetReplyStatus() != "disabled" || response.GetItem().GetReplyMsgId() != "" || response.GetItem().GetDraft().GetRevision() != 1 {
			t.Fatalf("skip=%v,%v", response, err)
		}
		if !proto.Equal(response.Item.Draft, taskDraftRPCResponse(original.Items[1]).Draft) {
			t.Fatalf("candidate evidence changed=%v", response.Item.Draft)
		}
		current = skippedCollectionFixture(original, 1)
	}
	if checks != 2 {
		t.Fatalf("current qualification checks=%d", checks)
	}
}

func TestSkippedCollectionReplyDisabledEvenWhenOldReplierConfigured(t *testing.T) {
	collection := skippedCollectionFixture(collectionEditFixture(), 1)
	server := NewServer(nil)
	server.replier = &draftReplier{}
	response := server.taskDraftCollectionResponse(collection)
	if response.Items[0].ReplyStatus != "not_started" || response.Items[1].ReplyStatus != "disabled" || response.Items[1].ReplyMsgId != "" {
		t.Fatalf("reply statuses=%v", response)
	}
}

func TestSkipCollectionRPCPermissionVersionAndFrozenFailuresAvoidTransaction(t *testing.T) {
	for _, name := range []string{"group revoked", "other actor", "single mode", "out of actual count", "stale skipped version", "creating", "succeeded"} {
		t.Run(name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			collection := collectionEditFixture()
			actor := int64(400)
			index := int32(1)
			revision := int64(1)
			want := codes.FailedPrecondition
			switch name {
			case "other actor":
				actor = 401
				want = codes.NotFound
			case "group revoked":
				want = codes.PermissionDenied
			case "out of actual count":
				index = 2
				want = codes.NotFound
			case "stale skipped version":
				collection = skippedCollectionFixture(collection, 1)
				revision = 2
				want = codes.Aborted
			case "creating", "succeeded":
				collection = freezeCollectionFixture(confirmationCollectionFixture(), 1)
				if name == "succeeded" {
					collection.Items[1].Status, collection.Items[1].TaskID = draftSucceeded, 7001
				}
			}
			rows := collectionEditRows(collection)
			if name == "other actor" {
				rows = sqlmock.NewRows(collectionColumns)
			}
			if name == "single mode" {
				values := collectionRowValues(0, 1, collectionStoreDraft())
				values[1] = "single"
				rows = sqlmock.NewRows(collectionColumns).AddRow(values...)
			}
			mock.ExpectQuery(regexp.QuoteMeta(selectDraftCollectionForInitiator)).WithArgs(int64(9001), actor).WillReturnRows(rows)
			reader := testDraftAccessReader(t, actor, nil, func(context.Context, *impb.CheckTeamGroupAccessRequest) error {
				if name == "group revoked" {
					return status.Error(codes.PermissionDenied, "left group")
				}
				if name == "other actor" || name == "single mode" {
					t.Fatal("inaccessible mode/actor checked group")
				}
				return nil
			})
			reader.store = store
			reader.members = draftMemberFunc(func(context.Context, *userpb.CheckTeamMemberByIDRequest) (*userpb.CheckTeamMemberByIDResponse, error) {
				t.Fatal("skip queried member")
				return nil, nil
			})
			ctx, cancel := draftRPCContext()
			defer cancel()
			response, err := testTaskDraftClient(t, reader).SkipTaskDraftItem(ctx, &pb.SkipTaskDraftItemRequest{RunId: 9001, ItemIndex: &index, ExpectedRevision: revision})
			if response != nil || status.Code(err) != want {
				t.Fatalf("skip rejection=%v,%v", response, err)
			}
		})
	}
}

func TestSkippedCollectionItemCannotEditOrConfirmAndDoesNotCheckMember(t *testing.T) {
	for _, operation := range []string{"text", "confirm"} {
		t.Run(operation, func(t *testing.T) {
			store, mock := testDraftStore(t)
			collection := skippedCollectionFixture(collectionEditFixture(), 1)
			mock.ExpectQuery(regexp.QuoteMeta(selectDraftCollectionForInitiator)).WithArgs(int64(9001), int64(400)).WillReturnRows(collectionEditRows(collection))
			reader := testDraftAccessReader(t, 400, nil, func(context.Context, *impb.CheckTeamGroupAccessRequest) error { return nil })
			reader.store = store
			reader.members = draftMemberFunc(func(context.Context, *userpb.CheckTeamMemberByIDRequest) (*userpb.CheckTeamMemberByIDResponse, error) {
				t.Fatal("skipped item checked member")
				return nil, nil
			})
			confirmer := &draftConfirmer{store: store, tasks: draftTaskCreateFunc(func(context.Context, *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
				t.Fatal("skipped item called Task")
				return nil, nil
			})}
			client := testTaskDraftClient(t, reader, confirmer)
			ctx, cancel := draftRPCContext()
			defer cancel()
			index := int32(1)
			var response *pb.GetTaskDraftItemResponse
			var err error
			if operation == "text" {
				response, err = client.EditTaskDraftItemText(ctx, &pb.EditTaskDraftItemTextRequest{RunId: 9001, ItemIndex: &index, ExpectedRevision: 1, Title: "改标题"})
			} else {
				response, err = client.ConfirmTaskDraftItem(ctx, collectionConfirmRequest(collection, index))
			}
			if response != nil || status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("skipped operation=%v,%v", response, err)
			}
		})
	}
}

func TestSkipCollectionRPCRequiresIdentityAndVersion(t *testing.T) {
	client := testTaskDraftClient(t, nil)
	for _, tc := range []struct {
		run, revision int64
		index         *int32
	}{{9001, 1, nil}, {9001, 1, collectionSkipIndex(-1)}, {9001, 1, collectionSkipIndex(5)}, {0, 1, collectionSkipIndex(0)}, {9001, 0, collectionSkipIndex(0)}} {
		ctx, cancel := draftRPCContext()
		response, err := client.SkipTaskDraftItem(ctx, &pb.SkipTaskDraftItemRequest{RunId: tc.run, ItemIndex: tc.index, ExpectedRevision: tc.revision})
		cancel()
		if response != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("identity=%v,%v", response, err)
		}
	}
}

func collectionSkipIndex(value int32) *int32 { return &value }
