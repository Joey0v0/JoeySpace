package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	agent "github.com/yjydist/go-im/rpc/agent"
	agentpb "github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	multiConfirmFreezeSQL   = "UPDATE agent_task_drafts SET status = ?, task_request_key = ? WHERE run_id = ? AND item_index = ? AND revision = ? AND status = 'waiting_confirmation'"
	multiConfirmCompleteSQL = "UPDATE agent_task_drafts SET status = ?, task_id = ? WHERE run_id = ? AND item_index = ? AND revision = ? AND status = 'creating' AND task_request_key = ?"
)

type multiConfirmTask struct {
	taskpb.UnimplementedTaskServer
	mu     sync.Mutex
	runID  int64
	drafts []*agentpb.TaskDraftItem
	code   codes.Code
	calls  [2]int
	total  int
}

func (s *multiConfirmTask) plan(code codes.Code, drafts []*agentpb.TaskDraftItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.code, s.drafts = code, multiEditClone(drafts)
}

func (s *multiConfirmTask) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

func (s *multiConfirmTask) CreateTask(ctx context.Context, req *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.total++
	md, _ := metadata.FromIncomingContext(ctx)
	keys, auth := md.Get("idempotency-key"), md.Get("authorization")
	index := -1
	for i := range s.drafts {
		if len(keys) == 1 && keys[0] == fmt.Sprintf("agent-task-%d-%d", s.runID, i) {
			index = i
		}
	}
	if index < 0 {
		return nil, status.Error(codes.InvalidArgument, "unexpected item creation key")
	}
	s.calls[index]++
	d := s.drafts[index]
	groupID := int64(0)
	if d.SourceMessageId != 0 {
		groupID = flowGroupID
	}
	if len(auth) != 1 || auth[0] != "Bearer user-token" || req.GetTeamId() != flowTeamID || req.GetTitle() != d.Title || req.GetDescription() != d.Description || req.GetAssigneeId() != d.AssigneeId || req.GetDueAtUnixMs() != d.DueAtUnixMs || req.GetSourceMessageId() != d.SourceMessageId || req.GetSourceGroupId() != groupID {
		return nil, status.Error(codes.InvalidArgument, "changed frozen item content or identity")
	}
	if s.code != codes.OK {
		return nil, status.Error(s.code, "private uncertain task response")
	}
	// The same key represents one Task result even if an earlier response or
	// Agent result save failed. Distinct item keys yield distinct precise IDs.
	return &taskpb.CreateTaskResponse{TaskId: int64(9007199254741201 + index)}, nil
}

type multiConfirmFlow struct {
	*multiEditFlow
	states [2]string
	keys   [2]string
	ids    [2]int64
	task   *multiConfirmTask
}

func newMultiConfirmFlow(t *testing.T) *multiConfirmFlow {
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
	f := &multiConfirmFlow{multiEditFlow: &multiEditFlow{multiPersistenceFlow: &multiPersistenceFlow{t: t, mock: mock, im: &multiPersistenceIM{}, httpClient: &http.Client{Timeout: 5 * time.Second}}, member: &multiEditUser{}}, states: [2]string{"waiting_confirmation", "waiting_confirmation"}}
	f.runID.Store(9007199254741011)
	parsed := time.Date(2026, 10, 4, 7, 30, 0, 0, time.UTC).UnixMilli()
	reference := time.Date(2026, 10, 6, 16, 0, 0, 123000000, time.UTC).UnixMilli()
	f.drafts = []*agentpb.TaskDraftItem{
		{Title: "修复缓存", Description: "修复任务", SourceMessageId: multiPersistenceSourceID, AssigneeName: "张三", AssigneeResolution: "matched", AssigneeId: multiPersistenceAssigneeID, DueAtUnixMs: parsed, Revision: 1,
			Deadline: &agentpb.TaskDraftDeadline{Text: "明天 15:30", Source: "message", SourceMessageId: multiPersistenceTimeID, ReferenceUnixMs: time.Date(2026, 10, 3, 4, 0, 0, 456000000, time.UTC).UnixMilli(), Timezone: "Asia/Shanghai", Resolution: "parsed", ParsedUnixMs: parsed, InstructionReferenceUnixMs: reference}},
		{Title: "整理文档", AssigneeName: "李四", AssigneeResolution: "unassigned", Revision: 3,
			Deadline: &agentpb.TaskDraftDeadline{Text: "明天下午", Source: "instruction", ReferenceUnixMs: reference, Timezone: "Asia/Shanghai", Resolution: "unset", Reason: "unsupported_expression", InstructionReferenceUnixMs: reference}},
	}
	f.task = &multiConfirmTask{runID: f.runID.Load()}
	f.task.plan(codes.OK, f.drafts)
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
	tasks := taskpb.NewTaskClient(dial(startFlowRPC(t, func(s *grpc.Server) { taskpb.RegisterTaskServer(s, f.task) })))
	impl := agent.NewServer(nil)
	impl.ConfigureDraftAccess(db, users, ims)
	impl.ConfigureDraftConfirmation(db, tasks)
	client := agentpb.NewAgentClient(dial(startFlowRPC(t, func(s *grpc.Server) { agentpb.RegisterAgentServer(s, impl) })))
	mux := http.NewServeMux()
	wrap := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			h(w, pathvar.WithVars(r, map[string]string{"run_id": r.PathValue("run_id"), "item_index": r.PathValue("item_index")}))
		}
	}
	mux.HandleFunc("POST /runs/{run_id}/drafts/{item_index}/confirm", wrap(confirmTaskDraftItemHandler(client)))
	mux.HandleFunc("GET /runs/{run_id}/drafts", wrap(getTaskDraftCollectionHandler(client)))
	mux.HandleFunc("GET /runs/{run_id}/drafts/{item_index}", wrap(getTaskDraftItemHandler(client)))
	mux.HandleFunc("PUT /runs/{run_id}/drafts/{item_index}", wrap(editTaskDraftItemTextHandler(client)))
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *multiConfirmFlow) rows() *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"run_id", "draft_mode", "item_count", "run_status", "team_id", "group_id", "initiator_id", "item_index", "status", "title", "description", "assignee_id", "due_at_unix_ms", "source_message_id", "task_request_key", "task_id", "assignee_name", "assignee_resolution", "revision", "deadline_text", "deadline_source", "deadline_source_message_id", "deadline_reference_unix_ms", "deadline_timezone", "deadline_resolution", "deadline_reason", "deadline_parsed_unix_ms", "instruction_reference_unix_ms"})
	for index, draft := range f.drafts {
		d := draft.Deadline
		rows.AddRow(f.runID.Load(), "collection", 2, "waiting_confirmation", flowTeamID, flowGroupID, 400, index, f.states[index], draft.Title, draft.Description, draft.AssigneeId, draft.DueAtUnixMs, draft.SourceMessageId, f.keys[index], f.ids[index], draft.AssigneeName, draft.AssigneeResolution, draft.Revision,
			d.Text, d.Source, d.SourceMessageId, d.ReferenceUnixMs, d.Timezone, d.Resolution, d.Reason, d.ParsedUnixMs, d.InstructionReferenceUnixMs)
	}
	return rows
}

func (f *multiConfirmFlow) expectRead() {
	f.mock.ExpectQuery(multiPersistenceSelect).WithArgs(f.runID.Load(), int64(400)).WillReturnRows(f.rows())
}

func (f *multiConfirmFlow) expectLock() {
	f.mock.ExpectBegin()
	f.mock.ExpectQuery(multiPersistenceSelect+" FOR UPDATE$").WithArgs(f.runID.Load(), int64(400)).WillReturnRows(f.rows())
}

func (f *multiConfirmFlow) expectFreeze(index int) {
	f.expectLock()
	if f.states[index] == "waiting_confirmation" {
		key := fmt.Sprintf("agent-task-%d-%d", f.runID.Load(), index)
		f.mock.ExpectExec(regexp.QuoteMeta(multiConfirmFreezeSQL)+"$").WithArgs("creating", key, f.runID.Load(), int64(index), f.drafts[index].Revision).WillReturnResult(sqlmock.NewResult(0, 1))
		f.states[index], f.keys[index] = "creating", key
	}
	f.mock.ExpectCommit()
}

func (f *multiConfirmFlow) expectComplete(index int, failure bool) {
	f.expectLock()
	id := int64(9007199254741201 + index)
	expect := f.mock.ExpectExec(regexp.QuoteMeta(multiConfirmCompleteSQL)+"$").WithArgs("succeeded", id, f.runID.Load(), int64(index), f.drafts[index].Revision, f.keys[index])
	if failure {
		expect.WillReturnError(errors.New("private result save failed"))
		f.mock.ExpectRollback()
	} else {
		expect.WillReturnResult(sqlmock.NewResult(0, 1))
		f.mock.ExpectCommit()
		f.states[index], f.ids[index] = "succeeded", id
	}
}

func (f *multiConfirmFlow) body(index int, revision int64) string {
	d := f.drafts[index]
	return fmt.Sprintf(`{"expected_title":%q,"expected_description":%q,"expected_revision":"%d","expected_assignee_id":"%d","expected_due_at_unix_ms":%d,"expected_deadline_resolution":%q}`, d.Title, d.Description, revision, d.AssigneeId, d.DueAtUnixMs, d.Deadline.Resolution)
}

func (f *multiConfirmFlow) checkItem(value any, index int) {
	f.t.Helper()
	item, ok := value.(map[string]any)
	if !ok || item["status"] != f.states[index] || item["task_id"] != fmt.Sprint(f.ids[index]) {
		f.t.Fatalf("wrong independent item state/result: %#v", value)
	}
	// Reuse the complete nine-field/precision/evidence assertion after checking
	// the actual status/result above. The original helper is waiting-only.
	content := make(map[string]any, len(item))
	for key, value := range item {
		content[key] = value
	}
	content["status"], content["task_id"] = "waiting_confirmation", "0"
	f.multiPersistenceFlow.checkItem(content, index)
}

func (f *multiConfirmFlow) confirm(index, want int) {
	f.t.Helper()
	data := f.request(http.MethodPost, f.path(index, "/confirm"), f.body(index, f.drafts[index].Revision), want)
	if want == http.StatusOK {
		result := data["data"].(map[string]any)
		f.checkScope(result)
		f.checkItem(result["item"], index)
	}
}

func (f *multiConfirmFlow) readAll() {
	f.t.Helper()
	f.expectRead()
	data := f.request(http.MethodGet, fmt.Sprintf("/runs/%d/drafts", f.runID.Load()), "", http.StatusOK)["data"].(map[string]any)
	f.checkScope(data)
	items := data["items"].([]any)
	if len(items) != 2 {
		f.t.Fatal("partial collection returned")
	}
	for index, item := range items {
		f.checkItem(item, index)
	}
}

// Real Gateway/Agent HTTP/TCP gRPC with SQL and Task/User/IM substitutes. No
// model, real Task database, migration or bot posting is used in this flow.
func TestMultiDraftItemHTTPFreezeRetryAndIndependentResults(t *testing.T) {
	f := newMultiConfirmFlow(t)
	f.task.plan(codes.DeadlineExceeded, f.drafts)
	f.expectRead()
	f.expectFreeze(0)
	f.confirm(0, http.StatusGatewayTimeout)
	if f.task.count() != 1 {
		t.Fatal("Task was not called once after freezing")
	}
	f.readAll() // creating item 0 and waiting item 1 coexist.
	f.expectRead()
	item := f.request(http.MethodGet, f.path(0, ""), "", http.StatusOK)["data"].(map[string]any)
	f.checkItem(item["item"], 0)
	f.expectRead()
	f.request(http.MethodPut, f.path(0, ""), `{"title":"cannot edit frozen","description":"","expected_revision":"1"}`, http.StatusConflict)

	// Another waiting item remains editable while item 0 is frozen. Its version
	// advances independently and its original deadline/mention evidence stays.
	f.expectRead()
	f.expectLock()
	f.expectWrite(multiEditTextSQL, 1, "确认文档", "可独立编辑", 3, 1)
	f.mock.ExpectCommit()
	f.drafts[1].Title, f.drafts[1].Description, f.drafts[1].Revision = "确认文档", "可独立编辑", 4
	result := f.request(http.MethodPut, f.path(1, ""), `{"title":"确认文档","description":"可独立编辑","expected_revision":"3"}`, http.StatusOK)["data"].(map[string]any)
	f.checkItem(result["item"], 1)
	f.readAll()

	f.im.deny.Store(true)
	f.expectRead()
	f.confirm(0, http.StatusForbidden)
	if f.task.count() != 1 {
		t.Fatal("revoked access continued creating a task")
	}
	f.im.deny.Store(false)
	f.task.plan(codes.Unavailable, f.drafts)
	f.expectRead()
	f.expectFreeze(0) // Same creating snapshot: no second freeze UPDATE.
	f.confirm(0, http.StatusServiceUnavailable)
	f.readAll()
	f.task.plan(codes.OK, f.drafts)
	f.expectRead()
	f.expectFreeze(0)
	f.expectComplete(0, false)
	f.confirm(0, http.StatusOK)
	f.expectRead()
	f.expectFreeze(1)
	f.expectComplete(1, false)
	f.confirm(1, http.StatusOK)
	f.readAll()
	if f.keys[0] == f.keys[1] || f.task.count() != 4 || f.member.checks.Load() != 1 || f.drafts[0].Revision != 1 || f.drafts[1].Revision != 4 {
		t.Fatalf("keys/versions/member checks leaked between items: keys=%v calls=%d checks=%d", f.keys, f.task.count(), f.member.checks.Load())
	}
	for index := range f.drafts {
		f.expectRead()
		f.expectFreeze(index) // A succeeded replay still locks and verifies.
		f.confirm(index, http.StatusOK)
	}
	if f.task.count() != 4 {
		t.Fatal("succeeded replay called Task again")
	}
}

func TestMultiDraftItemHTTPFreezeFailuresNeverCallTask(t *testing.T) {
	for _, mode := range []string{"freeze SQL error", "freeze no affected row", "stale revision", "group revoked", "assignee revoked", "deadline unresolved"} {
		t.Run(mode, func(t *testing.T) {
			f := newMultiConfirmFlow(t)
			index, want, revision := 0, http.StatusConflict, int64(1)
			if mode == "deadline unresolved" {
				index, revision = 1, 3
				f.drafts[1].Deadline.Resolution = "needs_input"
			}
			if mode == "group revoked" {
				f.im.deny.Store(true)
				want = http.StatusForbidden
			}
			if mode == "assignee revoked" {
				f.member.revoked.Store(true)
				want = http.StatusForbidden
			}
			if mode == "stale revision" {
				revision++
			}
			f.expectRead()
			if mode == "freeze SQL error" || mode == "freeze no affected row" {
				f.expectLock()
				expect := f.mock.ExpectExec(regexp.QuoteMeta(multiConfirmFreezeSQL)+"$").WithArgs("creating", fmt.Sprintf("agent-task-%d-0", f.runID.Load()), f.runID.Load(), int64(0), int64(1))
				if mode == "freeze SQL error" {
					expect.WillReturnError(errors.New("private freeze failed"))
					want = http.StatusServiceUnavailable
				} else {
					expect.WillReturnResult(sqlmock.NewResult(0, 0))
				}
				f.mock.ExpectRollback()
			}
			f.request(http.MethodPost, f.path(index, "/confirm"), f.body(index, revision), want)
			if f.task.count() != 0 {
				t.Fatal("rejected or uncommitted confirmation reached Task")
			}
		})
	}
}

func TestMultiDraftItemHTTPResultSaveFailureRecoversSameFrozenKey(t *testing.T) {
	f := newMultiConfirmFlow(t)
	f.expectRead()
	f.expectFreeze(0)
	f.expectComplete(0, true)
	f.confirm(0, http.StatusServiceUnavailable)
	f.readAll() // No success is claimed even though Task returned an ID.
	if f.states[0] != "creating" || f.ids[0] != 0 || f.task.count() != 1 {
		t.Fatal("lost result save fabricated success")
	}
	key := f.keys[0]
	f.expectRead()
	f.expectFreeze(0)
	f.expectComplete(0, false)
	f.confirm(0, http.StatusOK)
	if f.keys[0] != key || f.task.count() != 2 || f.drafts[0].Revision != 1 {
		t.Fatal("explicit recovery changed frozen identity")
	}
	f.expectRead()
	f.expectFreeze(0)
	f.confirm(0, http.StatusOK)
	f.im.deny.Store(true)
	f.expectRead()
	f.confirm(0, http.StatusForbidden)
	if f.task.count() != 2 {
		t.Fatal("succeeded/revoked replay called Task")
	}
}
