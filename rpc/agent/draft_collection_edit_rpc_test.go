package agent

import (
	"context"
	"database/sql/driver"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestDraftCollectionItemEditsOverRPCPreserveEvidenceAndScope(t *testing.T) {
	for _, name := range []string{"text", "select member", "unassigned", "set deadline", "unset deadline"} {
		t.Run(name, func(t *testing.T) {
			collection := collectionEditFixture()
			store, mock := testDraftStore(t)
			reader := testDraftAccessReader(t, 400, nil, func(ctx context.Context, req *impb.CheckTeamGroupAccessRequest) error {
				md, _ := metadata.FromOutgoingContext(ctx)
				if req.GetTeamId() != 200 || req.GetGroupId() != 300 || md.Get("authorization")[0] != "Bearer user-token" {
					t.Fatalf("group check=%v,%v", req, md)
				}
				return nil
			})
			reader.store = store
			members := 0
			reader.members = draftMemberFunc(func(ctx context.Context, req *userpb.CheckTeamMemberByIDRequest) (*userpb.CheckTeamMemberByIDResponse, error) {
				members++
				md, _ := metadata.FromOutgoingContext(ctx)
				if req.GetTeamId() != 200 || req.GetUserId() != 502 || md.Get("authorization")[0] != "Bearer user-token" {
					t.Fatalf("member check=%v,%v", req, md)
				}
				return &userpb.CheckTeamMemberByIDResponse{}, nil
			})
			candidate := collection.Items[1].Draft
			var query string
			var args []driver.Value
			switch name {
			case "text":
				candidate.Title, candidate.Description = "新标题", "新说明"
				query = updateDraftCollectionText
				args = []driver.Value{"新标题", "新说明"}
			case "select member":
				candidate.AssigneeID, candidate.AssigneeResolution = 502, assigneeSelected
				query = updateDraftCollectionAssignee
				args = []driver.Value{int64(502), "selected"}
			case "unassigned":
				candidate.AssigneeResolution = assigneeUnassigned
				query = updateDraftCollectionAssignee
				args = []driver.Value{int64(0), "unassigned"}
			case "set deadline":
				candidate.DueAtUnixMs, candidate.Deadline.Resolution = 5000, "selected"
				query = updateDraftCollectionDeadline
				args = []driver.Value{int64(5000), "selected"}
			case "unset deadline":
				candidate.Deadline.Resolution = "unset"
				query = updateDraftCollectionDeadline
				args = []driver.Value{int64(0), "unset"}
			}
			mock.ExpectQuery(regexp.QuoteMeta(selectDraftCollectionForInitiator)).WithArgs(int64(9001), int64(400)).WillReturnRows(collectionEditRows(collection))
			expectCollectionLock(mock, collection)
			args = append(args, int64(9001), int64(1), int64(1))
			mock.ExpectExec(regexp.QuoteMeta(query)).WithArgs(args...).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			client := testTaskDraftClient(t, reader)
			ctx, cancel := draftRPCContext()
			defer cancel()
			index := int32(1)
			var response *pb.GetTaskDraftItemResponse
			var err error
			switch name {
			case "text":
				response, err = client.EditTaskDraftItemText(ctx, &pb.EditTaskDraftItemTextRequest{RunId: 9001, ItemIndex: &index, ExpectedRevision: 1, Title: " 新标题 ", Description: " 新说明 "})
			case "select member", "unassigned":
				id := candidate.AssigneeID
				response, err = client.SelectTaskDraftItemAssignee(ctx, &pb.SelectTaskDraftItemAssigneeRequest{RunId: 9001, ItemIndex: &index, ExpectedRevision: 1, AssigneeId: &id})
			default:
				due := candidate.DueAtUnixMs
				response, err = client.EditTaskDraftItemDeadline(ctx, &pb.EditTaskDraftItemDeadlineRequest{RunId: 9001, ItemIndex: &index, ExpectedRevision: 1, DueAtUnixMs: &due})
			}
			if err != nil || response.GetRunId() != 9001 || response.GetTeamId() != 200 || response.GetGroupId() != 300 || response.GetItemCount() != 2 || response.GetItem().ItemIndex == nil || response.GetItem().GetItemIndex() != 1 || response.Item.Draft.Revision != 2 || response.Item.Status != "waiting_confirmation" || response.Item.TaskId != 0 || response.Item.ReplyStatus != "disabled" {
				t.Fatalf("edited=%v,%v", response, err)
			}
			draft := response.Item.Draft
			if !proto.Equal(draft.Deadline, candidate.Deadline.rpc()) {
				t.Fatalf("complete deadline evidence changed=%v", draft.Deadline)
			}
			if draft.Title != candidate.Title || draft.Description != candidate.Description || draft.AssigneeId != candidate.AssigneeID || draft.AssigneeName != candidate.AssigneeName || draft.AssigneeResolution != string(candidate.AssigneeResolution) || draft.SourceMessageId != candidate.SourceMessageID || draft.DueAtUnixMs != candidate.DueAtUnixMs || draft.Deadline.Text != candidate.Deadline.Text || draft.Deadline.Source != candidate.Deadline.Source || draft.Deadline.ReferenceUnixMs != candidate.Deadline.ReferenceUnixMs || draft.Deadline.InstructionReferenceUnixMs != candidate.Deadline.InstructionReferenceUnixMs || draft.Deadline.Resolution != candidate.Deadline.Resolution || draft.Deadline.Reason != candidate.Deadline.Reason || draft.Deadline.ParsedUnixMs != candidate.Deadline.ParsedUnixMs {
				t.Fatalf("evidence changed=%v", draft)
			}
			wantMembers := 0
			if name == "select member" {
				wantMembers = 1
			}
			if members != wantMembers {
				t.Fatalf("member checks=%d", members)
			}
		})
	}
}

func TestDraftCollectionItemEditDenialAndStaleRevisionStopBeforeLock(t *testing.T) {
	for _, name := range []string{"group revoked", "member revoked", "nil member response", "stale revision"} {
		t.Run(name, func(t *testing.T) {
			collection := collectionEditFixture()
			store, mock := testDraftStore(t)
			mock.ExpectQuery(regexp.QuoteMeta(selectDraftCollectionForInitiator)).WithArgs(int64(9001), int64(400)).WillReturnRows(collectionEditRows(collection))
			reader := testDraftAccessReader(t, 400, nil, func(context.Context, *impb.CheckTeamGroupAccessRequest) error {
				if name == "group revoked" {
					return status.Error(codes.PermissionDenied, "left group")
				}
				return nil
			})
			reader.store = store
			reader.members = draftMemberFunc(func(context.Context, *userpb.CheckTeamMemberByIDRequest) (*userpb.CheckTeamMemberByIDResponse, error) {
				if name == "stale revision" || name == "group revoked" {
					t.Fatal("denied/stale edit checked member")
				}
				if name == "nil member response" {
					return nil, nil
				}
				return nil, status.Error(codes.PermissionDenied, "member disabled")
			})
			client := testTaskDraftClient(t, reader)
			ctx, cancel := draftRPCContext()
			defer cancel()
			index, id, revision := int32(1), int64(502), int64(1)
			want := codes.PermissionDenied
			if name == "stale revision" {
				revision = 2
				want = codes.Aborted
			}
			if name == "nil member response" {
				want = codes.Unavailable
			}
			response, err := client.SelectTaskDraftItemAssignee(ctx, &pb.SelectTaskDraftItemAssigneeRequest{RunId: 9001, ItemIndex: &index, ExpectedRevision: revision, AssigneeId: &id})
			if response != nil || status.Code(err) != want {
				t.Fatalf("denied=%v,%v", response, err)
			}
		})
	}
}

func TestDraftCollectionItemEditingRejectsInvalidRPCIdentityAndValues(t *testing.T) {
	client := testTaskDraftClient(t, nil)
	ctx, cancel := draftRPCContext()
	defer cancel()
	zeroIndex := int32(0)
	negative := int32(-1)
	large := int32(5)
	for _, tc := range []struct {
		run, revision int64
		index         *int32
	}{{9001, 1, nil}, {9001, 1, &negative}, {9001, 1, &large}, {0, 1, &zeroIndex}, {9001, 0, &zeroIndex}} {
		if response, err := client.EditTaskDraftItemText(ctx, &pb.EditTaskDraftItemTextRequest{RunId: tc.run, ExpectedRevision: tc.revision, ItemIndex: tc.index, Title: "任务"}); response != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("identity=%v,%v", response, err)
		}
	}
	for _, id := range []*int64{nil, ptrCollectionEditValue(-1)} {
		if response, err := client.SelectTaskDraftItemAssignee(ctx, &pb.SelectTaskDraftItemAssigneeRequest{RunId: 9001, ItemIndex: &zeroIndex, ExpectedRevision: 1, AssigneeId: id}); response != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("assignee=%v,%v", response, err)
		}
	}
	for _, due := range []*int64{nil, ptrCollectionEditValue(-1), ptrCollectionEditValue(maxDraftDueAtUnixMs + 1)} {
		if response, err := client.EditTaskDraftItemDeadline(ctx, &pb.EditTaskDraftItemDeadlineRequest{RunId: 9001, ItemIndex: &zeroIndex, ExpectedRevision: 1, DueAtUnixMs: due}); response != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("deadline=%v,%v", response, err)
		}
	}
}

func ptrCollectionEditValue(value int64) *int64 { return &value }
