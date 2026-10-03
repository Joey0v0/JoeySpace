package agent

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/cloudwego/eino/schema"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func parsedDeadlineFixture() draftDeadlineMetadata {
	return draftDeadlineMetadata{Text: "2026-10-04 09:00", Source: "instruction", Timezone: draftDeadlineTimezone, Resolution: "parsed", ParsedUnixMs: 1791075600000}
}

func needsDeadlineFixture() draftDeadlineMetadata {
	return draftDeadlineMetadata{Text: "明天下午", Source: "instruction", Timezone: draftDeadlineTimezone, Resolution: "needs_input", Reason: "unsupported_expression", ReferenceUnixMs: 1790996400000, InstructionReferenceUnixMs: 1790996400000}
}

func TestDraftDeadlineMetadataValidatesCompleteShapeAndLegacy(t *testing.T) {
	parsed := parsedDeadlineFixture()
	needs := needsDeadlineFixture()
	for _, tc := range []struct {
		name string
		meta draftDeadlineMetadata
		due  int64
	}{
		{"legacy", draftDeadlineMetadata{}, 1000},
		{"none preserves request reference", draftDeadlineMetadata{Source: "none", Timezone: draftDeadlineTimezone, Resolution: "none", InstructionReferenceUnixMs: 1000}, 0},
		{"absolute without reference", parsed, parsed.ParsedUnixMs},
		{"needs input", needs, 0},
		{"none manually selected", draftDeadlineMetadata{Source: "none", Timezone: draftDeadlineTimezone, Resolution: "selected"}, 1000},
		{"none explicitly unset", draftDeadlineMetadata{Source: "none", Timezone: draftDeadlineTimezone, Resolution: "unset"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.meta.valid(tc.due) {
				t.Fatalf("valid shape rejected: %+v", tc.meta)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		mutate func(*draftDeadlineMetadata)
		due    int64
	}{
		{"partial metadata", func(m *draftDeadlineMetadata) { m.Timezone = "" }, parsed.ParsedUnixMs},
		{"wrong timezone", func(m *draftDeadlineMetadata) { m.Timezone = "UTC" }, parsed.ParsedUnixMs},
		{"unknown source", func(m *draftDeadlineMetadata) { m.Source = "other" }, parsed.ParsedUnixMs},
		{"instruction ID", func(m *draftDeadlineMetadata) { m.SourceMessageID = 99 }, parsed.ParsedUnixMs},
		{"reference mismatch", func(m *draftDeadlineMetadata) { m.ReferenceUnixMs = 1 }, parsed.ParsedUnixMs},
		{"message missing reference", func(m *draftDeadlineMetadata) { m.Source = "message"; m.SourceMessageID = 99 }, parsed.ParsedUnixMs},
		{"candidate and reason", func(m *draftDeadlineMetadata) { m.Reason = "invalid_date" }, parsed.ParsedUnixMs},
		{"unsupported reason", func(m *draftDeadlineMetadata) { m.Resolution = "needs_input"; m.ParsedUnixMs = 0; m.Reason = "other" }, 0},
		{"parsed with no candidate", func(m *draftDeadlineMetadata) { m.ParsedUnixMs = 0 }, 0},
		{"parsed due differs", func(*draftDeadlineMetadata) {}, 1000},
		{"selected without due", func(m *draftDeadlineMetadata) { m.Resolution = "selected" }, 0},
		{"unset with due", func(m *draftDeadlineMetadata) { m.Resolution = "unset" }, 1000},
		{"negative reference", func(m *draftDeadlineMetadata) { m.ReferenceUnixMs = -1; m.InstructionReferenceUnixMs = -1 }, parsed.ParsedUnixMs},
		{"too large candidate", func(m *draftDeadlineMetadata) { m.ParsedUnixMs = maxDraftDueAtUnixMs + 1 }, parsed.ParsedUnixMs},
		{"missing reference with positive reference", func(m *draftDeadlineMetadata) {
			m.Resolution = "needs_input"
			m.ParsedUnixMs = 0
			m.Reason = "missing_reference"
			m.ReferenceUnixMs = 1000
			m.InstructionReferenceUnixMs = 1000
		}, 0},
		{"missing reference for message", func(m *draftDeadlineMetadata) {
			m.Source = "message"
			m.SourceMessageID = 99
			m.ReferenceUnixMs = 1000
			m.Resolution = "needs_input"
			m.ParsedUnixMs = 0
			m.Reason = "missing_reference"
		}, 0},
		{"invalid utf8", func(m *draftDeadlineMetadata) { m.Text = "\xff" }, parsed.ParsedUnixMs},
		{"trimmed text", func(m *draftDeadlineMetadata) { m.Text = " 明天 09:00" }, parsed.ParsedUnixMs},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := parsed
			tc.mutate(&m)
			if m.valid(tc.due) {
				t.Fatalf("invalid shape accepted: %+v", m)
			}
		})
	}
}

func TestEinoTaskDraftRequiresSevenFieldsAndRejectsTrustedDeadlineOutput(t *testing.T) {
	base := map[string]any{"title": "任务", "description": "", "source_message_id": "0", "assignee_name": "", "deadline_text": "", "deadline_source": "none", "deadline_source_message_id": "0"}
	for _, field := range []string{"deadline_text", "deadline_source", "deadline_source_message_id"} {
		for _, value := range []any{nil, 123, true} {
			t.Run(field, func(t *testing.T) {
				obj := map[string]any{}
				for k, v := range base {
					obj[k] = v
				}
				obj[field] = value
				body, _ := json.Marshal(obj)
				g, _ := NewEinoTaskDraftGenerator(context.Background(), chatModelFunc(func(context.Context, []*schema.Message) (*schema.Message, error) {
					return schema.AssistantMessage(string(body), nil), nil
				}))
				if _, err := g.GenerateDraft(context.Background(), "任务", nil); status.Code(err) != codes.FailedPrecondition {
					t.Fatal(err)
				}
			})
		}
		obj := map[string]any{}
		for k, v := range base {
			if k != field {
				obj[k] = v
			}
		}
		body, _ := json.Marshal(obj)
		g, _ := NewEinoTaskDraftGenerator(context.Background(), chatModelFunc(func(context.Context, []*schema.Message) (*schema.Message, error) {
			return schema.AssistantMessage(string(body), nil), nil
		}))
		if _, err := g.GenerateDraft(context.Background(), "任务", nil); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("missing %s: %v", field, err)
		}
	}
	for _, value := range []string{"-1", "01", "+1", "9223372036854775808"} {
		base["deadline_source_message_id"] = value
		body, _ := json.Marshal(base)
		g, _ := NewEinoTaskDraftGenerator(context.Background(), chatModelFunc(func(context.Context, []*schema.Message) (*schema.Message, error) {
			return schema.AssistantMessage(string(body), nil), nil
		}))
		if _, err := g.GenerateDraft(context.Background(), "任务", nil); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("bad source ID %s: %v", value, err)
		}
	}
	base["deadline_source_message_id"] = "0"
	base["due_at_unix_ms"] = 1000
	body, _ := json.Marshal(base)
	g, _ := NewEinoTaskDraftGenerator(context.Background(), chatModelFunc(func(context.Context, []*schema.Message) (*schema.Message, error) {
		return schema.AssistantMessage(string(body), nil), nil
	}))
	if _, err := g.GenerateDraft(context.Background(), "任务", nil); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
}

func TestDraftPreparerExtractsVerifiesAndPersistsDeadlineOnce(t *testing.T) {
	reference := deadlineTestUTC(t, "2026-10-03T04:00:00Z")
	messageRef := deadlineTestUTC(t, "2026-10-01T04:00:00Z")
	for _, tc := range []struct {
		name, text, source, resolution     string
		id, expectedDue, expectedReference int64
	}{
		{"instruction parsed", "明天 09:00", "instruction", "parsed", 0, deadlineTestUTC(t, "2026-10-04T01:00:00Z"), reference},
		{"message independent source", "明天 09:00", "message", "parsed", 9007199254740993, deadlineTestUTC(t, "2026-10-02T01:00:00Z"), messageRef},
		{"ambiguous time", "明天下午", "instruction", "needs_input", 0, 0, reference},
		{"no time", "", "none", "none", 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testDraftPreparer(t)
			var calls int
			var saved taskDraft
			var savedID int64
			p.messages = draftMessagesFunc(func(context.Context, string, int64, int64) ([]*impb.TeamGroupMessage, error) {
				return []*impb.TeamGroupMessage{{Id: 9007199254740993, ContentType: 1, Content: "明天 09:00完成任务", CreatedAtUnixMs: messageRef}}, nil
			})
			body, _ := json.Marshal(map[string]any{"title": "任务", "description": "", "source_message_id": "0", "assignee_name": "", "deadline_text": tc.text, "deadline_source": tc.source, "deadline_source_message_id": strconv.FormatInt(tc.id, 10)})
			generator, err := NewEinoTaskDraftGenerator(context.Background(), chatModelFunc(func(context.Context, []*schema.Message) (*schema.Message, error) {
				calls++
				return schema.AssistantMessage(string(body), nil), nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			p.generator = generator
			p.store = draftPreparationStoreFuncs{find: func(context.Context, draftRunScope, string, string) (int64, error) { return savedID, nil }, save: func(_ context.Context, id int64, _ draftRunScope, d taskDraft, _, _ string) (int64, error) {
				savedID = id
				saved = d
				return id, nil
			}}
			for attempt := 0; attempt < 2; attempt++ {
				id, err := p.prepareWithReference(context.Background(), "user-token", 200, 300, "明天 09:00或明天下午交付", "request-1", &reference)
				if err != nil || id <= 0 {
					t.Fatalf("prepare: %d %v", id, err)
				}
			}
			if calls != 1 || saved.SourceMessageID != 0 || saved.DueAtUnixMs != tc.expectedDue || saved.Deadline.Resolution != tc.resolution || saved.Deadline.ReferenceUnixMs != tc.expectedReference || saved.Deadline.InstructionReferenceUnixMs != reference || saved.Deadline.SourceMessageID != tc.id || !saved.Deadline.valid(saved.DueAtUnixMs) {
				t.Fatalf("saved: %+v, calls %d", saved, calls)
			}
		})
	}
}

func TestDraftDeadlineMetadataPersistsReadsLocksAndRejectsPartialRows(t *testing.T) {
	store, mock := testDraftStore(t)
	run := confirmationRun()
	run.Draft.Deadline = parsedDeadlineFixture()
	run.Draft.DueAtUnixMs = run.Draft.Deadline.ParsedUnixMs
	m := run.Draft.Deadline
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO agent_runs").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO agent_task_drafts").WithArgs(run.ID, run.Draft.Title, run.Draft.Description, run.Draft.AssigneeID, run.Draft.DueAtUnixMs, run.Draft.SourceMessageID, run.Draft.AssigneeName, string(run.Draft.AssigneeResolution), m.Text, m.Source, m.SourceMessageID, m.ReferenceUnixMs, m.Timezone, m.Resolution, m.Reason, m.ParsedUnixMs, m.InstructionReferenceUnixMs).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if _, err := store.saveWaitingDraft(context.Background(), run.ID, run.Scope, run.Draft, "request-1", testDraftFingerprint); err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftForInitiator)).WithArgs(run.ID, run.Scope.InitiatorID).WillReturnRows(confirmationRows(run))
	loaded, err := store.loadDraftForInitiator(context.Background(), run.ID, run.Scope.InitiatorID)
	if err != nil || loaded != run {
		t.Fatalf("read: %+v %v", loaded, err)
	}
	response := taskDraftRPCResponse(loaded)
	if response.GetDraft().GetDeadline() == nil || response.GetDraft().GetDeadline().GetParsedUnixMs() != m.ParsedUnixMs {
		t.Fatalf("RPC metadata: %v", response)
	}
	bad := run
	bad.Draft.Deadline.Timezone = ""
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftForInitiator)).WithArgs(run.ID, run.Scope.InitiatorID).WillReturnRows(confirmationRows(bad))
	if _, err := store.loadDraftForInitiator(context.Background(), run.ID, run.Scope.InitiatorID); status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
	expectConfirmationLock(mock, bad)
	mock.ExpectRollback()
	if _, err := store.freezeDraft(context.Background(), run.ID, run.Scope.InitiatorID, run.Draft.Title, run.Draft.Description, run.Revision, nil, &run.Draft.DueAtUnixMs, "parsed"); status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
	if taskDraftRPCResponse(confirmationRun()).GetDraft().GetDeadline() != nil {
		t.Fatal("legacy metadata was invented")
	}
}

func TestDraftDeadlineHumanChoicePreservesEvidenceAndVersionsStateChange(t *testing.T) {
	for _, due := range []int64{0, 1791075600001} {
		t.Run(strconv.FormatInt(due, 10), func(t *testing.T) {
			store, mock := testDraftStore(t)
			run := confirmationRun()
			run.Draft.Deadline = needsDeadlineFixture()
			run.Draft.DueAtUnixMs = 0
			resolution := "selected"
			if due == 0 {
				resolution = "unset"
			}
			expectConfirmationLock(mock, run)
			mock.ExpectExec("UPDATE agent_task_drafts SET due_at_unix_ms = \\?, deadline_resolution = \\?, revision = revision \\+ 1").WithArgs(due, resolution, run.ID).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			got, err := store.updateDraftDeadline(context.Background(), run, due)
			want := run
			want.Draft.Deadline.Resolution = resolution
			want.Draft.DueAtUnixMs = due
			want.Revision++
			if err != nil || got != want {
				t.Fatalf("choice: %+v %v", got, err)
			}
			expectConfirmationLock(mock, got)
			mock.ExpectCommit()
			again, err := store.updateDraftDeadline(context.Background(), got, due)
			if err != nil || again != got {
				t.Fatalf("noop: %+v %v", again, err)
			}
		})
	}
}

func TestDraftDeadlineNewStatesRequireExplicitReviewAndFreezeSafely(t *testing.T) {
	for _, state := range []draftRunStatus{draftWaitingConfirmation, draftCreating, draftSucceeded} {
		for _, tc := range []struct {
			name, resolution string
			due              *int64
			want             codes.Code
		}{
			{"missing state", "", draftID(0), codes.FailedPrecondition}, {"missing value", "unset", nil, codes.FailedPrecondition}, {"stale state", "needs_input", draftID(0), codes.Aborted}, {"reviewed zero", "unset", draftID(0), codes.OK},
		} {
			t.Run(string(state)+tc.name, func(t *testing.T) {
				store, mock := testDraftStore(t)
				run := confirmationRun()
				run.Status = state
				run.Draft.Deadline = needsDeadlineFixture()
				run.Draft.Deadline.Resolution = "unset"
				run.Draft.DueAtUnixMs = 0
				if state != draftWaitingConfirmation {
					run.TaskRequestKey = draftTaskRequestKey(run.ID)
				}
				if state == draftSucceeded {
					run.TaskID = 123
				}
				expectConfirmationLock(mock, run)
				if tc.want == codes.OK {
					if state == draftWaitingConfirmation {
						mock.ExpectExec("UPDATE agent_task_drafts SET task_request_key").WillReturnResult(sqlmock.NewResult(0, 1))
						mock.ExpectExec("UPDATE agent_runs SET status").WillReturnResult(sqlmock.NewResult(0, 1))
					}
					mock.ExpectCommit()
				} else {
					mock.ExpectRollback()
				}
				got, err := store.freezeDraft(context.Background(), run.ID, run.Scope.InitiatorID, run.Draft.Title, run.Draft.Description, run.Revision, nil, tc.due, tc.resolution)
				if status.Code(err) != tc.want || (tc.want == codes.OK && (got.Draft != run.Draft || got.Revision != run.Revision)) {
					t.Fatalf("freeze: %+v %v", got, err)
				}
			})
		}
	}
	for _, resolution := range []string{"", "needs_input"} {
		draft := taskDraft{Deadline: needsDeadlineFixture()}
		if err := draft.requireDeadlineStateReview(draftID(0), resolution); status.Code(err) != codes.FailedPrecondition {
			t.Fatal(err)
		}
	}
}

func TestConfirmNeedsInputNeverCallsTaskAndRejectsInvalidState(t *testing.T) {
	run := confirmationRun()
	run.Draft.Deadline = needsDeadlineFixture()
	run.Draft.DueAtUnixMs = 0
	reader := confirmationReader(t, func(context.Context, int64, int64) (taskDraftRun, error) { return run, nil }, nil)
	confirmer := &draftConfirmer{store: confirmationStoreStub{freeze: func(context.Context, int64, int64, string, string) (taskDraftRun, error) {
		t.Fatal("unresolved time reached freeze")
		return run, nil
	}}, tasks: draftTaskCreateFunc(nil)}
	for _, tc := range []struct {
		resolution string
		want       codes.Code
	}{{"", codes.FailedPrecondition}, {"needs_input", codes.FailedPrecondition}, {"guess", codes.InvalidArgument}} {
		ctx, cancel := draftRPCContext()
		defer cancel()
		req := confirmDraftRequest()
		req.ExpectedDueAtUnixMs = draftID(0)
		req.ExpectedDeadlineResolution = tc.resolution
		if _, err := testTaskDraftClient(t, reader, confirmer).ConfirmTaskDraft(ctx, req); status.Code(err) != tc.want {
			t.Fatal(err)
		}
	}
}

func TestDraftPreparerRejectsUntrustedDeadlineMetadataOrMissingEvidence(t *testing.T) {
	for _, metadata := range []draftDeadlineMetadata{
		{}, {Source: "instruction", Text: "凭空时间"}, {Source: "message", Text: "修复缓存", SourceMessageID: 601},
		{Source: "none", ReferenceUnixMs: 1000}, {Source: "none", Timezone: draftDeadlineTimezone},
		{Source: "none", Resolution: "none"}, {Source: "none", Reason: "unsupported_expression"},
		{Source: "none", ParsedUnixMs: 1000}, {Source: "none", InstructionReferenceUnixMs: 1000},
	} {
		p := testDraftPreparer(t)
		p.generator = taskDraftGeneratorFunc(func(context.Context, string, []*impb.TeamGroupMessage) (taskDraft, error) {
			return taskDraft{Title: "任务", Deadline: metadata}, nil
		})
		p.store = draftPreparationStoreFuncs{
			find: func(context.Context, draftRunScope, string, string) (int64, error) { return 0, nil },
			save: func(context.Context, int64, draftRunScope, taskDraft, string, string) (int64, error) {
				t.Fatal("unverified deadline was saved")
				return 0, nil
			},
		}
		if id, err := p.prepare(context.Background(), "user-token", 200, 300, "提取待办", "request-1"); id != 0 || status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("trusted or missing evidence: %+v, %d, %v", metadata, id, err)
		}
	}
}

func TestDraftTextAndAssigneeEditsPreserveFullDeadlineEvidence(t *testing.T) {
	run := confirmationRun()
	run.Draft.DueAtUnixMs = 0
	run.Draft.Deadline = needsDeadlineFixture()
	reader := confirmationReader(t, func(context.Context, int64, int64) (taskDraftRun, error) { return run, nil }, nil)
	reader.editor = draftTextUpdateFunc(func(context.Context, taskDraftRun, string, string) error { return nil })
	got, err := reader.editText(context.Background(), "user-token", run.ID, run.Draft.Title, run.Draft.Description, "新标题", run.Draft.Description, run.Revision)
	if err != nil || got.Draft.Deadline != run.Draft.Deadline || got.Draft.DueAtUnixMs != 0 || got.Revision != 2 {
		t.Fatalf("text edit: %+v, %v", got, err)
	}
	store, mock := testDraftStore(t)
	expectConfirmationLock(mock, run)
	mock.ExpectExec("UPDATE agent_task_drafts SET assignee_id").WithArgs(int64(501), "selected", run.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	got, err = store.updateDraftAssignee(context.Background(), run, 501)
	if err != nil || got.Draft.Deadline != run.Draft.Deadline || got.Draft.DueAtUnixMs != 0 || got.Revision != 2 {
		t.Fatalf("assignee edit: %+v, %v", got, err)
	}
}
