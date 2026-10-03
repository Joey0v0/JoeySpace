package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type collectionGeneratorFunc func(context.Context, string, []*impb.TeamGroupMessage) ([]taskDraft, error)

func (f collectionGeneratorFunc) GenerateDrafts(ctx context.Context, instruction string, messages []*impb.TeamGroupMessage) ([]taskDraft, error) {
	return f(ctx, instruction, messages)
}
func (f collectionGeneratorFunc) GenerateDraft(context.Context, string, []*impb.TeamGroupMessage) (taskDraft, error) {
	return taskDraft{}, status.Error(codes.Internal, "collection used single generator")
}

type collectionPreparationStoreFuncs struct {
	draftPreparationStoreFuncs
	saveCollection func(context.Context, int64, draftRunScope, []taskDraft, string, string) (int64, error)
}

func (s collectionPreparationStoreFuncs) saveWaitingDraftCollection(ctx context.Context, id int64, scope draftRunScope, drafts []taskDraft, key, fingerprint string) (int64, error) {
	return s.saveCollection(ctx, id, scope, drafts, key, fingerprint)
}

func TestDraftCollectionFingerprintSeparatesModesAndPreservesExactContract(t *testing.T) {
	ref := int64(1234)
	for _, tc := range []struct {
		ref  *int64
		json string
	}{
		{nil, `{"team_id":200,"group_id":300,"instruction":"提取待办","mode":"collection"}`},
		{&ref, `{"team_id":200,"group_id":300,"instruction":"提取待办","mode":"collection","instruction_reference_unix_ms":1234}`},
	} {
		sum := sha256.Sum256([]byte(tc.json))
		actual := draftCollectionFingerprint(200, 300, " 提取待办 ", tc.ref)
		if actual != hex.EncodeToString(sum[:]) || actual == draftPreparationFingerprintWithReference(200, 300, "提取待办", tc.ref) {
			t.Fatalf("fingerprint = %s", actual)
		}
	}
}

func TestDraftCollectionPreparationNormalizesAllItemsAndKeepsReplayFirst(t *testing.T) {
	for _, replay := range []bool{false, true} {
		p := testDraftPreparer(t)
		p.generator = collectionGeneratorFunc(func(_ context.Context, instruction string, messages []*impb.TeamGroupMessage) ([]taskDraft, error) {
			if replay {
				t.Fatal("replay reached model")
			}
			if instruction != "提取待办" || len(messages) != 1 || messages[0].GetId() != 600 {
				t.Fatalf("model input=%s,%v", instruction, messages)
			}
			return []taskDraft{{Title: " 修复缓存 ", Deadline: draftDeadlineMetadata{Source: "none"}}, {Title: " 整理文档 ", Deadline: draftDeadlineMetadata{Source: "none"}}}, nil
		})
		p.store = collectionPreparationStoreFuncs{
			draftPreparationStoreFuncs: draftPreparationStoreFuncs{find: func(_ context.Context, scope draftRunScope, key, fingerprint string) (int64, error) {
				if scope != (draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}) || key != "batch-1" || fingerprint != draftCollectionFingerprint(200, 300, "提取待办", nil) {
					t.Fatalf("lookup=%+v,%s,%s", scope, key, fingerprint)
				}
				if replay {
					return 8123, nil
				}
				return 0, nil
			}},
			saveCollection: func(_ context.Context, id int64, scope draftRunScope, drafts []taskDraft, key, fingerprint string) (int64, error) {
				if replay {
					t.Fatal("replay saved again")
				}
				if id <= 0 || len(drafts) != 2 || drafts[0].Title != "修复缓存" || drafts[1].Title != "整理文档" || drafts[0].AssigneeResolution != assigneeNone || drafts[1].Deadline.Resolution != "none" {
					t.Fatalf("saved=%+v", drafts)
				}
				return id, nil
			},
		}
		ctx, cancel := preparedRPCContext("authorization", "Bearer user-token", "idempotency-key", "batch-1")
		response, err := testPrepareDraftClient(t, p).PrepareTaskDraftCollection(ctx, &pb.PrepareTaskDraftRequest{TeamId: 200, GroupId: 300, Instruction: " 提取待办 "})
		cancel()
		if err != nil || response.GetRunId() <= 0 || (replay && response.GetRunId() != 8123) {
			t.Fatalf("collection=%v,%v", response, err)
		}
	}
}

func TestDraftCollectionPreparationChecksGroupBeforeReplayAndNeverSavesInvalidSecond(t *testing.T) {
	for _, denied := range []bool{false, true} {
		p := testDraftPreparer(t)
		p.generator = collectionGeneratorFunc(func(context.Context, string, []*impb.TeamGroupMessage) ([]taskDraft, error) {
			if denied {
				t.Fatal("denied group reached model")
			}
			return []taskDraft{{Title: "任务", Deadline: draftDeadlineMetadata{Source: "none"}}, {Title: "非法来源", SourceMessageID: 999, Deadline: draftDeadlineMetadata{Source: "none"}}}, nil
		})
		p.store = collectionPreparationStoreFuncs{draftPreparationStoreFuncs: draftPreparationStoreFuncs{find: func(context.Context, draftRunScope, string, string) (int64, error) {
			if denied {
				t.Fatal("denied group reached replay")
			}
			return 0, nil
		}}, saveCollection: func(context.Context, int64, draftRunScope, []taskDraft, string, string) (int64, error) {
			t.Fatal("invalid batch was saved")
			return 0, nil
		}}
		if denied {
			p.messages = draftMessagesFunc(func(context.Context, string, int64, int64) ([]*impb.TeamGroupMessage, error) {
				return nil, status.Error(codes.PermissionDenied, "left group")
			})
		}
		id, err := p.prepareCollectionWithReference(context.Background(), "user-token", 200, 300, "提取待办", "batch-1", nil)
		want := codes.FailedPrecondition
		if denied {
			want = codes.PermissionDenied
		}
		if id != 0 || status.Code(err) != want {
			t.Fatalf("invalid preparation=%d,%v", id, err)
		}
	}
}
