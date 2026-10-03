package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bwmarrin/snowflake"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type draftMessagesFunc func(context.Context, string, int64, int64) ([]*impb.TeamGroupMessage, error)

func (f draftMessagesFunc) GroupMessages(ctx context.Context, token string, teamID, groupID int64) ([]*impb.TeamGroupMessage, error) {
	return f(ctx, token, teamID, groupID)
}

type taskDraftGeneratorFunc func(context.Context, string, []*impb.TeamGroupMessage) (taskDraft, error)

func (f taskDraftGeneratorFunc) GenerateDraft(ctx context.Context, instruction string, messages []*impb.TeamGroupMessage) (taskDraft, error) {
	return f(ctx, instruction, messages)
}

type draftPreparationStoreFuncs struct {
	find func(context.Context, draftRunScope, string, string) (int64, error)
	save func(context.Context, int64, draftRunScope, taskDraft, string, string) (int64, error)
}

func (f draftPreparationStoreFuncs) findExistingDraft(ctx context.Context, scope draftRunScope, key, fingerprint string) (int64, error) {
	return f.find(ctx, scope, key, fingerprint)
}

func (f draftPreparationStoreFuncs) saveWaitingDraft(ctx context.Context, id int64, scope draftRunScope, draft taskDraft, key, fingerprint string) (int64, error) {
	return f.save(ctx, id, scope, draft, key, fingerprint)
}

func testDraftPreparer(t *testing.T) *draftPreparer {
	t.Helper()
	node, err := snowflake.NewNode(5)
	if err != nil {
		t.Fatal(err)
	}
	return &draftPreparer{
		identity: &draftIdentityResolver{users: draftUserFunc(func(context.Context) (*userpb.GetUserInfoResponse, error) {
			return &userpb.GetUserInfoResponse{Id: 400}, nil
		})},
		messages: draftMessagesFunc(func(_ context.Context, token string, teamID, groupID int64) ([]*impb.TeamGroupMessage, error) {
			if token != "user-token" || teamID != 200 || groupID != 300 {
				t.Fatalf("message scope = %q, %d, %d", token, teamID, groupID)
			}
			return []*impb.TeamGroupMessage{{Id: 600, ContentType: 1, Content: "修复缓存"}}, nil
		}),
		generator: taskDraftGeneratorFunc(func(context.Context, string, []*impb.TeamGroupMessage) (taskDraft, error) {
			return taskDraft{Title: "修复缓存", SourceMessageID: 600}, nil
		}),
		store: draftPreparationStoreFuncs{
			find: func(context.Context, draftRunScope, string, string) (int64, error) { return 0, nil },
			save: func(_ context.Context, id int64, _ draftRunScope, _ taskDraft, _, _ string) (int64, error) {
				return id, nil
			},
		},
		idNode: node,
	}
}

func TestDraftPreparerUsesVerifiedScopeAndGeneratedRunID(t *testing.T) {
	p := testDraftPreparer(t)
	p.generator = taskDraftGeneratorFunc(func(_ context.Context, instruction string, messages []*impb.TeamGroupMessage) (taskDraft, error) {
		if instruction != "提取待办" || len(messages) != 1 || messages[0].GetId() != 600 {
			t.Fatalf("generator input = %q, %v", instruction, messages)
		}
		return taskDraft{Title: "修复缓存", SourceMessageID: 600}, nil
	})
	p.store = draftPreparationStoreFuncs{
		find: func(_ context.Context, scope draftRunScope, key, fingerprint string) (int64, error) {
			if scope != (draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}) || key != "request-1" || fingerprint != draftPreparationFingerprint(200, 300, "提取待办") {
				t.Fatalf("lookup = %+v, %q, %q", scope, key, fingerprint)
			}
			return 0, nil
		},
		save: func(_ context.Context, id int64, scope draftRunScope, draft taskDraft, key, fingerprint string) (int64, error) {
			if id <= 0 || scope.InitiatorID != 400 || draft.Title != "修复缓存" || draft.SourceMessageID != 600 || key != "request-1" || fingerprint != draftPreparationFingerprint(200, 300, "提取待办") {
				t.Fatalf("save = %d, %+v, %+v, %q, %q", id, scope, draft, key, fingerprint)
			}
			return id, nil
		},
	}
	id, err := p.prepare(context.Background(), "user-token", 200, 300, " 提取待办 ", "request-1")
	if err != nil || id <= 0 {
		t.Fatalf("prepared ID = %d, %v", id, err)
	}
}

func TestDraftPreparerReplaysWithoutCallingGenerator(t *testing.T) {
	p := testDraftPreparer(t)
	p.assignees = &draftAssigneeResolver{users: assigneeClientFunc(func(context.Context, *userpb.ResolveTeamMemberRequest) (*userpb.ResolveTeamMemberResponse, error) {
		t.Fatal("replayed request resolved a new assignee")
		return nil, nil
	})}
	p.generator = taskDraftGeneratorFunc(func(context.Context, string, []*impb.TeamGroupMessage) (taskDraft, error) {
		t.Fatal("replayed request reached model")
		return taskDraft{}, nil
	})
	p.store = draftPreparationStoreFuncs{
		find: func(context.Context, draftRunScope, string, string) (int64, error) { return 8123, nil },
		save: func(context.Context, int64, draftRunScope, taskDraft, string, string) (int64, error) {
			t.Fatal("replayed request wrote another draft")
			return 0, nil
		},
	}
	id, err := p.prepare(context.Background(), "user-token", 200, 300, "提取待办", "request-1")
	if err != nil || id != 8123 {
		t.Fatalf("replay = %d, %v", id, err)
	}
}

func TestDraftPreparerSavesOnlyVerifiedAssigneeResolution(t *testing.T) {
	p := testDraftPreparer(t)
	p.generator = taskDraftGeneratorFunc(func(context.Context, string, []*impb.TeamGroupMessage) (taskDraft, error) {
		return taskDraft{Title: "整理文档", AssigneeName: " 张三 "}, nil
	})
	p.assignees = &draftAssigneeResolver{users: assigneeClientFunc(func(context.Context, *userpb.ResolveTeamMemberRequest) (*userpb.ResolveTeamMemberResponse, error) {
		return &userpb.ResolveTeamMemberResponse{Candidates: matchingCandidates(1)}, nil
	})}
	p.store = draftPreparationStoreFuncs{
		find: func(context.Context, draftRunScope, string, string) (int64, error) { return 0, nil },
		save: func(_ context.Context, id int64, scope draftRunScope, d taskDraft, _, _ string) (int64, error) {
			if scope.InitiatorID != 400 || d.AssigneeName != "张三" || d.AssigneeID != 1 || d.AssigneeResolution != assigneeMatched {
				t.Errorf("saved resolution: %+v, %+v", scope, d)
			}
			return id, nil
		},
	}
	if id, err := p.prepare(context.Background(), "user-token", 200, 300, "张三整理文档", "request-1"); err != nil || id <= 0 {
		t.Fatalf("verified resolution: %d, %v", id, err)
	}
	for _, instruction := range []string{"提取待办", "请看图片"} {
		p.messages = draftMessagesFunc(func(context.Context, string, int64, int64) ([]*impb.TeamGroupMessage, error) {
			return []*impb.TeamGroupMessage{{ContentType: 2, Content: "张三"}}, nil
		})
		p.assignees = &draftAssigneeResolver{users: assigneeClientFunc(func(context.Context, *userpb.ResolveTeamMemberRequest) (*userpb.ResolveTeamMemberResponse, error) {
			t.Fatal("ungrounded mention reached User")
			return nil, nil
		})}
		if id, err := p.prepare(context.Background(), "user-token", 200, 300, instruction, "request-1"); id != 0 || status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("ungrounded mention: %d, %v", id, err)
		}
	}
}

func TestDraftPreparerLookupFailureDoesNotSaveUnassignedDraft(t *testing.T) {
	p := testDraftPreparer(t)
	p.generator = taskDraftGeneratorFunc(func(context.Context, string, []*impb.TeamGroupMessage) (taskDraft, error) {
		return taskDraft{Title: "任务", AssigneeName: "张三"}, nil
	})
	p.assignees = &draftAssigneeResolver{users: assigneeClientFunc(func(context.Context, *userpb.ResolveTeamMemberRequest) (*userpb.ResolveTeamMemberResponse, error) {
		return nil, status.Error(codes.PermissionDenied, "left team")
	})}
	p.store = draftPreparationStoreFuncs{find: func(context.Context, draftRunScope, string, string) (int64, error) { return 0, nil }, save: func(context.Context, int64, draftRunScope, taskDraft, string, string) (int64, error) {
		t.Fatal("failed lookup saved draft")
		return 0, nil
	}}
	if id, err := p.prepare(context.Background(), "user-token", 200, 300, "張三的同事张三来做", "request-1"); id != 0 || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("lookup denial: %d, %v", id, err)
	}
}

func TestDraftPreparerStopsAfterGroupDenial(t *testing.T) {
	p := testDraftPreparer(t)
	p.messages = draftMessagesFunc(func(context.Context, string, int64, int64) ([]*impb.TeamGroupMessage, error) {
		return nil, status.Error(codes.PermissionDenied, "left group")
	})
	p.generator = taskDraftGeneratorFunc(func(context.Context, string, []*impb.TeamGroupMessage) (taskDraft, error) {
		t.Fatal("denied request reached model")
		return taskDraft{}, nil
	})
	id, err := p.prepare(context.Background(), "user-token", 200, 300, "提取待办", "request-1")
	if id != 0 || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("denial = %d, %v", id, err)
	}
}

func TestDraftPreparerRejectsUnverifiedModelFields(t *testing.T) {
	for _, draft := range []taskDraft{
		{Title: "任务", SourceMessageID: 601},
		{Title: "任务", AssigneeID: 500},
		{Title: "任务", DueAtUnixMs: 1000},
		{Title: ""},
	} {
		p := testDraftPreparer(t)
		p.generator = taskDraftGeneratorFunc(func(context.Context, string, []*impb.TeamGroupMessage) (taskDraft, error) { return draft, nil })
		p.store = draftPreparationStoreFuncs{
			find: func(context.Context, draftRunScope, string, string) (int64, error) { return 0, nil },
			save: func(context.Context, int64, draftRunScope, taskDraft, string, string) (int64, error) {
				t.Fatal("unverified draft was saved")
				return 0, nil
			},
		}
		id, err := p.prepare(context.Background(), "user-token", 200, 300, "提取待办", "request-1")
		if id != 0 || status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("draft %+v: %d, %v", draft, id, err)
		}
	}
}

func TestDraftPreparerRejectsInvalidInputAndMasksModelFailure(t *testing.T) {
	p := testDraftPreparer(t)
	for _, tc := range []struct {
		teamID           int64
		instruction, key string
	}{
		{0, "提取待办", "request-1"},
		{200, strings.Repeat("中", 2001), "request-1"},
		{200, "提取待办", "bad key"},
	} {
		id, err := p.prepare(context.Background(), "user-token", tc.teamID, 300, tc.instruction, tc.key)
		if id != 0 || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid input = %d, %v", id, err)
		}
	}
	p.store = draftPreparationStoreFuncs{
		find: func(context.Context, draftRunScope, string, string) (int64, error) { return 0, nil },
	}
	p.generator = taskDraftGeneratorFunc(func(context.Context, string, []*impb.TeamGroupMessage) (taskDraft, error) {
		return taskDraft{}, errors.New("private provider detail")
	})
	id, err := p.prepare(context.Background(), "user-token", 200, 300, "提取待办", "request-1")
	if id != 0 || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private provider detail") {
		t.Fatalf("model error = %d, %v", id, err)
	}
	p.generator = taskDraftGeneratorFunc(func(context.Context, string, []*impb.TeamGroupMessage) (taskDraft, error) {
		return taskDraft{}, status.Error(codes.FailedPrecondition, "private invalid output")
	})
	id, err = p.prepare(context.Background(), "user-token", 200, 300, "提取待办", "request-1")
	if id != 0 || status.Code(err) != codes.FailedPrecondition || strings.Contains(err.Error(), "private invalid output") {
		t.Fatalf("invalid model output = %d, %v", id, err)
	}
}
