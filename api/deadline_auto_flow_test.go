package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
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

const autoDeadlineMessageID int64 = 9007199254740993

type autoDeadlineModel struct {
	output string
	calls  atomic.Int32
}

func (*autoDeadlineModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream unused")
}
func (m *autoDeadlineModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if len(input) != 2 || !strings.Contains(input[1].Content, "明天 15:30") {
		return nil, errors.New("missing authorized context")
	}
	m.calls.Add(1)
	return schema.AssistantMessage(m.output, nil), nil
}

type autoDeadlineIM struct {
	confirmFlowIM
	messageReference int64
}

func (s *autoDeadlineIM) ListTeamGroupMessages(ctx context.Context, req *impb.ListTeamGroupMessagesRequest) (*impb.ListTeamGroupMessagesResponse, error) {
	if _, err := s.CheckTeamGroupAccess(ctx, &impb.CheckTeamGroupAccessRequest{TeamId: req.GetTeamId(), GroupId: req.GetGroupId()}); err != nil {
		return nil, err
	}
	return &impb.ListTeamGroupMessagesResponse{Messages: []*impb.TeamGroupMessage{
		{Id: 600, ContentType: 1, Content: "修复缓存", CreatedAtUnixMs: s.messageReference},
		{Id: autoDeadlineMessageID, ContentType: 1, Content: "在明天 15:30完成", CreatedAtUnixMs: s.messageReference},
	}}, nil
}

type autoDeadlineTask struct {
	taskpb.UnimplementedTaskServer
	runID *atomic.Int64
	due   int64
	calls atomic.Int32
}

func (s *autoDeadlineTask) CreateTask(ctx context.Context, req *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if auth, key := md.Get("authorization"), md.Get("idempotency-key"); len(auth) != 1 || auth[0] != "Bearer user-token" || len(key) != 1 || key[0] != fmt.Sprintf("agent-task-%d-0", s.runID.Load()) ||
		req.GetDueAtUnixMs() != s.due || req.GetTeamId() != flowTeamID || req.GetSourceGroupId() != flowGroupID || req.GetSourceMessageId() != 600 || req.GetTitle() != "修复缓存" || req.GetAssigneeId() != 0 || req.GetDescription() != "" {
		return nil, status.Error(codes.InvalidArgument, "changed frozen task")
	}
	if s.calls.Add(1) == 1 {
		return nil, status.Error(codes.DeadlineExceeded, "private lost create response")
	}
	return &taskpb.CreateTaskResponse{TaskId: 88}, nil
}
func autoDeadlineRows(state string, due, revision, taskID int64, d *agentpb.TaskDraftDeadline, key string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"team_id", "group_id", "initiator_id", "status", "title", "description", "assignee_id", "due_at_unix_ms", "source_message_id", "task_request_key", "task_id", "assignee_name", "assignee_resolution", "revision",
		"deadline_text", "deadline_source", "deadline_source_message_id", "deadline_reference_unix_ms", "deadline_timezone", "deadline_resolution", "deadline_reason", "deadline_parsed_unix_ms", "instruction_reference_unix_ms"}).
		AddRow(flowTeamID, flowGroupID, 400, state, "修复缓存", "", 0, due, 600, key, taskID, "", "none", revision,
			d.Text, d.Source, d.SourceMessageId, d.ReferenceUnixMs, d.Timezone, d.Resolution, d.Reason, d.ParsedUnixMs, d.InstructionReferenceUnixMs)
}

// HTTP and TCP gRPC use production Agent generation/store/confirmation and Eino.
// SQL, model, User/IM/Task are substitutes; no real migrations are executed.
func TestAutomaticDeadlineHTTPPersistenceReviewAndFrozenRetry(t *testing.T) {
	for _, tc := range []struct {
		name, text, source, state, reason string
		sourceID                          int64
		originalDue, editedDue            int64
	}{
		{"message parsed then manual override", "明天 15:30", "message", "parsed", "", autoDeadlineMessageID, time.Date(2026, 10, 2, 7, 30, 0, 0, time.UTC).UnixMilli(), time.Date(2026, 10, 4, 8, 30, 0, 123000000, time.UTC).UnixMilli()},
		{"ambiguous instruction explicitly unset", "明天下午", "instruction", "needs_input", "unsupported_expression", 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sqlDB, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { sqlDB.Close() })
			db, err := gorm.Open(gormmysql.New(gormmysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			instructionRef := time.Date(2026, 10, 3, 15, 59, 59, 123000000, time.UTC).UnixMilli()
			messageRef := time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC).UnixMilli()
			const instruction = "明天下午完成缓存修复"
			var runID atomic.Int64
			im := &autoDeadlineIM{messageReference: messageRef}
			tasks := &autoDeadlineTask{runID: &runID, due: tc.editedDue}
			chat := &autoDeadlineModel{output: fmt.Sprintf(`{"title":"修复缓存","description":"","source_message_id":"600","assignee_name":"","deadline_text":%q,"deadline_source":%q,"deadline_source_message_id":"%d"}`, tc.text, tc.source, tc.sourceID)}
			userAddr := startFlowRPC(t, func(s *grpc.Server) { userpb.RegisterUserServer(s, &confirmFlowUser{}) })
			imAddr := startFlowRPC(t, func(s *grpc.Server) { impb.RegisterIMServer(s, im) })
			taskAddr := startFlowRPC(t, func(s *grpc.Server) { taskpb.RegisterTaskServer(s, tasks) })
			dial := func(addr string) *grpc.ClientConn {
				conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { conn.Close() })
				return conn
			}
			generator, err := agent.NewEinoTaskDraftGenerator(context.Background(), chat)
			if err != nil {
				t.Fatal(err)
			}
			node, err := snowflake.NewNode(5)
			if err != nil {
				t.Fatal(err)
			}
			impl := agent.NewServer(nil)
			users := userpb.NewUserClient(dial(userAddr))
			ims := impb.NewIMClient(dial(imAddr))
			impl.ConfigureDraftPreparation(db, users, ims, generator, node)
			impl.ConfigureDraftAccess(db, users, ims)
			impl.ConfigureDraftConfirmation(db, taskpb.NewTaskClient(dial(taskAddr)))
			addr := startFlowRPC(t, func(s *grpc.Server) { agentpb.RegisterAgentServer(s, impl) })
			client := agentpb.NewAgentClient(dial(addr))
			mux := http.NewServeMux()
			wrap := func(h http.HandlerFunc) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					h(w, pathvar.WithVars(r, map[string]string{"run_id": r.PathValue("run_id"), "team_id": fmt.Sprint(flowTeamID), "group_id": fmt.Sprint(flowGroupID)}))
				}
			}
			mux.HandleFunc("POST /prepare", wrap(prepareTaskDraftHandler(client)))
			mux.HandleFunc("GET /runs/{run_id}/draft", wrap(getTaskDraftHandler(client)))
			mux.HandleFunc("PUT /runs/{run_id}/deadline", wrap(editTaskDraftDeadlineHandler(client)))
			mux.HandleFunc("POST /runs/{run_id}/confirm", wrap(confirmTaskDraftHandler(client)))
			server := httptest.NewServer(mux)
			t.Cleanup(server.Close)
			httpClient := &http.Client{Timeout: 5 * time.Second}
			request := func(method, path, body string, want int) agentDraftResponse {
				t.Helper()
				req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer user-token")
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Idempotency-Key", "auto-deadline-1")
				resp, err := httpClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				data, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != want || strings.Contains(string(data), "private ") {
					t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, data)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
				var result agentDraftResponse
				if err := json.Unmarshal(data, &result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			meta := &agentpb.TaskDraftDeadline{Text: tc.text, Source: tc.source, SourceMessageId: tc.sourceID, ReferenceUnixMs: instructionRef, Timezone: "Asia/Shanghai", Resolution: tc.state, Reason: tc.reason, ParsedUnixMs: tc.originalDue, InstructionReferenceUnixMs: instructionRef}
			if tc.source == "message" {
				meta.ReferenceUnixMs = messageRef
			}
			body := fmt.Sprintf(`{"instruction":%q,"instruction_reference_unix_ms":%d}`, instruction, instructionRef)
			identity := fmt.Sprintf(`{"team_id":%d,"group_id":%d,"instruction":%q,"instruction_reference_unix_ms":%d}`, flowTeamID, flowGroupID, instruction, instructionRef)
			sum := sha256.Sum256([]byte(identity))
			fingerprint := hex.EncodeToString(sum[:])
			lookup := regexp.QuoteMeta("SELECT id, team_id, group_id, request_fingerprint FROM agent_runs")
			mock.ExpectQuery(lookup).WithArgs(int64(400), "auto-deadline-1").WillReturnRows(sqlmock.NewRows([]string{"id", "team_id", "group_id", "request_fingerprint"}))
			mock.ExpectBegin()
			mock.ExpectExec("INSERT INTO agent_runs").WithArgs(captureReferenceFlowRun{&runID}, flowTeamID, flowGroupID, int64(400), "auto-deadline-1", fingerprint, "waiting_confirmation").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("INSERT INTO agent_task_drafts").WithArgs(sqlmock.AnyArg(), "修复缓存", "", int64(0), tc.originalDue, int64(600), "", "none", meta.Text, meta.Source, meta.SourceMessageId, meta.ReferenceUnixMs, meta.Timezone, meta.Resolution, meta.Reason, meta.ParsedUnixMs, meta.InstructionReferenceUnixMs).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			prepared := request(http.MethodPost, "/prepare", body, 200)
			if prepared.Data == nil || prepared.Data.RunID != runID.Load() {
				t.Fatal("wrong prepared run")
			}
			path := fmt.Sprintf("/runs/%d/", runID.Load())
			expectRead := func(state string, due, revision, taskID int64, key string) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT r.team_id")).WithArgs(runID.Load(), int64(400)).WillReturnRows(autoDeadlineRows(state, due, revision, taskID, meta, key))
			}
			expectLock := func(state string, due, revision, taskID int64, key string) {
				mock.ExpectBegin()
				mock.ExpectQuery(`SELECT r\.team_id[\s\S]*FOR UPDATE`).WithArgs(runID.Load(), int64(400)).WillReturnRows(autoDeadlineRows(state, due, revision, taskID, meta, key))
			}
			checkMetadata := func(result agentDraftResponse, wantState string, due int64) {
				t.Helper()
				wire, _ := json.Marshal(result)
				var decoded struct {
					Data struct {
						Draft struct {
							Due      int64 `json:"due_at_unix_ms"`
							Deadline struct {
								Text, Source, Timezone, Resolution, Reason string
								SourceID                                   string `json:"source_message_id"`
								Reference                                  int64  `json:"reference_unix_ms"`
								Parsed                                     int64  `json:"parsed_unix_ms"`
								Instruction                                int64  `json:"instruction_reference_unix_ms"`
							} `json:"deadline"`
						} `json:"draft"`
					} `json:"data"`
				}
				if err := json.Unmarshal(wire, &decoded); err != nil {
					t.Fatal(err)
				}
				d := decoded.Data.Draft.Deadline
				if decoded.Data.Draft.Due != due || d.Text != tc.text || d.Source != tc.source || d.Timezone != "Asia/Shanghai" || d.Resolution != wantState || d.SourceID != fmt.Sprint(tc.sourceID) || d.Reference != meta.ReferenceUnixMs || d.Parsed != tc.originalDue || d.Instruction != instructionRef || d.Reason != tc.reason {
					t.Fatalf("changed evidence: %s", wire)
				}
			}
			expectRead("waiting_confirmation", tc.originalDue, 1, 0, "")
			checkMetadata(request(http.MethodGet, path+"draft", "", 200), tc.state, tc.originalDue)
			mock.ExpectQuery(lookup).WithArgs(int64(400), "auto-deadline-1").WillReturnRows(sqlmock.NewRows([]string{"id", "team_id", "group_id", "request_fingerprint"}).AddRow(runID.Load(), flowTeamID, flowGroupID, fingerprint))
			request(http.MethodPost, "/prepare", body, 200)
			if chat.calls.Load() != 1 {
				t.Fatal("replay called model")
			}
			confirmBody := func(revision int64, due int64, resolution string) string {
				return fmt.Sprintf(`{"expected_title":"修复缓存","expected_description":"","expected_revision":"%d","expected_assignee_id":"0","expected_due_at_unix_ms":%d,"expected_deadline_resolution":%q}`, revision, due, resolution)
			}
			expectRead("waiting_confirmation", tc.originalDue, 1, 0, "")
			request(http.MethodPost, path+"confirm", confirmBody(1, tc.originalDue, ""), 409)
			if tc.state == "needs_input" {
				expectRead("waiting_confirmation", 0, 1, 0, "")
				request(http.MethodPost, path+"confirm", confirmBody(1, 0, tc.state), 409)
			}
			if tasks.calls.Load() != 0 {
				t.Fatal("unreviewed or ambiguous deadline created a task")
			}
			selected := "selected"
			if tc.editedDue == 0 {
				selected = "unset"
			}
			expectRead("waiting_confirmation", tc.originalDue, 1, 0, "")
			expectLock("waiting_confirmation", tc.originalDue, 1, 0, "")
			mock.ExpectExec("UPDATE agent_task_drafts SET due_at_unix_ms").WithArgs(tc.editedDue, selected, runID.Load()).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			checkMetadata(request(http.MethodPut, path+"deadline", fmt.Sprintf(`{"due_at_unix_ms":%d,"expected_revision":"1"}`, tc.editedDue), 200), selected, tc.editedDue)
			meta.Resolution = selected
			expectRead("waiting_confirmation", tc.editedDue, 2, 0, "")
			request(http.MethodPut, path+"deadline", `{"due_at_unix_ms":0,"expected_revision":"1"}`, 409)
			key := fmt.Sprintf("agent-task-%d-0", runID.Load())
			confirm := confirmBody(2, tc.editedDue, selected)
			expectRead("waiting_confirmation", tc.editedDue, 2, 0, "")
			expectLock("waiting_confirmation", tc.editedDue, 2, 0, "")
			mock.ExpectExec("UPDATE agent_task_drafts SET task_request_key").WithArgs(key, runID.Load()).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("UPDATE agent_runs SET status").WithArgs("creating", runID.Load(), int64(400)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			request(http.MethodPost, path+"confirm", confirm, 504)
			expectRead("creating", tc.editedDue, 2, 0, key)
			checkMetadata(request(http.MethodGet, path+"draft", "", 200), selected, tc.editedDue)
			expectRead("creating", tc.editedDue, 2, 0, key)
			request(http.MethodPut, path+"deadline", `{"due_at_unix_ms":0,"expected_revision":"2"}`, 409)
			expectRead("creating", tc.editedDue, 2, 0, key)
			expectLock("creating", tc.editedDue, 2, 0, key)
			mock.ExpectCommit()
			expectLock("creating", tc.editedDue, 2, 0, key)
			mock.ExpectExec("UPDATE agent_task_drafts SET task_id").WithArgs(int64(88), runID.Load()).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("UPDATE agent_runs SET status").WithArgs("succeeded", runID.Load(), int64(400)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			checkMetadata(request(http.MethodPost, path+"confirm", confirm, 200), selected, tc.editedDue)
			expectRead("succeeded", tc.editedDue, 2, 88, key)
			expectLock("succeeded", tc.editedDue, 2, 88, key)
			mock.ExpectCommit()
			request(http.MethodPost, path+"confirm", confirm, 200)
			if tasks.calls.Load() != 2 {
				t.Fatal("successful replay created a second task")
			}
			im.deny.Store(true)
			expectRead("succeeded", tc.editedDue, 2, 88, key)
			request(http.MethodGet, path+"draft", "", 403)
		})
	}
}
