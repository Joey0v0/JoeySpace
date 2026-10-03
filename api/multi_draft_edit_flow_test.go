package main

import (
	"context"
	"database/sql/driver"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	agent "github.com/yjydist/go-im/rpc/agent"
	agentpb "github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	multiEditTargetID = multiPersistenceAssigneeID + 6
	multiEditWhere    = " WHERE run_id = ? AND item_index = ? AND revision = ? AND status = 'waiting_confirmation'"
	multiEditTextSQL  = "UPDATE agent_task_drafts SET title = ?, description = ?, revision = revision + 1" + multiEditWhere
	multiEditWhoSQL   = "UPDATE agent_task_drafts SET assignee_id = ?, assignee_resolution = ?, revision = revision + 1" + multiEditWhere
	multiEditDueSQL   = "UPDATE agent_task_drafts SET due_at_unix_ms = ?, deadline_resolution = ?, revision = revision + 1" + multiEditWhere
)

type multiEditUser struct {
	confirmFlowUser
	revoked atomic.Bool
	checks  atomic.Int32
}

func (s *multiEditUser) CheckTeamMemberByID(ctx context.Context, req *userpb.CheckTeamMemberByIDRequest) (*userpb.CheckTeamMemberByIDResponse, error) {
	s.checks.Add(1)
	md, _ := metadata.FromIncomingContext(ctx)
	if auth := md.Get("authorization"); len(auth) != 1 || auth[0] != "Bearer user-token" || len(md.Get("idempotency-key")) != 0 || req.GetTeamId() != flowTeamID || (req.GetUserId() != multiEditTargetID && req.GetUserId() != multiPersistenceAssigneeID) {
		return nil, status.Error(codes.InvalidArgument, "changed target member scope")
	}
	if s.revoked.Load() {
		return nil, status.Error(codes.PermissionDenied, "private target member revoked")
	}
	return &userpb.CheckTeamMemberByIDResponse{}, nil
}

type multiEditFlow struct {
	*multiPersistenceFlow
	member *multiEditUser
}

func newMultiEditFlow(t *testing.T) *multiEditFlow {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	db, err := gorm.Open(gormmysql.New(gormmysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	f := &multiEditFlow{multiPersistenceFlow: &multiPersistenceFlow{t: t, mock: mock, im: &multiPersistenceIM{}, httpClient: &http.Client{Timeout: 5 * time.Second}}, member: &multiEditUser{}}
	f.runID.Store(9007199254741011)
	parsed := time.Date(2026, 10, 4, 7, 30, 0, 0, time.UTC).UnixMilli()
	messageReference := time.Date(2026, 10, 3, 4, 0, 0, 456000000, time.UTC).UnixMilli()
	reference := time.Date(2026, 10, 6, 16, 0, 0, 123000000, time.UTC).UnixMilli()
	f.drafts = []*agentpb.TaskDraftItem{
		{Title: "修复缓存", Description: "修复任务", SourceMessageId: multiPersistenceSourceID, AssigneeName: "张三", AssigneeResolution: "matched", AssigneeId: multiPersistenceAssigneeID, DueAtUnixMs: parsed, Revision: 1,
			Deadline: &agentpb.TaskDraftDeadline{Text: "明天 15:30", Source: "message", SourceMessageId: multiPersistenceTimeID, ReferenceUnixMs: messageReference, Timezone: "Asia/Shanghai", Resolution: "parsed", ParsedUnixMs: parsed, InstructionReferenceUnixMs: reference}},
		{Title: "整理文档", SourceMessageId: multiPersistenceTimeID, AssigneeName: "李四", AssigneeResolution: "ambiguous", Revision: 1,
			Deadline: &agentpb.TaskDraftDeadline{Text: "明天下午", Source: "instruction", ReferenceUnixMs: reference, Timezone: "Asia/Shanghai", Resolution: "needs_input", Reason: "unsupported_expression", InstructionReferenceUnixMs: reference}},
	}
	dial := func(addr string) *grpc.ClientConn {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	users := userpb.NewUserClient(dial(startFlowRPC(t, func(s *grpc.Server) { userpb.RegisterUserServer(s, f.member) })))
	ims := impb.NewIMClient(dial(startFlowRPC(t, func(s *grpc.Server) { impb.RegisterIMServer(s, f.im) })))
	impl := agent.NewServer(nil)
	// Only the normal access configuration is needed for all three editing RPCs.
	// No model, preparation, Task writer or bot replier is configured.
	impl.ConfigureDraftAccess(db, users, ims)
	client := agentpb.NewAgentClient(dial(startFlowRPC(t, func(s *grpc.Server) { agentpb.RegisterAgentServer(s, impl) })))
	mux := http.NewServeMux()
	wrap := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			h(w, pathvar.WithVars(r, map[string]string{"run_id": r.PathValue("run_id"), "item_index": r.PathValue("item_index")}))
		}
	}
	mux.HandleFunc("GET /runs/{run_id}/drafts", wrap(getTaskDraftCollectionHandler(client)))
	mux.HandleFunc("GET /runs/{run_id}/drafts/{item_index}", wrap(getTaskDraftItemHandler(client)))
	mux.HandleFunc("PUT /runs/{run_id}/drafts/{item_index}", wrap(editTaskDraftItemTextHandler(client)))
	mux.HandleFunc("PUT /runs/{run_id}/drafts/{item_index}/assignee", wrap(selectTaskDraftItemAssigneeHandler(client)))
	mux.HandleFunc("PUT /runs/{run_id}/drafts/{item_index}/deadline", wrap(editTaskDraftItemDeadlineHandler(client)))
	mux.HandleFunc("GET /runs/{run_id}/draft", wrap(getTaskDraftHandler(client)))
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *multiEditFlow) path(index int, suffix string) string {
	return fmt.Sprintf("/runs/%d/drafts/%d%s", f.runID.Load(), index, suffix)
}

func multiEditClone(drafts []*agentpb.TaskDraftItem) []*agentpb.TaskDraftItem {
	result := make([]*agentpb.TaskDraftItem, len(drafts))
	for index, draft := range drafts {
		result[index] = proto.Clone(draft).(*agentpb.TaskDraftItem)
	}
	return result
}

func (f *multiEditFlow) rowsWith(drafts []*agentpb.TaskDraftItem) *sqlmock.Rows {
	before := f.drafts
	f.drafts = drafts
	rows := f.collectionRows()
	f.drafts = before
	return rows
}

func (f *multiEditFlow) expectLock(drafts []*agentpb.TaskDraftItem) {
	f.mock.ExpectBegin()
	f.mock.ExpectQuery(multiPersistenceSelect+" FOR UPDATE$").WithArgs(f.runID.Load(), int64(400)).WillReturnRows(f.rowsWith(drafts))
}

// Exact SQL/parameters ensure only the selected item's permitted fields and
// version change. In particular original mentions and all time evidence stay
// outside these UPDATE statements.
func (f *multiEditFlow) expectWrite(sql string, index int, first, second driver.Value, revision int64, affected int64) {
	f.mock.ExpectExec(regexp.QuoteMeta(sql)+"$").WithArgs(first, second, f.runID.Load(), int64(index), revision).WillReturnResult(sqlmock.NewResult(0, affected))
}

func (f *multiEditFlow) save(index int, suffix, body, update string, first, second driver.Value, apply func(*agentpb.TaskDraftItem)) {
	f.t.Helper()
	previous := f.drafts[index].Revision
	f.expectRead()
	f.expectLock(f.drafts)
	if update != "" {
		f.expectWrite(update, index, first, second, previous, 1)
	}
	f.mock.ExpectCommit()
	if apply != nil {
		apply(f.drafts[index])
		f.drafts[index].Revision++
	}
	data := f.request(http.MethodPut, f.path(index, suffix), body, http.StatusOK)["data"].(map[string]any)
	f.checkScope(data)
	f.checkItem(data["item"], index)
}

func (f *multiEditFlow) readAll() {
	f.t.Helper()
	f.expectRead()
	data := f.request(http.MethodGet, fmt.Sprintf("/runs/%d/drafts", f.runID.Load()), "", http.StatusOK)["data"].(map[string]any)
	f.checkScope(data)
	items, ok := data["items"].([]any)
	if !ok || len(items) != 2 {
		f.t.Fatalf("edit changed collection shape: %#v", data)
	}
	for index, item := range items {
		f.checkItem(item, index)
	}
}

// The HTTP handlers and Agent editing/storage implementations are real, with
// local TCP gRPC. Only SQL/User/IM are substitutes; no Task/model/bot is involved.
func TestMultiDraftItemHTTPIndependentEditsPreserveEvidence(t *testing.T) {
	f := newMultiEditFlow(t)
	f.readAll()
	f.save(0, "", `{"title":"  修复缓存（确认）  ","description":" 人工说明 ","expected_revision":"1"}`, multiEditTextSQL, "修复缓存（确认）", "人工说明", func(d *agentpb.TaskDraftItem) {
		d.Title, d.Description = "修复缓存（确认）", "人工说明"
	})
	// Item 1 remains revision 1 while item 0 advances to revision 2.
	f.readAll()
	f.save(1, "/assignee", fmt.Sprintf(`{"assignee_id":"%d","expected_revision":"1"}`, multiEditTargetID), multiEditWhoSQL, multiEditTargetID, "selected", func(d *agentpb.TaskDraftItem) {
		d.AssigneeId, d.AssigneeResolution = multiEditTargetID, "selected"
	})
	f.save(1, "/assignee", fmt.Sprintf(`{"assignee_id":"%d","expected_revision":"2"}`, multiEditTargetID), "", nil, nil, nil)
	// Explicit zero resolves ambiguity. Merely reading the zero due did not.
	checks := f.member.checks.Load()
	f.save(1, "/deadline", `{"due_at_unix_ms":0,"expected_revision":"2"}`, multiEditDueSQL, int64(0), "unset", func(d *agentpb.TaskDraftItem) {
		d.Deadline.Resolution = "unset"
	})
	f.save(1, "/deadline", `{"due_at_unix_ms":0,"expected_revision":"3"}`, "", nil, nil, nil)
	f.save(1, "/assignee", `{"assignee_id":"0","expected_revision":"3"}`, multiEditWhoSQL, int64(0), "unassigned", func(d *agentpb.TaskDraftItem) {
		d.AssigneeId, d.AssigneeResolution = 0, "unassigned"
	})
	if f.member.checks.Load() != checks {
		t.Fatal("deadline editing or explicit unassignment queried a target member")
	}
	// Numerically identical parsed→selected and matched→selected are deliberate
	// human decisions and must each advance the target's revision.
	due := f.drafts[0].DueAtUnixMs
	f.save(0, "/deadline", fmt.Sprintf(`{"due_at_unix_ms":%d,"expected_revision":"2"}`, due), multiEditDueSQL, due, "selected", func(d *agentpb.TaskDraftItem) {
		d.Deadline.Resolution = "selected"
	})
	f.save(0, "/deadline", fmt.Sprintf(`{"due_at_unix_ms":%d,"expected_revision":"3"}`, due), "", nil, nil, nil)
	f.save(0, "/assignee", fmt.Sprintf(`{"assignee_id":"%d","expected_revision":"3"}`, multiPersistenceAssigneeID), multiEditWhoSQL, multiPersistenceAssigneeID, "selected", func(d *agentpb.TaskDraftItem) {
		d.AssigneeResolution = "selected"
	})
	f.save(0, "/assignee", fmt.Sprintf(`{"assignee_id":"%d","expected_revision":"4"}`, multiPersistenceAssigneeID), "", nil, nil, nil)
	f.save(0, "", `{"title":"修复缓存（确认）","description":"人工说明","expected_revision":"4"}`, "", nil, nil, nil)
	manualDue := time.Date(2026, 10, 8, 8, 30, 45, 123000000, time.UTC).UnixMilli()
	f.save(1, "/deadline", fmt.Sprintf(`{"due_at_unix_ms":%d,"expected_revision":"4"}`, manualDue), multiEditDueSQL, manualDue, "selected", func(d *agentpb.TaskDraftItem) {
		d.DueAtUnixMs, d.Deadline.Resolution = manualDue, "selected"
	})
	f.readAll()
	if f.drafts[0].Revision != 4 || f.drafts[1].Revision != 5 {
		t.Fatalf("item revisions are not independent: %d %d", f.drafts[0].Revision, f.drafts[1].Revision)
	}
}

func TestMultiDraftItemHTTPLockRejectsTargetChangesAllowsOtherItemChanges(t *testing.T) {
	for _, field := range []string{"revision", "description"} {
		t.Run("target "+field+" changed after authorization", func(t *testing.T) {
			f := newMultiEditFlow(t)
			f.expectRead()
			locked := multiEditClone(f.drafts)
			if field == "revision" {
				locked[0].Revision++
			} else {
				// Even a changed field with the same version must fail the complete
				// target comparison instead of overwriting storage corruption.
				locked[0].Description = "another saved description"
			}
			f.expectLock(locked)
			f.mock.ExpectRollback()
			f.request(http.MethodPut, f.path(0, ""), `{"title":"my edit","description":"","expected_revision":"1"}`, http.StatusConflict)
		})
	}
	t.Run("other item legitimately changed", func(t *testing.T) {
		f := newMultiEditFlow(t)
		f.expectRead()
		locked := multiEditClone(f.drafts)
		locked[1].Title, locked[1].Revision = "另一项已修改", 9007199254740993
		f.expectLock(locked)
		f.expectWrite(multiEditTextSQL, 0, "my edit", "", 1, 1)
		f.mock.ExpectCommit()
		f.drafts = locked
		f.drafts[0].Title, f.drafts[0].Description, f.drafts[0].Revision = "my edit", "", 2
		data := f.request(http.MethodPut, f.path(0, ""), `{"title":"my edit","description":"","expected_revision":"1"}`, http.StatusOK)["data"].(map[string]any)
		f.checkScope(data)
		f.checkItem(data["item"], 0)
		f.readAll() // Also verifies the other item's large version remains a string.
	})
	for _, affected := range []int64{0, 2} {
		t.Run(fmt.Sprintf("update affected %d rows", affected), func(t *testing.T) {
			f := newMultiEditFlow(t)
			f.expectRead()
			f.expectLock(f.drafts)
			f.expectWrite(multiEditTextSQL, 0, "my edit", "", 1, affected)
			f.mock.ExpectRollback()
			f.request(http.MethodPut, f.path(0, ""), `{"title":"my edit","description":"","expected_revision":"1"}`, http.StatusConflict)
		})
	}
}

func TestMultiDraftItemHTTPRejectsStaleMembershipAndWrongMode(t *testing.T) {
	for _, tc := range []struct{ name, suffix, body string }{
		{"text", "", `{"title":"new","description":"","expected_revision":"2"}`},
		{"assignee", "/assignee", fmt.Sprintf(`{"assignee_id":"%d","expected_revision":"2"}`, multiEditTargetID)},
		{"deadline", "/deadline", `{"due_at_unix_ms":0,"expected_revision":"2"}`},
	} {
		t.Run("stale "+tc.name, func(t *testing.T) {
			f := newMultiEditFlow(t)
			f.expectRead()
			f.request(http.MethodPut, f.path(0, tc.suffix), tc.body, http.StatusConflict)
			if f.member.checks.Load() != 0 {
				t.Fatal("stale edit consulted the target member")
			}
		})
	}
	t.Run("group qualification revoked", func(t *testing.T) {
		f := newMultiEditFlow(t)
		f.im.deny.Store(true)
		f.expectRead()
		f.request(http.MethodPut, f.path(1, "/assignee"), fmt.Sprintf(`{"assignee_id":"%d","expected_revision":"1"}`, multiEditTargetID), http.StatusForbidden)
		if f.member.checks.Load() != 0 {
			t.Fatal("group rejection continued to member validation")
		}
	})
	t.Run("target qualification revoked", func(t *testing.T) {
		f := newMultiEditFlow(t)
		f.member.revoked.Store(true)
		f.expectRead()
		f.request(http.MethodPut, f.path(1, "/assignee"), fmt.Sprintf(`{"assignee_id":"%d","expected_revision":"1"}`, multiEditTargetID), http.StatusForbidden)
		if f.member.checks.Load() != 1 {
			t.Fatal("positive assignee was not revalidated")
		}
	})
	t.Run("new item editing rejects single run", func(t *testing.T) {
		f := newMultiEditFlow(t)
		// Only mode matters: the collection loader must reject single before
		// interpreting its legacy status/metadata as an editable item.
		rows := sqlmock.NewRows([]string{"run_id", "draft_mode"}).AddRow(f.runID.Load(), "single")
		f.mock.ExpectQuery(multiPersistenceSelect).WithArgs(f.runID.Load(), int64(400)).WillReturnRows(rows)
		f.request(http.MethodPut, f.path(0, ""), `{"title":"new","description":"","expected_revision":"1"}`, http.StatusConflict)
	})
	t.Run("old single reading still rejects collection", func(t *testing.T) {
		f := newMultiEditFlow(t)
		marker := `CASE WHEN r.draft_mode = 'single' THEN r.status ELSE 'collection' END AS status`
		f.mock.ExpectQuery("SELECT .*"+regexp.QuoteMeta(marker)+".*FROM agent_runs AS r").WithArgs(f.runID.Load(), int64(400)).WillReturnRows(confirmFlowRows("collection", 0))
		f.request(http.MethodGet, fmt.Sprintf("/runs/%d/draft", f.runID.Load()), "", http.StatusConflict)
	})
}
