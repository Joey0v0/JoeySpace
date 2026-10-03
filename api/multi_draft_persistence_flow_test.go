package main

import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/bwmarrin/snowflake"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
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
	multiPersistenceInstruction = "李四明天下午整理文档"
	multiPersistenceSourceID    = int64(9007199254740993)
	multiPersistenceTimeID      = int64(9007199254740995)
	multiPersistenceAssigneeID  = int64(9007199254740997)
	multiPersistenceKey         = "collection-persistence-1"
	// LIMIT 6 is deliberate: a sixth persisted item must not disappear in a read.
	multiPersistenceSelect = `SELECT r\.id AS run_id, r\.draft_mode, r\.item_count, r\.status AS run_status.*FROM agent_runs AS r.*WHERE r\.id = \? AND r\.initiator_id = \?.*ORDER BY d\.item_index LIMIT 6`
)

type multiPersistenceUser struct {
	confirmFlowUser
	calls        atomic.Int32
	rejectSecond bool
}

func (s *multiPersistenceUser) ResolveTeamMember(ctx context.Context, req *userpb.ResolveTeamMemberRequest) (*userpb.ResolveTeamMemberResponse, error) {
	s.calls.Add(1)
	md, _ := metadata.FromIncomingContext(ctx)
	if auth := md.Get("authorization"); len(auth) != 1 || auth[0] != "Bearer user-token" || len(md.Get("idempotency-key")) != 0 || req.GetTeamId() != flowTeamID {
		return nil, status.Error(codes.InvalidArgument, "changed member lookup scope")
	}
	switch req.GetName() {
	case "张三":
		return &userpb.ResolveTeamMemberResponse{Candidates: []*userpb.TeamMember{{UserId: multiPersistenceAssigneeID, Nickname: "张三"}}}, nil
	case "李四":
		if s.rejectSecond {
			return nil, status.Error(codes.PermissionDenied, "private second member lookup rejection")
		}
		return &userpb.ResolveTeamMemberResponse{Candidates: []*userpb.TeamMember{
			{UserId: multiPersistenceAssigneeID + 2, Nickname: "李四"},
			{UserId: multiPersistenceAssigneeID + 4, Nickname: "李四"},
		}}, nil
	default:
		return nil, status.Error(codes.InvalidArgument, "invented member name")
	}
}

type multiPersistenceIM struct {
	confirmFlowIM
	messageReference int64
}

func (s *multiPersistenceIM) ListTeamGroupMessages(ctx context.Context, req *impb.ListTeamGroupMessagesRequest) (*impb.ListTeamGroupMessagesResponse, error) {
	if _, err := s.CheckTeamGroupAccess(ctx, &impb.CheckTeamGroupAccessRequest{TeamId: req.GetTeamId(), GroupId: req.GetGroupId()}); err != nil {
		return nil, err
	}
	md, _ := metadata.FromIncomingContext(ctx)
	if req.GetLimit() != 20 || len(md.Get("idempotency-key")) != 0 {
		return nil, status.Error(codes.InvalidArgument, "changed authorized message request")
	}
	return &impb.ListTeamGroupMessagesResponse{Messages: []*impb.TeamGroupMessage{
		{Id: multiPersistenceSourceID, ContentType: 1, Content: "张三修复缓存", CreatedAtUnixMs: s.messageReference - 86400000},
		{Id: multiPersistenceTimeID, ContentType: 1, Content: "在明天 15:30完成", CreatedAtUnixMs: s.messageReference},
	}}, nil
}

type multiPersistenceModel struct {
	output string
	calls  atomic.Int32
}

func (*multiPersistenceModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream unused")
}

func (s *multiPersistenceModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	s.calls.Add(1)
	if len(input) != 2 || !strings.Contains(input[0].Content, `{"drafts":[...]}`) || !strings.Contains(input[1].Content, multiPersistenceInstruction) || !strings.Contains(input[1].Content, "明天 15:30") || !strings.Contains(input[1].Content, strconv.FormatInt(multiPersistenceTimeID, 10)) {
		return nil, errors.New("missing collection prompt or authorized source context")
	}
	return schema.AssistantMessage(s.output, nil), nil
}

// Count before validation: even a malformed unexpected write RPC is evidence
// that a legacy operation crossed the collection isolation boundary.
type multiPersistenceTask struct {
	taskpb.UnimplementedTaskServer
	calls atomic.Int32
}

func (s *multiPersistenceTask) CreateTask(context.Context, *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
	s.calls.Add(1)
	return nil, status.Error(codes.Internal, "unexpected task creation")
}

type multiPersistenceBot struct {
	impb.UnimplementedIMBotServer
	calls atomic.Int32
}

func (s *multiPersistenceBot) PostTaskCreatedCard(context.Context, *impb.PostTaskCreatedCardRequest) (*impb.PostTaskCreatedCardResponse, error) {
	s.calls.Add(1)
	return nil, status.Error(codes.Internal, "unexpected bot post")
}

type multiPersistenceRunID struct{ value *atomic.Int64 }

func (m multiPersistenceRunID) Match(v driver.Value) bool {
	id, ok := v.(int64)
	return ok && id > 0 && id == m.value.Load()
}

type multiPersistenceFlow struct {
	t          *testing.T
	mock       sqlmock.Sqlmock
	runID      atomic.Int64
	reference  int64
	messageRef int64
	users      *multiPersistenceUser
	im         *multiPersistenceIM
	chat       *multiPersistenceModel
	tasks      *multiPersistenceTask
	bot        *multiPersistenceBot
	server     *httptest.Server
	httpClient *http.Client
	drafts     []*agentpb.TaskDraftItem
}

func newMultiPersistenceFlow(t *testing.T, badSource, deniedSecond bool) *multiPersistenceFlow {
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
	f := &multiPersistenceFlow{
		t: t, mock: mock, reference: time.Date(2026, 10, 6, 16, 0, 0, 123000000, time.UTC).UnixMilli(),
		messageRef: time.Date(2026, 10, 3, 4, 0, 0, 456000000, time.UTC).UnixMilli(),
		users:      &multiPersistenceUser{rejectSecond: deniedSecond}, tasks: &multiPersistenceTask{}, bot: &multiPersistenceBot{},
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
	f.im = &multiPersistenceIM{messageReference: f.messageRef}
	secondSource := multiPersistenceTimeID
	if badSource {
		secondSource = 99 // Syntactically valid, but absent from the authorized messages.
	}
	f.chat = &multiPersistenceModel{output: fmt.Sprintf(`{"drafts":[{"title":"修复缓存","description":"修复任务","source_message_id":"%d","assignee_name":"张三","deadline_text":"明天 15:30","deadline_source":"message","deadline_source_message_id":"%d"},{"title":"整理文档","description":"","source_message_id":"%d","assignee_name":"李四","deadline_text":"明天下午","deadline_source":"instruction","deadline_source_message_id":"0"}]}`, multiPersistenceSourceID, multiPersistenceTimeID, secondSource)}
	parsed := time.Date(2026, 10, 4, 7, 30, 0, 0, time.UTC).UnixMilli()
	f.drafts = []*agentpb.TaskDraftItem{
		{Title: "修复缓存", Description: "修复任务", SourceMessageId: multiPersistenceSourceID, AssigneeName: "张三", AssigneeResolution: "matched", AssigneeId: multiPersistenceAssigneeID, DueAtUnixMs: parsed, Revision: 1,
			Deadline: &agentpb.TaskDraftDeadline{Text: "明天 15:30", Source: "message", SourceMessageId: multiPersistenceTimeID, ReferenceUnixMs: f.messageRef, Timezone: "Asia/Shanghai", Resolution: "parsed", ParsedUnixMs: parsed, InstructionReferenceUnixMs: f.reference}},
		{Title: "整理文档", SourceMessageId: multiPersistenceTimeID, AssigneeName: "李四", AssigneeResolution: "ambiguous", Revision: 1,
			Deadline: &agentpb.TaskDraftDeadline{Text: "明天下午", Source: "instruction", ReferenceUnixMs: f.reference, Timezone: "Asia/Shanghai", Resolution: "needs_input", Reason: "unsupported_expression", InstructionReferenceUnixMs: f.reference}},
	}
	dial := func(addr string) *grpc.ClientConn {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	users := userpb.NewUserClient(dial(startFlowRPC(t, func(s *grpc.Server) { userpb.RegisterUserServer(s, f.users) })))
	ims := impb.NewIMClient(dial(startFlowRPC(t, func(s *grpc.Server) { impb.RegisterIMServer(s, f.im) })))
	tasks := taskpb.NewTaskClient(dial(startFlowRPC(t, func(s *grpc.Server) { taskpb.RegisterTaskServer(s, f.tasks) })))
	bot := impb.NewIMBotClient(dial(startFlowRPC(t, func(s *grpc.Server) { impb.RegisterIMBotServer(s, f.bot) })))
	generator, err := agent.NewEinoTaskDraftGenerator(context.Background(), f.chat)
	if err != nil {
		t.Fatal(err)
	}
	node, err := snowflake.NewNode(5)
	if err != nil {
		t.Fatal(err)
	}
	impl := agent.NewServer(nil)
	// These existing production configuration methods also wire the new RPCs.
	impl.ConfigureDraftPreparation(db, users, ims, generator, node)
	impl.ConfigureDraftAccess(db, users, ims)
	impl.ConfigureDraftConfirmation(db, tasks)
	impl.ConfigureDraftReplies(db, bot)
	client := agentpb.NewAgentClient(dial(startFlowRPC(t, func(s *grpc.Server) { agentpb.RegisterAgentServer(s, impl) })))
	mux := http.NewServeMux()
	wrap := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			h(w, pathvar.WithVars(r, map[string]string{"team_id": fmt.Sprint(flowTeamID), "group_id": fmt.Sprint(flowGroupID), "run_id": r.PathValue("run_id"), "item_index": r.PathValue("item_index")}))
		}
	}
	mux.HandleFunc("POST /collections", wrap(prepareTaskDraftCollectionHandler(client)))
	mux.HandleFunc("GET /runs/{run_id}/drafts", wrap(getTaskDraftCollectionHandler(client)))
	mux.HandleFunc("GET /runs/{run_id}/drafts/{item_index}", wrap(getTaskDraftItemHandler(client)))
	mux.HandleFunc("POST /single", wrap(prepareTaskDraftHandler(client)))
	mux.HandleFunc("GET /runs/{run_id}/draft", wrap(getTaskDraftHandler(client)))
	mux.HandleFunc("PUT /runs/{run_id}/draft", wrap(editTaskDraftHandler(client)))
	mux.HandleFunc("PUT /runs/{run_id}/deadline", wrap(editTaskDraftDeadlineHandler(client)))
	mux.HandleFunc("PUT /runs/{run_id}/assignee", wrap(selectTaskDraftAssigneeHandler(client)))
	mux.HandleFunc("POST /runs/{run_id}/confirm", wrap(confirmTaskDraftHandler(client)))
	mux.HandleFunc("POST /runs/{run_id}/reply/retry", wrap(retryTaskReplyHandler(client)))
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *multiPersistenceFlow) request(method, path, body string, want int) map[string]any {
	f.t.Helper()
	req, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer user-token")
	req.Header.Set("Idempotency-Key", multiPersistenceKey)
	req.Header.Set("Content-Type", "application/json")
	response, err := f.httpClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	if response.StatusCode != want || strings.Contains(string(data), "private ") {
		f.t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, response.StatusCode, want, data)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	var envelope map[string]any
	if err := decoder.Decode(&envelope); err != nil {
		f.t.Fatal(err)
	}
	if want != http.StatusOK {
		if envelope["data"] != nil {
			f.t.Fatalf("rejected operation exposed partial draft data: %s", data)
		}
	} else if envelope["code"] != json.Number("0") {
		f.t.Fatalf("successful HTTP response has an error code: %s", data)
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		f.t.Fatal(err)
	}
	return envelope
}

func (f *multiPersistenceFlow) body(reference int64) string {
	return fmt.Sprintf(`{"instruction":%q,"instruction_reference_unix_ms":%d}`, multiPersistenceInstruction, reference)
}

func (f *multiPersistenceFlow) fingerprint() string {
	identity := fmt.Sprintf(`{"team_id":%d,"group_id":%d,"instruction":%q,"mode":"collection","instruction_reference_unix_ms":%d}`, flowTeamID, flowGroupID, multiPersistenceInstruction, f.reference)
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])
}

func (f *multiPersistenceFlow) expectLookup(existing bool) {
	rows := sqlmock.NewRows([]string{"id", "team_id", "group_id", "request_fingerprint"})
	if existing {
		rows.AddRow(f.runID.Load(), flowTeamID, flowGroupID, f.fingerprint())
	}
	f.mock.ExpectQuery(regexp.QuoteMeta("SELECT id, team_id, group_id, request_fingerprint FROM agent_runs")).WithArgs(int64(400), multiPersistenceKey).WillReturnRows(rows)
}

func (f *multiPersistenceFlow) expectSave() {
	f.mock.ExpectBegin()
	f.mock.ExpectExec("INSERT INTO agent_runs").WithArgs(captureReferenceFlowRun{&f.runID}, flowTeamID, flowGroupID, int64(400), multiPersistenceKey, f.fingerprint(), "waiting_confirmation", "collection", int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
	for index, draft := range f.drafts {
		d := draft.Deadline
		f.mock.ExpectExec("INSERT INTO agent_task_drafts").WithArgs(multiPersistenceRunID{&f.runID}, int64(index), draft.Title, draft.Description, draft.AssigneeId, draft.DueAtUnixMs, draft.SourceMessageId, draft.AssigneeName, draft.AssigneeResolution,
			d.Text, d.Source, d.SourceMessageId, d.ReferenceUnixMs, d.Timezone, d.Resolution, d.Reason, d.ParsedUnixMs, d.InstructionReferenceUnixMs, "waiting_confirmation").WillReturnResult(sqlmock.NewResult(0, 1))
	}
	f.mock.ExpectCommit()
}

func (f *multiPersistenceFlow) collectionRows() *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"run_id", "draft_mode", "item_count", "run_status", "team_id", "group_id", "initiator_id", "item_index", "status", "title", "description", "assignee_id", "due_at_unix_ms", "source_message_id", "task_request_key", "task_id", "assignee_name", "assignee_resolution", "revision",
		"deadline_text", "deadline_source", "deadline_source_message_id", "deadline_reference_unix_ms", "deadline_timezone", "deadline_resolution", "deadline_reason", "deadline_parsed_unix_ms", "instruction_reference_unix_ms"})
	for index, draft := range f.drafts {
		d := draft.Deadline
		rows.AddRow(f.runID.Load(), "collection", 2, "waiting_confirmation", flowTeamID, flowGroupID, 400, index, "waiting_confirmation", draft.Title, draft.Description, draft.AssigneeId, draft.DueAtUnixMs, draft.SourceMessageId, "", 0, draft.AssigneeName, draft.AssigneeResolution, draft.Revision,
			d.Text, d.Source, d.SourceMessageId, d.ReferenceUnixMs, d.Timezone, d.Resolution, d.Reason, d.ParsedUnixMs, d.InstructionReferenceUnixMs)
	}
	return rows
}

func (f *multiPersistenceFlow) expectRead() {
	f.mock.ExpectQuery(multiPersistenceSelect).WithArgs(f.runID.Load(), int64(400)).WillReturnRows(f.collectionRows())
}

func (f *multiPersistenceFlow) checkScope(data map[string]any) {
	f.t.Helper()
	for field, want := range map[string]any{"run_id": fmt.Sprint(f.runID.Load()), "team_id": fmt.Sprint(flowTeamID), "group_id": fmt.Sprint(flowGroupID), "item_count": json.Number("2")} {
		if data[field] != want {
			f.t.Fatalf("collection scope %s=%#v want=%#v", field, data[field], want)
		}
	}
}

func (f *multiPersistenceFlow) checkItem(value any, index int) {
	f.t.Helper()
	item, ok := value.(map[string]any)
	if !ok {
		f.t.Fatalf("missing collection item %d: %#v", index, value)
	}
	if item["item_index"] != json.Number(strconv.Itoa(index)) || item["status"] != "waiting_confirmation" || item["task_id"] != "0" || (item["reply_status"] != "disabled" && item["reply_status"] != "not_started") || item["reply_msg_id"] != "" {
		f.t.Fatalf("item %d was omitted, frozen or posted: %#v", index, item)
	}
	draft, ok := item["draft"].(map[string]any)
	if !ok {
		f.t.Fatalf("missing complete draft %d", index)
	}
	expected := f.drafts[index]
	want := map[string]any{"title": expected.Title, "description": expected.Description, "assignee_id": fmt.Sprint(expected.AssigneeId), "assignee_name": expected.AssigneeName, "assignee_resolution": expected.AssigneeResolution, "source_message_id": fmt.Sprint(expected.SourceMessageId), "due_at_unix_ms": json.Number(fmt.Sprint(expected.DueAtUnixMs)), "revision": fmt.Sprint(expected.Revision)}
	for field, value := range want {
		if draft[field] != value {
			f.t.Fatalf("item %d persisted field %s=%#v want=%#v", index, field, draft[field], value)
		}
	}
	d := expected.Deadline
	wantDeadline := map[string]any{"text": d.Text, "source": d.Source, "source_message_id": fmt.Sprint(d.SourceMessageId), "reference_unix_ms": json.Number(fmt.Sprint(d.ReferenceUnixMs)), "timezone": d.Timezone, "resolution": d.Resolution, "reason": d.Reason, "parsed_unix_ms": json.Number(fmt.Sprint(d.ParsedUnixMs)), "instruction_reference_unix_ms": json.Number(fmt.Sprint(d.InstructionReferenceUnixMs))}
	if !reflect.DeepEqual(draft["deadline"], wantDeadline) {
		f.t.Fatalf("item %d lost independent nine-field time evidence: %#v want %#v", index, draft["deadline"], wantDeadline)
	}
}

// Gateway handlers and Agent are production implementations connected over
// real local TCP gRPC. SQL, Eino model, User and IM are substitutes; no real
// model, database migrations, Task writes or bot messages are used.
func TestMultiDraftCollectionHTTPPersistenceReadReplayAndLegacyIsolation(t *testing.T) {
	f := newMultiPersistenceFlow(t, false, false)
	f.expectLookup(false)
	f.expectSave()
	prepared := f.request(http.MethodPost, "/collections", f.body(f.reference), http.StatusOK)
	if f.runID.Load() <= 9007199254740991 || prepared["data"].(map[string]any)["run_id"] != fmt.Sprint(f.runID.Load()) || f.chat.calls.Load() != 1 || f.users.calls.Load() != 2 {
		t.Fatalf("collection was not generated and fully persisted once: %v model=%d members=%d", prepared, f.chat.calls.Load(), f.users.calls.Load())
	}
	path := fmt.Sprintf("/runs/%d", f.runID.Load())
	f.expectRead()
	collection := f.request(http.MethodGet, path+"/drafts", "", http.StatusOK)["data"].(map[string]any)
	f.checkScope(collection)
	items, ok := collection["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("read did not return the complete collection: %#v", collection)
	}
	for index := range items {
		f.checkItem(items[index], index)
		f.expectRead()
		one := f.request(http.MethodGet, fmt.Sprintf("%s/drafts/%d", path, index), "", http.StatusOK)["data"].(map[string]any)
		f.checkScope(one)
		f.checkItem(one["item"], index)
	}
	f.expectRead()
	f.request(http.MethodGet, path+"/drafts/2", "", http.StatusNotFound)

	f.expectLookup(true)
	replayed := f.request(http.MethodPost, "/collections", f.body(f.reference), http.StatusOK)
	if !reflect.DeepEqual(prepared, replayed) || f.chat.calls.Load() != 1 || f.users.calls.Load() != 2 {
		t.Fatalf("same-key replay regenerated/re-resolved: %#v model=%d members=%d", replayed, f.chat.calls.Load(), f.users.calls.Load())
	}
	f.expectLookup(true)
	f.request(http.MethodPost, "/collections", f.body(f.reference+1), http.StatusConflict)
	f.expectLookup(true)
	f.request(http.MethodPost, "/single", f.body(f.reference), http.StatusConflict)

	// The CASE marker is part of the production query, not just a forged status
	// from an RPC double. Every legacy operation must reject it before SQL writes.
	marker := `CASE WHEN r.draft_mode = 'single' THEN r.status ELSE 'collection' END AS status`
	for _, operation := range []struct{ method, suffix, body string }{
		{http.MethodGet, "/draft", ""},
		{http.MethodPut, "/draft", `{"title":"changed","description":"","expected_title":"修复缓存","expected_description":"修复任务","expected_revision":"1"}`},
		{http.MethodPut, "/deadline", `{"due_at_unix_ms":0,"expected_revision":"1"}`},
		{http.MethodPut, "/assignee", `{"assignee_id":"0","expected_revision":"1"}`},
		{http.MethodPost, "/confirm", fmt.Sprintf(`{"expected_title":"修复缓存","expected_description":"修复任务","expected_revision":"1","expected_assignee_id":"%d","expected_due_at_unix_ms":%d,"expected_deadline_resolution":"parsed"}`, multiPersistenceAssigneeID, f.drafts[0].DueAtUnixMs)},
		{http.MethodPost, "/reply/retry", ""},
	} {
		f.mock.ExpectQuery("SELECT .*"+regexp.QuoteMeta(marker)+".*FROM agent_runs AS r").WithArgs(f.runID.Load(), int64(400)).WillReturnRows(confirmFlowRows("collection", 0))
		f.request(operation.method, path+operation.suffix, operation.body, http.StatusConflict)
	}

	// Stored results do not override current membership, even on same-key replay.
	f.im.deny.Store(true)
	f.request(http.MethodPost, "/collections", f.body(f.reference), http.StatusForbidden)
	f.expectRead()
	f.request(http.MethodGet, path+"/drafts", "", http.StatusForbidden)
	f.expectRead()
	f.request(http.MethodGet, path+"/drafts/0", "", http.StatusForbidden)
	if f.chat.calls.Load() != 1 || f.users.calls.Load() != 2 || f.tasks.calls.Load() != 0 || f.bot.calls.Load() != 0 {
		t.Fatalf("read/replay/rejection caused effects: model=%d members=%d Task=%d bot=%d", f.chat.calls.Load(), f.users.calls.Load(), f.tasks.calls.Load(), f.bot.calls.Load())
	}
}

func TestMultiDraftCollectionInvalidLaterItemNeverPersistsPartialResult(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		badSource, rejectedMember bool
		status, memberCalls       int
	}{
		{"later source absent from authorized messages", true, false, http.StatusConflict, 1},
		{"later member lookup revoked", false, true, http.StatusForbidden, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMultiPersistenceFlow(t, tc.badSource, tc.rejectedMember)
			// The only allowed SQL is the pre-generation idempotency lookup. If a
			// first item is saved before validating the later item, sqlmock rejects
			// the unexpected transaction and the required 409/403 becomes 503.
			f.expectLookup(false)
			f.request(http.MethodPost, "/collections", f.body(f.reference), tc.status)
			if f.runID.Load() != 0 || f.chat.calls.Load() != 1 || f.users.calls.Load() != int32(tc.memberCalls) || f.tasks.calls.Load() != 0 || f.bot.calls.Load() != 0 {
				t.Fatalf("bad later item retained partial work: run=%d model=%d members=%d Task=%d bot=%d", f.runID.Load(), f.chat.calls.Load(), f.users.calls.Load(), f.tasks.calls.Load(), f.bot.calls.Load())
			}
		})
	}
}
