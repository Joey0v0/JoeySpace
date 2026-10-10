package main

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
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
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const multiSkipSQL = "UPDATE agent_task_drafts SET status = ? WHERE run_id = ? AND item_index = ? AND revision = ? AND status = 'waiting_confirmation' AND (task_request_key IS NULL OR task_request_key = '') AND (task_id IS NULL OR task_id = 0)"

type multiSkipFlow struct{ *multiConfirmFlow }

func newMultiSkipFlow(t *testing.T) *multiSkipFlow {
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
	f := &multiSkipFlow{multiConfirmFlow: &multiConfirmFlow{multiEditFlow: &multiEditFlow{multiPersistenceFlow: &multiPersistenceFlow{t: t, mock: mock, im: &multiPersistenceIM{}, httpClient: &http.Client{Timeout: 5 * time.Second}}, member: &multiEditUser{}}, states: [2]string{"waiting_confirmation", "waiting_confirmation"}}}
	f.runID.Store(9007199254741011)
	parsed := time.Date(2026, 10, 4, 7, 30, 0, 0, time.UTC).UnixMilli()
	reference := time.Date(2026, 10, 6, 16, 0, 0, 123000000, time.UTC).UnixMilli()
	f.drafts = []*agentpb.TaskDraftItem{
		{Title: "修复缓存", Description: "修复任务", SourceMessageId: multiPersistenceSourceID, AssigneeName: "张三", AssigneeResolution: "matched", AssigneeId: multiPersistenceAssigneeID, DueAtUnixMs: parsed, Revision: 1,
			Deadline: &agentpb.TaskDraftDeadline{Text: "明天 15:30", Source: "message", SourceMessageId: multiPersistenceTimeID, ReferenceUnixMs: time.Date(2026, 10, 3, 4, 0, 0, 456000000, time.UTC).UnixMilli(), Timezone: "Asia/Shanghai", Resolution: "parsed", ParsedUnixMs: parsed, InstructionReferenceUnixMs: reference}},
		{Title: "整理文档", SourceMessageId: multiPersistenceTimeID, AssigneeName: "李四", AssigneeResolution: "ambiguous", Revision: 3,
			Deadline: &agentpb.TaskDraftDeadline{Text: "明天下午", Source: "instruction", ReferenceUnixMs: reference, Timezone: "Asia/Shanghai", Resolution: "needs_input", Reason: "unsupported_expression", InstructionReferenceUnixMs: reference}},
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
	mux.HandleFunc("POST /runs/{run_id}/drafts/{item_index}/skip", wrap(skipTaskDraftItemHandler(client)))
	mux.HandleFunc("POST /runs/{run_id}/drafts/{item_index}/confirm", wrap(confirmTaskDraftItemHandler(client)))
	mux.HandleFunc("GET /runs/{run_id}/drafts", wrap(getTaskDraftCollectionHandler(client)))
	mux.HandleFunc("GET /runs/{run_id}/drafts/{item_index}", wrap(getTaskDraftItemHandler(client)))
	mux.HandleFunc("PUT /runs/{run_id}/drafts/{item_index}", wrap(editTaskDraftItemTextHandler(client)))
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *multiSkipFlow) skipBody(revision int64) string {
	return fmt.Sprintf(`{"expected_revision":"%d"}`, revision)
}

func (f *multiSkipFlow) expectSkip(index int) {
	f.expectLock()
	if f.states[index] == "waiting_confirmation" {
		f.mock.ExpectExec(regexp.QuoteMeta(multiSkipSQL)+"$").WithArgs("skipped", f.runID.Load(), int64(index), f.drafts[index].Revision).WillReturnResult(sqlmock.NewResult(0, 1))
		f.states[index] = "skipped"
	}
	f.mock.ExpectCommit()
}

func (f *multiSkipFlow) skip(index int) {
	f.t.Helper()
	data := f.request(http.MethodPost, f.path(index, "/skip"), f.skipBody(f.drafts[index].Revision), http.StatusOK)["data"].(map[string]any)
	f.checkScope(data)
	f.checkItem(data["item"], index)
	item := data["item"].(map[string]any)
	if item["reply_status"] != "disabled" || item["reply_msg_id"] != "" || item["task_id"] != "0" {
		f.t.Fatal("skipping reported task creation or reply")
	}
}

// Production HTTP handlers and Agent over real local TCP gRPC; SQL/User/IM/Task
// are substitutes. No model, bot, real database or migration is involved.
func TestMultiDraftItemHTTPSkipPreservesUnresolvedEvidenceAndIndependentWork(t *testing.T) {
	for _, revision := range []int64{3, math.MaxInt64} {
		t.Run(fmt.Sprintf("revision %d", revision), func(t *testing.T) {
			f := newMultiSkipFlow(t)
			f.drafts[1].Revision = revision
			f.expectRead()
			f.expectSkip(1)
			f.skip(1)
			f.readAll() // Original ambiguous name and needs_input time remain visible.
			f.expectRead()
			one := f.request(http.MethodGet, f.path(1, ""), "", http.StatusOK)["data"].(map[string]any)
			f.checkItem(one["item"], 1)
			f.expectRead()
			f.expectSkip(1) // Same-version replay locks but has no UPDATE.
			f.skip(1)
			if f.task.count() != 0 || f.member.checks.Load() != 0 || f.drafts[1].Revision != revision {
				t.Fatal("skip/replay changed content version or queried write dependencies")
			}
			f.expectRead()
			f.request(http.MethodPut, f.path(1, ""), fmt.Sprintf(`{"title":"cannot restore","description":"","expected_revision":"%d"}`, revision), http.StatusConflict)
			f.expectRead()
			f.confirm(1, http.StatusConflict)
			if f.task.count() != 0 || f.member.checks.Load() != 0 {
				t.Fatal("skipped target continued editing/confirmation")
			}

			// The remaining waiting item can still edit and create its own Task;
			// skip does not finish the whole run or change another item's version.
			f.expectRead()
			f.expectLock()
			f.expectWrite(multiEditTextSQL, 0, "继续修复缓存", "另一项独立处理", 1, 1)
			f.mock.ExpectCommit()
			f.drafts[0].Title, f.drafts[0].Description, f.drafts[0].Revision = "继续修复缓存", "另一项独立处理", 2
			data := f.request(http.MethodPut, f.path(0, ""), `{"title":"继续修复缓存","description":"另一项独立处理","expected_revision":"1"}`, http.StatusOK)["data"].(map[string]any)
			f.checkItem(data["item"], 0)
			f.task.plan(codes.OK, f.drafts)
			f.expectRead()
			f.expectFreeze(0)
			f.expectComplete(0, false)
			f.confirm(0, http.StatusOK)
			f.readAll()
			if f.task.count() != 1 || f.states[1] != "skipped" || f.ids[1] != 0 || f.keys[1] != "" || f.drafts[1].Revision != revision {
				t.Fatal("another item's result rewrote the skipped item")
			}
		})
	}
}

func TestMultiDraftItemHTTPSkipSQLVersionAndPermissionFailures(t *testing.T) {
	for _, mode := range []string{"SQL error", "zero rows", "two rows", "stale revision", "locked version changed", "locked content changed", "group revoked"} {
		t.Run(mode, func(t *testing.T) {
			f := newMultiSkipFlow(t)
			want, revision := http.StatusConflict, int64(1)
			if mode == "stale revision" {
				revision++
			}
			if mode == "group revoked" {
				f.im.deny.Store(true)
				want = http.StatusForbidden
			}
			f.expectRead()
			if mode == "SQL error" || mode == "zero rows" || mode == "two rows" {
				f.expectLock()
				expect := f.mock.ExpectExec(regexp.QuoteMeta(multiSkipSQL)+"$").WithArgs("skipped", f.runID.Load(), int64(0), int64(1))
				switch mode {
				case "SQL error":
					expect.WillReturnError(errors.New("private skip write failed"))
					want = http.StatusServiceUnavailable
				case "zero rows":
					expect.WillReturnResult(sqlmock.NewResult(0, 0))
				case "two rows":
					expect.WillReturnResult(sqlmock.NewResult(0, 2))
				}
				f.mock.ExpectRollback()
			} else if mode == "locked version changed" || mode == "locked content changed" {
				if mode == "locked version changed" {
					f.drafts[0].Revision++
				} else {
					f.drafts[0].Description = "同版本不同内容"
				}
				f.expectLock()
				f.mock.ExpectRollback()
			}
			f.request(http.MethodPost, f.path(0, "/skip"), f.skipBody(revision), want)
			if f.task.count() != 0 || f.member.checks.Load() != 0 {
				t.Fatal("rejected skip consulted Task or target member")
			}
		})
	}
	t.Run("revoked assignee does not prevent rejecting a candidate", func(t *testing.T) {
		f := newMultiSkipFlow(t)
		f.member.revoked.Store(true)
		f.expectRead()
		f.expectSkip(0)
		f.skip(0)
		if f.member.checks.Load() != 0 || f.task.count() != 0 {
			t.Fatal("skip unnecessarily checked the candidate's assignee")
		}
	})
	t.Run("other item changed before skip lock", func(t *testing.T) {
		f := newMultiSkipFlow(t)
		f.expectRead()
		f.drafts[1].Title, f.drafts[1].Revision = "另一项已更新", 9007199254740993
		f.expectSkip(0)
		f.skip(0)
		f.readAll()
		if f.task.count() != 0 || f.drafts[1].Revision != 9007199254740993 {
			t.Fatal("skip conflicted with or changed the other item")
		}
	})
}

func TestMultiDraftItemHTTPSkipAndFreezeCompetitionNeverCreatesSkippedTask(t *testing.T) {
	for _, state := range []string{"creating", "succeeded"} {
		for _, duringLock := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s skip duringLock=%t", state, duringLock), func(t *testing.T) {
				f := newMultiSkipFlow(t)
				if duringLock {
					f.expectRead()
				}
				f.states[0], f.keys[0] = state, fmt.Sprintf("agent-task-%d-0", f.runID.Load())
				if state == "succeeded" {
					f.ids[0] = 9007199254741201
				}
				if duringLock {
					f.expectLock()
					f.mock.ExpectRollback()
				} else {
					f.expectRead()
				}
				f.request(http.MethodPost, f.path(0, "/skip"), f.skipBody(1), http.StatusConflict)
				if f.task.count() != 0 || f.member.checks.Load() != 0 {
					t.Fatal("skip tried to cancel or recreate a frozen task")
				}
			})
		}
	}
	t.Run("skip wins after confirmation authorized waiting", func(t *testing.T) {
		f := newMultiSkipFlow(t)
		f.expectRead()          // The confirmation saw a valid waiting snapshot.
		f.states[0] = "skipped" // Skip commits first, before the freeze lock.
		f.expectLock()
		f.mock.ExpectRollback()
		f.confirm(0, http.StatusConflict)
		if f.task.count() != 0 {
			t.Fatal("confirmation created a Task after skip won the lock")
		}
	})
	t.Run("already skipped resolved target rejects confirmation before member check", func(t *testing.T) {
		f := newMultiSkipFlow(t)
		f.states[0] = "skipped"
		f.member.revoked.Store(true)
		f.expectRead()
		f.confirm(0, http.StatusConflict)
		if f.task.count() != 0 || f.member.checks.Load() != 0 {
			t.Fatal("skipped confirmation queried write dependencies")
		}
	})
}
