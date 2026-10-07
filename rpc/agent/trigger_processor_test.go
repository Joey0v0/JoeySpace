package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/snowflake"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type processorRPCFuncs struct {
	read   func(context.Context, *impb.ReadTaskTriggerContextRequest) (*impb.ReadTaskTriggerContextResponse, error)
	member func(context.Context, *impb.ResolveTaskTriggerMemberRequest) (*impb.ResolveTaskTriggerMemberResponse, error)
}

func (f processorRPCFuncs) ReadTaskTriggerContext(ctx context.Context, req *impb.ReadTaskTriggerContextRequest, _ ...grpc.CallOption) (*impb.ReadTaskTriggerContextResponse, error) {
	return f.read(ctx, req)
}

func (f processorRPCFuncs) ResolveTaskTriggerMember(ctx context.Context, req *impb.ResolveTaskTriggerMemberRequest, _ ...grpc.CallOption) (*impb.ResolveTaskTriggerMemberResponse, error) {
	return f.member(ctx, req)
}

type processorStoreFuncs struct {
	begin func(context.Context, TriggerLease) (bool, error)
	save  func(context.Context, TriggerLease, int64, draftRunScope, []taskDraft, string, string) (int64, error)
}

func (f processorStoreFuncs) BeginModel(ctx context.Context, lease TriggerLease) (bool, error) {
	return f.begin(ctx, lease)
}

func (f processorStoreFuncs) CompleteDraftCollection(ctx context.Context, lease TriggerLease, runID int64, scope draftRunScope, drafts []taskDraft, key, fingerprint string) (int64, error) {
	return f.save(ctx, lease, runID, scope, drafts, key, fingerprint)
}

type processorFixture struct {
	processor  *triggerTaskProcessor
	source     *impb.ReadTaskTriggerContextResponse
	lease      TriggerLease
	drafts     []taskDraft
	events     []string
	reads      int
	models     int
	saves      int
	saved      []taskDraft
	readHook   func(int, *impb.ReadTaskTriggerContextResponse) (*impb.ReadTaskTriggerContextResponse, error)
	memberHook func(*impb.ResolveTaskTriggerMemberResponse) (*impb.ResolveTaskTriggerMemberResponse, error)
	beginHook  func(context.Context) (bool, error)
	modelHook  func(context.Context, []*impb.TeamGroupMessage) ([]taskDraft, error)
	saveHook   func(context.Context, int64) (int64, error)
}

func newProcessorFixture(t *testing.T) *processorFixture {
	t.Helper()
	f := &processorFixture{source: validTriggerClientResponse(triggerClientSourceID), lease: TriggerLease{MessageID: triggerClientSourceID, Token: strings.Repeat("a", 64)}}
	reference := time.Date(2026, 10, 4, 2, 0, 0, 0, time.UTC).UnixMilli()
	f.source.ReferenceTimeUnixMs = reference
	f.source.Instruction = "请张三明天 15:30修复缓存，李四明天下午整理文档"
	f.source.Messages[0].Content = "@AI 整理任务 " + f.source.Instruction
	f.source.Messages[0].CreatedAtUnixMs = reference
	f.source.Messages[1].Content = "张三明天 15:30修复缓存；李四明天下午整理文档"
	f.source.Messages[1].ContentType, f.source.Messages[1].SenderType, f.source.Messages[1].InitiatorId = 1, 1, 0
	f.source.Messages[1].CreatedAtUnixMs = reference - 24*time.Hour.Milliseconds()
	f.drafts = []taskDraft{
		{Title: " 修复缓存 ", Description: " 先检查失效逻辑 ", SourceMessageID: triggerClientSourceID - 1, AssigneeName: "张三", Deadline: draftDeadlineMetadata{Text: "明天 15:30", Source: "message", SourceMessageID: triggerClientSourceID - 1}},
		{Title: "整理文档", AssigneeName: "李四", Deadline: draftDeadlineMetadata{Text: "明天下午", Source: "instruction"}},
	}
	checkMetadata := func(ctx context.Context) {
		t.Helper()
		if md, _ := metadata.FromOutgoingContext(ctx); len(md) != 0 {
			t.Fatalf("caller identity leaked into restricted RPC: %v", md)
		}
	}
	client := &TriggerContextClient{rpc: processorRPCFuncs{
		read: func(ctx context.Context, req *impb.ReadTaskTriggerContextRequest) (*impb.ReadTaskTriggerContextResponse, error) {
			checkMetadata(ctx)
			if req.MessageId != f.lease.MessageID {
				t.Fatalf("source identity changed: %v", req)
			}
			f.reads++
			f.events = append(f.events, fmt.Sprintf("read%d", f.reads))
			response := proto.Clone(f.source).(*impb.ReadTaskTriggerContextResponse)
			if f.readHook != nil {
				return f.readHook(f.reads, response)
			}
			return response, nil
		},
		member: func(ctx context.Context, req *impb.ResolveTaskTriggerMemberRequest) (*impb.ResolveTaskTriggerMemberResponse, error) {
			checkMetadata(ctx)
			if req.MessageId != f.lease.MessageID {
				t.Fatalf("member lookup changed source: %v", req)
			}
			f.events = append(f.events, "member:"+req.Name)
			count := 1
			if req.Name == "李四" {
				count = 2
			}
			response := triggerMemberResponse(req.MessageId, req.Name, count, false)
			if f.memberHook != nil {
				return f.memberHook(response)
			}
			return response, nil
		},
	}}
	f.processor = &triggerTaskProcessor{source: client, nextRunID: func() int64 { return 9007199254741099 }}
	f.processor.generator = collectionGeneratorFunc(func(ctx context.Context, instruction string, messages []*impb.TeamGroupMessage) ([]taskDraft, error) {
		f.events = append(f.events, "generate")
		f.models++
		if instruction != f.source.Instruction || len(messages) != 2 || messages[0].Id != f.lease.MessageID || messages[0].CreatedAtUnixMs != reference {
			t.Fatalf("model input did not come from saved reference: %q %v", instruction, messages)
		}
		if f.modelHook != nil {
			return f.modelHook(ctx, messages)
		}
		return append([]taskDraft(nil), f.drafts...), nil
	})
	f.processor.store = processorStoreFuncs{
		begin: func(ctx context.Context, lease TriggerLease) (bool, error) {
			f.events = append(f.events, "begin")
			if lease != f.lease {
				t.Fatal("model accounting changed lease")
			}
			if f.beginHook != nil {
				return f.beginHook(ctx)
			}
			return true, nil
		},
		save: func(ctx context.Context, lease TriggerLease, runID int64, scope draftRunScope, drafts []taskDraft, key, fingerprint string) (int64, error) {
			f.events = append(f.events, "save")
			f.saves++
			if ctx.Err() != nil || lease != f.lease || runID != 9007199254741099 || scope != (draftRunScope{TeamID: f.source.TeamId, GroupID: f.source.GroupId, InitiatorID: f.source.ActorId}) ||
				key != f.source.RequestKey || fingerprint != draftCollectionFingerprint(scope.TeamID, scope.GroupID, f.source.Instruction, &reference) {
				t.Fatalf("save authority/reference changed: scope=%+v key=%q fingerprint=%q", scope, key, fingerprint)
			}
			f.saved = append([]taskDraft(nil), drafts...)
			if f.saveHook != nil {
				return f.saveHook(ctx, runID)
			}
			return runID, nil
		},
	}
	return f
}

func TestTriggerProcessorGeneratesOnceWithSavedScopeAndPerItemTimeEvidence(t *testing.T) {
	f := newProcessorFixture(t)
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer forbidden", "actor_id", "1"))
	if err := f.processor.Process(ctx, f.lease); err != nil {
		t.Fatal(err)
	}
	wantEvents := []string{"read1", "begin", "generate", "read2", "member:张三", "member:李四", "read3", "save"}
	if !reflect.DeepEqual(f.events, wantEvents) || f.models != 1 || f.saves != 1 || len(f.saved) != 2 {
		t.Fatalf("events=%v models=%d saves=%d drafts=%+v", f.events, f.models, f.saves, f.saved)
	}
	first, second := f.saved[0], f.saved[1]
	due := time.Date(2026, 10, 4, 7, 30, 0, 0, time.UTC).UnixMilli()
	if first.Title != "修复缓存" || first.Description != "先检查失效逻辑" || first.AssigneeResolution != assigneeMatched || first.AssigneeID != 9007199254741013 ||
		first.DueAtUnixMs != due || first.Deadline.ReferenceUnixMs != f.source.Messages[1].CreatedAtUnixMs || first.Deadline.InstructionReferenceUnixMs != f.source.ReferenceTimeUnixMs || first.Deadline.Resolution != "parsed" ||
		second.AssigneeResolution != assigneeAmbiguous || second.AssigneeID != 0 || second.DueAtUnixMs != 0 || second.Deadline.Resolution != "needs_input" || second.Deadline.ReferenceUnixMs != f.source.ReferenceTimeUnixMs {
		t.Fatalf("verified fields=%+v / %+v", first, second)
	}
	if f.drafts[0].DueAtUnixMs != 0 || f.drafts[0].AssigneeID != 0 || f.drafts[0].Deadline.Resolution != "" {
		t.Fatal("model candidates were mutated into trusted results")
	}
}

func TestTriggerProcessorFailureReportsOnlySourceIDStageAndCode(t *testing.T) {
	for _, tc := range []struct {
		name, stage string
		code        codes.Code
		fail        func(*processorFixture)
	}{
		{"source", "source_initial", codes.PermissionDenied, func(f *processorFixture) {
			f.readHook = func(int, *impb.ReadTaskTriggerContextResponse) (*impb.ReadTaskTriggerContextResponse, error) {
				return nil, status.Error(codes.PermissionDenied, "private source text")
			}
		}},
		{"budget", "model_budget", codes.Unavailable, func(f *processorFixture) {
			f.beginHook = func(context.Context) (bool, error) { return false, errors.New("private database detail") }
		}},
		{"model", "model_generate", codes.Unavailable, func(f *processorFixture) {
			f.modelHook = func(context.Context, []*impb.TeamGroupMessage) ([]taskDraft, error) {
				return nil, errors.New("private model output")
			}
		}},
		{"final scope", "source_final", codes.PermissionDenied, func(f *processorFixture) {
			f.readHook = func(read int, response *impb.ReadTaskTriggerContextResponse) (*impb.ReadTaskTriggerContextResponse, error) {
				if read == 3 {
					return nil, status.Error(codes.PermissionDenied, "private group details")
				}
				return response, nil
			}
		}},
		{"assignee", "assignee_resolve", codes.Unavailable, func(f *processorFixture) {
			f.memberHook = func(*impb.ResolveTaskTriggerMemberResponse) (*impb.ResolveTaskTriggerMemberResponse, error) {
				return nil, errors.New("private member details")
			}
		}},
		{"result", "result_persist", codes.Unavailable, func(f *processorFixture) {
			f.saveHook = func(context.Context, int64) (int64, error) { return 0, errors.New("private SQL detail") }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProcessorFixture(t)
			tc.fail(f)
			calls := 0
			f.processor.failureLog = func(messageID int64, stage string, code codes.Code) {
				calls++
				if messageID != f.lease.MessageID || stage != tc.stage || code != tc.code {
					t.Errorf("unsafe failure record: id=%d stage=%q code=%s", messageID, stage, code)
				}
			}
			if err := f.processor.Process(context.Background(), f.lease); status.Code(err) != tc.code || calls != 1 {
				t.Fatalf("failure=%v records=%d", err, calls)
			}
		})
	}
	f := newProcessorFixture(t)
	f.processor.failureLog = func(int64, string, codes.Code) { t.Error("successful processing logged as failure") }
	if err := f.processor.Process(context.Background(), f.lease); err != nil {
		t.Fatal(err)
	}
}

func TestTriggerProcessorAllowsOneToFiveUnassignedUntimedDraftsWithoutMemberCalls(t *testing.T) {
	for _, count := range []int{1, maxGeneratedTaskDrafts} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			f := newProcessorFixture(t)
			f.drafts = make([]taskDraft, count)
			for i := range f.drafts {
				f.drafts[i] = taskDraft{Title: fmt.Sprintf("任务%d", i+1), Deadline: draftDeadlineMetadata{Source: "none"}}
			}
			if err := f.processor.Process(context.Background(), f.lease); err != nil || len(f.saved) != count || f.models != 1 || f.saves != 1 {
				t.Fatalf("valid collection rejected: err=%v events=%v", err, f.events)
			}
			for _, event := range f.events {
				if strings.HasPrefix(event, "member:") {
					t.Fatalf("empty name triggered a member lookup: %v", f.events)
				}
			}
			for _, draft := range f.saved {
				if draft.AssigneeID != 0 || draft.AssigneeResolution != assigneeNone || draft.DueAtUnixMs != 0 || draft.Deadline.Resolution != "none" || draft.Deadline.InstructionReferenceUnixMs != f.source.ReferenceTimeUnixMs {
					t.Fatalf("missing assignment or time was invented: %+v", draft)
				}
			}
		})
	}
}

func TestTriggerProcessorRequiresSourceAndCommittedModelPermissionBeforeGenerate(t *testing.T) {
	for _, name := range []string{"source denied", "source invalid", "begin denied", "begin error", "begin lost"} {
		t.Run(name, func(t *testing.T) {
			f := newProcessorFixture(t)
			want := codes.Unavailable
			switch name {
			case "source denied":
				want = codes.PermissionDenied
				f.readHook = func(int, *impb.ReadTaskTriggerContextResponse) (*impb.ReadTaskTriggerContextResponse, error) {
					return nil, status.Error(want, "private source")
				}
			case "source invalid":
				f.source.ActorId = 0
			case "begin denied":
				want = codes.FailedPrecondition
				f.beginHook = func(context.Context) (bool, error) { return false, nil }
			case "begin error":
				f.beginHook = func(context.Context) (bool, error) { return false, errors.New("private SQL") }
			case "begin lost":
				f.beginHook = func(context.Context) (bool, error) { return false, ErrTriggerLeaseLost }
			}
			err := f.processor.Process(context.Background(), f.lease)
			if (name == "begin lost" && !errors.Is(err, ErrTriggerLeaseLost)) || (name != "begin lost" && status.Code(err) != want) || f.models != 0 || f.saves != 0 || strings.Contains(err.Error(), "private") {
				t.Fatalf("err=%v models=%d saves=%d", err, f.models, f.saves)
			}
			if strings.HasPrefix(name, "source") && len(f.events) != 1 {
				t.Fatalf("failed source reached accounting: %v", f.events)
			}
		})
	}
}

func TestTriggerProcessorRejectsAllModelFailuresAndForgedLastItemsWithoutSaving(t *testing.T) {
	for _, name := range []string{"provider error", "no tasks", "six tasks", "bad title", "member ID", "assignee state", "UTC", "deadline state", "deadline reference", "source absent", "source nontext", "name absent", "time absent"} {
		t.Run(name, func(t *testing.T) {
			f := newProcessorFixture(t)
			want := codes.FailedPrecondition
			switch name {
			case "provider error":
				want = codes.Unavailable
				f.modelHook = func(context.Context, []*impb.TeamGroupMessage) ([]taskDraft, error) {
					return nil, errors.New("private provider body")
				}
			case "no tasks":
				f.drafts = nil
			case "six tasks":
				f.drafts = append(f.drafts, f.drafts...)
				f.drafts = append(f.drafts, f.drafts[0], f.drafts[1])
			case "bad title":
				f.drafts[1].Title = " "
			case "member ID":
				f.drafts[1].AssigneeID = 88
			case "assignee state":
				f.drafts[1].AssigneeResolution = assigneeMatched
			case "UTC":
				f.drafts[1].DueAtUnixMs = f.source.ReferenceTimeUnixMs
			case "deadline state":
				f.drafts[1].Deadline.Resolution = "parsed"
			case "deadline reference":
				f.drafts[1].Deadline.ReferenceUnixMs = f.source.ReferenceTimeUnixMs
			case "source absent":
				f.drafts[1].SourceMessageID = f.lease.MessageID - 20
			case "source nontext":
				f.drafts[1].SourceMessageID = f.lease.MessageID - 1
				f.drafts[0].SourceMessageID = 0
				f.drafts[0].Deadline = draftDeadlineMetadata{Source: "none"}
				f.source.Messages[1].ContentType = 2
			case "name absent":
				f.drafts[1].AssigneeName = "王五"
			case "time absent":
				f.drafts[1].Deadline.Text = "明天 16:00"
			}
			err := f.processor.Process(context.Background(), f.lease)
			if status.Code(err) != want || f.models != 1 || f.saves != 0 || f.saved != nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("partial/forged result: err=%v events=%v saved=%+v", err, f.events, f.saved)
			}
		})
	}
}

func TestTriggerProcessorRechecksCurrentRightsAndImmutableSourceBeforeSaving(t *testing.T) {
	for _, when := range []int{2, 3} {
		for _, name := range []string{"revoked", "actor", "team", "group", "msg ID", "instruction", "reference", "missing evidence"} {
			t.Run(fmt.Sprintf("read%d/%s", when, name), func(t *testing.T) {
				f := newProcessorFixture(t)
				want := codes.FailedPrecondition
				f.readHook = func(n int, r *impb.ReadTaskTriggerContextResponse) (*impb.ReadTaskTriggerContextResponse, error) {
					if n != when {
						return r, nil
					}
					switch name {
					case "revoked":
						return nil, status.Error(codes.PermissionDenied, "private revoked source")
					case "actor":
						r.ActorId++
						r.Messages[0].FromId = r.ActorId
					case "team":
						r.TeamId++
					case "group":
						r.GroupId++
					case "msg ID":
						r.MsgId = "other-source"
						r.Messages[0].MsgId = r.MsgId
					case "instruction":
						r.Instruction += "改动"
						r.Messages[0].Content = "@AI 整理任务 " + r.Instruction
					case "reference":
						r.ReferenceTimeUnixMs += 1000
						r.Messages[0].CreatedAtUnixMs = r.ReferenceTimeUnixMs
					case "missing evidence":
						r.Messages = r.Messages[:1]
					}
					return r, nil
				}
				if name == "revoked" {
					want = codes.PermissionDenied
				}
				err := f.processor.Process(context.Background(), f.lease)
				if status.Code(err) != want || f.models != 1 || f.saves != 0 || strings.Contains(err.Error(), "private") {
					t.Fatalf("changed source saved: err=%v events=%v", err, f.events)
				}
			})
		}
	}
}

func TestTriggerProcessorRestrictedMemberFailureOrWrongScopeRejectsWholeCollection(t *testing.T) {
	for _, name := range []string{"denied second", "wrong scope", "malformed match"} {
		t.Run(name, func(t *testing.T) {
			f := newProcessorFixture(t)
			want := codes.Unavailable
			if name == "denied second" {
				want = codes.PermissionDenied
			}
			f.memberHook = func(r *impb.ResolveTaskTriggerMemberResponse) (*impb.ResolveTaskTriggerMemberResponse, error) {
				if r.Name != "李四" {
					return r, nil
				}
				switch name {
				case "denied second":
					return nil, status.Error(codes.PermissionDenied, "private member")
				case "wrong scope":
					r.TeamId++
				case "malformed match":
					r.Candidates[0].Nickname = "其他人"
				}
				return r, nil
			}
			err := f.processor.Process(context.Background(), f.lease)
			if status.Code(err) != want || f.models != 1 || f.saves != 0 || strings.Contains(err.Error(), "private") {
				t.Fatalf("member failure returned partial result: %v %v", err, f.events)
			}
		})
	}
}

func TestTriggerProcessorCancellationNeverReachesLaterModelOrSave(t *testing.T) {
	for _, stage := range []string{"before", "begin", "model", "final read", "ID generation"} {
		t.Run(stage, func(t *testing.T) {
			f := newProcessorFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch stage {
			case "before":
				cancel()
			case "begin":
				f.beginHook = func(context.Context) (bool, error) { cancel(); return true, nil }
			case "model":
				f.modelHook = func(context.Context, []*impb.TeamGroupMessage) ([]taskDraft, error) { cancel(); return f.drafts, nil }
			case "final read":
				f.readHook = func(n int, r *impb.ReadTaskTriggerContextResponse) (*impb.ReadTaskTriggerContextResponse, error) {
					if n == 3 {
						cancel()
					}
					return r, nil
				}
			case "ID generation":
				f.processor.nextRunID = func() int64 { cancel(); return 9007199254741099 }
			}
			err := f.processor.Process(ctx, f.lease)
			if status.Code(err) != codes.Canceled || f.saves != 0 || (stage == "before" || stage == "begin") && f.models != 0 {
				t.Fatalf("canceled work continued: %v %v", err, f.events)
			}
		})
	}
}

func TestTriggerProcessorCompleteFailureDoesNotRetryAndCommittedCancellationIsSuccess(t *testing.T) {
	for _, name := range []string{"SQL", "lease lost", "wrong result ID", "committed then canceled"} {
		t.Run(name, func(t *testing.T) {
			f := newProcessorFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.saveHook = func(_ context.Context, runID int64) (int64, error) {
				switch name {
				case "SQL":
					return 0, errors.New("private SQL body")
				case "lease lost":
					return 0, ErrTriggerLeaseLost
				case "wrong result ID":
					return runID + 1, nil
				default:
					cancel()
					return runID, nil
				}
			}
			err := f.processor.Process(ctx, f.lease)
			if f.models != 1 || f.saves != 1 {
				t.Fatalf("unexpected retry: %v", f.events)
			}
			switch name {
			case "committed then canceled":
				if err != nil {
					t.Fatalf("committed result reported failure: %v", err)
				}
			case "lease lost":
				if !errors.Is(err, ErrTriggerLeaseLost) {
					t.Fatalf("lease failure lost: %v", err)
				}
			default:
				if status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private") {
					t.Fatalf("unsafe save error: %v", err)
				}
			}
		})
	}
}

func TestTriggerProcessorSnapshotsFactsAndAllowsUnrelatedHistoryChanges(t *testing.T) {
	f := newProcessorFixture(t)
	f.modelHook = func(_ context.Context, messages []*impb.TeamGroupMessage) ([]taskDraft, error) {
		messages[0].FromId = 1
		messages[0].Content = "malicious generator mutation"
		return f.drafts, nil
	}
	if err := f.processor.Process(context.Background(), f.lease); err != nil || f.source.Messages[0].FromId == 1 || f.saves != 1 {
		t.Fatalf("generator changed immutable scope: %v %v", err, f.source)
	}
	f = newProcessorFixture(t)
	f.readHook = func(n int, r *impb.ReadTaskTriggerContextResponse) (*impb.ReadTaskTriggerContextResponse, error) {
		if n > 1 {
			r.Messages = append(r.Messages, &impb.TeamGroupMessage{Id: f.lease.MessageID - 2, MsgId: "earlier", FromId: 77, ContentType: 1, Content: "无关历史", SenderType: 1, CreatedAtUnixMs: r.ReferenceTimeUnixMs})
		}
		return r, nil
	}
	if err := f.processor.Process(context.Background(), f.lease); err != nil || f.saves != 1 {
		t.Fatalf("unrelated history rejected: %v %v", err, f.events)
	}
}

func TestTriggerProcessorDoesNotAcceptModelSourceAbsentFromOriginalContext(t *testing.T) {
	f := newProcessorFixture(t)
	f.drafts[1] = taskDraft{Title: "后来才可见的来源", SourceMessageID: f.lease.MessageID - 2, Deadline: draftDeadlineMetadata{Source: "none"}}
	f.readHook = func(n int, r *impb.ReadTaskTriggerContextResponse) (*impb.ReadTaskTriggerContextResponse, error) {
		if n > 1 {
			r.Messages = append(r.Messages, &impb.TeamGroupMessage{Id: f.lease.MessageID - 2, MsgId: "newly-visible", FromId: 77, ContentType: 1, Content: "后来才可见的来源", SenderType: 1, CreatedAtUnixMs: r.ReferenceTimeUnixMs})
		}
		return r, nil
	}
	if err := f.processor.Process(context.Background(), f.lease); status.Code(err) != codes.FailedPrecondition || f.models != 1 || f.saves != 0 {
		t.Fatalf("model invented a source outside its supplied context: err=%v events=%v", err, f.events)
	}
}

func TestTriggerTaskProcessorConstructorReusesConfiguredGeneratorAndNode(t *testing.T) {
	f := newProcessorFixture(t)
	store, _ := testDraftStore(t)
	node, err := snowflake.NewNode(1)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{preparer: &draftPreparer{generator: f.processor.generator.(collectionGeneratorFunc), idNode: node}}
	source := f.processor.source.(*TriggerContextClient)
	inbox := NewTriggerInboxStore(store.db)
	processor, err := NewTriggerTaskProcessor(server, source, inbox)
	if err != nil {
		t.Fatal(err)
	}
	implementation := processor.(*triggerTaskProcessor)
	if implementation.source != source || implementation.store != inbox || reflect.ValueOf(implementation.generator).Pointer() != reflect.ValueOf(server.preparer.generator).Pointer() || implementation.nextRunID() <= 0 {
		t.Fatal("constructor did not reuse existing dependencies")
	}
	for _, name := range []string{"nil server", "nil source", "nil store", "no generator", "uncompiled Eino", "typed nil Eino", "no node"} {
		t.Run(name, func(t *testing.T) {
			configured := &Server{preparer: &draftPreparer{generator: server.preparer.generator, idNode: node}}
			client, saved := source, inbox
			switch name {
			case "nil server":
				configured = nil
			case "nil source":
				client = nil
			case "nil store":
				saved = nil
			case "no generator":
				configured.preparer.generator = nil
			case "uncompiled Eino":
				configured.preparer.generator = &EinoTaskDraftGenerator{}
			case "typed nil Eino":
				configured.preparer.generator = (*EinoTaskDraftGenerator)(nil)
			case "no node":
				configured.preparer.idNode = nil
			}
			if processor, err := NewTriggerTaskProcessor(configured, client, saved); processor != nil || status.Code(err) != codes.Unavailable {
				t.Fatalf("incomplete configuration accepted: %v %v", processor, err)
			}
		})
	}
}
