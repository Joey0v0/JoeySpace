package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/rpcauth"
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

type multiReplyTLSBot struct {
	impb.UnimplementedIMBotServer
	mu       sync.Mutex
	runID    int64
	im       *multiPersistenceIM
	cards    [2]string
	codes    [2]codes.Code
	calls    [2]int
	oldCalls int
}

func (b *multiReplyTLSBot) PostTaskCreatedCard(context.Context, *impb.PostTaskCreatedCardRequest) (*impb.PostTaskCreatedCardResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.oldCalls++
	return nil, status.Error(codes.Unimplemented, "legacy posting cannot represent a collection item")
}

func (b *multiReplyTLSBot) PostTaskCreatedCardItem(ctx context.Context, r *impb.PostTaskCreatedCardItemRequest) (*impb.PostTaskCreatedCardResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	md, _ := metadata.FromIncomingContext(ctx)
	if r == nil || r.ItemIndex == nil || r.GetItemIndex() < 0 || r.GetItemIndex() > 1 || r.GetRunId() != b.runID ||
		r.GetTeamId() != flowTeamID || r.GetGroupId() != flowGroupID || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" || len(md.Get("idempotency-key")) != 0 {
		return nil, status.Error(codes.InvalidArgument, "changed item identity or original credentials")
	}
	i := r.GetItemIndex()
	if b.im.deny.Load() {
		return nil, status.Error(codes.PermissionDenied, "private revoked group")
	}
	if r.GetContent() != b.cards[i] {
		return nil, status.Error(codes.AlreadyExists, "changed fixed card")
	}
	b.calls[i]++
	if b.codes[i] != codes.OK {
		return nil, status.Error(b.codes[i], "private IM result uncertain")
	}
	id, _ := model.BotTaskItemMsgID(b.runID, i)
	return &impb.PostTaskCreatedCardResponse{MsgId: id, Accepted: true}, nil
}

func (b *multiReplyTLSBot) plan(index int, code codes.Code) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.codes[index] = code
}
func (b *multiReplyTLSBot) counts() ([2]int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls, b.oldCalls
}

type multiReplyHTTPFlow struct {
	*multiConfirmFlow
	bot *multiReplyTLSBot
}

func newMultiReplyHTTPFlow(t *testing.T) *multiReplyHTTPFlow {
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
	f := &multiReplyHTTPFlow{multiConfirmFlow: &multiConfirmFlow{multiEditFlow: &multiEditFlow{multiPersistenceFlow: &multiPersistenceFlow{t: t, mock: mock, im: &multiPersistenceIM{}, httpClient: &http.Client{Timeout: 5 * time.Second}}, member: &multiEditUser{}}, states: [2]string{"waiting_confirmation", "waiting_confirmation"}}}
	f.runID.Store(9007199254741011)
	for _, title := range []string{"修复缓存", "整理文档"} {
		f.drafts = append(f.drafts, &agentpb.TaskDraftItem{Title: title, Revision: 1, AssigneeResolution: "none", Deadline: &agentpb.TaskDraftDeadline{Source: "none", Timezone: "Asia/Shanghai", Resolution: "none", InstructionReferenceUnixMs: 1791000000123}})
	}
	f.task = &multiConfirmTask{runID: f.runID.Load()}
	f.task.plan(codes.OK, f.drafts)
	f.bot = &multiReplyTLSBot{runID: f.runID.Load(), im: f.im}
	for i := range f.bot.cards {
		f.bot.cards[i], err = model.EncodeTaskCreatedCard(int64(9007199254741201+i), f.drafts[i].Title)
		if err != nil {
			t.Fatal(err)
		}
	}
	serverFiles, clientFiles := replyFlowCertificates(t)
	creds, err := rpcauth.NewIMServerCredentials(serverFiles, "agent.go-im.internal")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	botServer := grpc.NewServer(grpc.Creds(creds))
	impb.RegisterIMBotServer(botServer, f.bot)
	go botServer.Serve(listener)
	t.Cleanup(func() { botServer.Stop(); listener.Close() })
	values := map[string]string{"AGENT_IM_BOT_ADDR": listener.Addr().String(), "AGENT_IM_BOT_SERVER_NAME": "im.go-im.internal", "AGENT_IM_BOT_TLS_CERT_FILE": clientFiles.CertFile, "AGENT_IM_BOT_TLS_KEY_FILE": clientFiles.KeyFile, "AGENT_IM_BOT_TLS_CA_FILE": clientFiles.CAFile}
	botClient, closeBot, err := agent.NewBotReplyClient(func(n string) string { return values[n] })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeBot)
	dial := func(addr string) *grpc.ClientConn {
		c, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	users := userpb.NewUserClient(dial(startFlowRPC(t, func(s *grpc.Server) { userpb.RegisterUserServer(s, f.member) })))
	ims := impb.NewIMClient(dial(startFlowRPC(t, func(s *grpc.Server) { impb.RegisterIMServer(s, f.im) })))
	tasks := taskpb.NewTaskClient(dial(startFlowRPC(t, func(s *grpc.Server) { taskpb.RegisterTaskServer(s, f.task) })))
	impl := agent.NewServer(nil)
	impl.ConfigureDraftAccess(db, users, ims)
	impl.ConfigureDraftConfirmation(db, tasks)
	impl.ConfigureDraftReplies(db, botClient)
	client := agentpb.NewAgentClient(dial(startFlowRPC(t, func(s *grpc.Server) { agentpb.RegisterAgentServer(s, impl) })))
	mux := http.NewServeMux()
	wrap := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			h(w, pathvar.WithVars(r, map[string]string{"run_id": r.PathValue("run_id"), "item_index": r.PathValue("item_index")}))
		}
	}
	mux.HandleFunc("POST /runs/{run_id}/drafts/{item_index}/confirm", wrap(confirmTaskDraftItemHandler(client)))
	mux.HandleFunc("POST /runs/{run_id}/drafts/{item_index}/reply/retry", wrap(retryTaskReplyItemHandler(client)))
	mux.HandleFunc("GET /runs/{run_id}/drafts", wrap(getTaskDraftCollectionHandler(client)))
	mux.HandleFunc("GET /runs/{run_id}/drafts/{item_index}", wrap(getTaskDraftItemHandler(client)))
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *multiReplyHTTPFlow) replyRows(index int, found, accepted bool) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"run_id", "item_index", "task_id", "team_id", "group_id", "initiator_id", "msg_id", "content", "accepted"})
	if found {
		id, _ := model.BotTaskItemMsgID(f.runID.Load(), int32(index))
		rows.AddRow(f.runID.Load(), index, f.ids[index], flowTeamID, flowGroupID, 400, id, f.bot.cards[index], accepted)
	}
	return rows
}

func (f *multiReplyHTTPFlow) expectReplyRead(index int, found, accepted bool) {
	f.mock.ExpectQuery(`SELECT run_id, item_index, task_id[\s\S]*FROM agent_task_replies`).WithArgs(f.runID.Load(), int64(index), int64(400)).WillReturnRows(f.replyRows(index, found, accepted))
}

func (f *multiReplyHTTPFlow) expectReplyPrepare(index int, found, accepted bool) {
	f.expectLock()
	f.expectReplyRead(index, found, accepted)
	if !found {
		id, _ := model.BotTaskItemMsgID(f.runID.Load(), int32(index))
		f.mock.ExpectExec("INSERT INTO agent_task_replies").WithArgs(f.runID.Load(), int64(index), f.ids[index], flowTeamID, flowGroupID, int64(400), id, f.bot.cards[index], false).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	f.mock.ExpectCommit()
}

func (f *multiReplyHTTPFlow) expectAccept(index int, failure bool) {
	id, _ := model.BotTaskItemMsgID(f.runID.Load(), int32(index))
	e := f.mock.ExpectExec("UPDATE agent_task_replies SET accepted = 1").WithArgs(f.runID.Load(), int64(index), f.ids[index], flowTeamID, flowGroupID, int64(400), id, f.bot.cards[index])
	if failure {
		e.WillReturnError(errors.New("private acknowledgement save failed"))
	} else {
		e.WillReturnResult(sqlmock.NewResult(0, 1))
	}
}

func (f *multiReplyHTTPFlow) itemReply(envelope map[string]any, index int, state string) {
	f.t.Helper()
	data := envelope["data"].(map[string]any)
	f.checkScope(data)
	item := data["item"].(map[string]any)
	if item["status"] != "succeeded" || item["task_id"] != fmt.Sprint(f.ids[index]) || item["reply_status"] != state || item["item_index"] != json.Number(fmt.Sprint(index)) {
		f.t.Fatalf("wrong item task/reply: %#v", item)
	}
	msgID := ""
	if state == "pending" || state == "accepted" {
		msgID, _ = model.BotTaskItemMsgID(f.runID.Load(), int32(index))
	}
	if item["reply_msg_id"] != msgID {
		f.t.Fatalf("wrong reply message: %#v", item)
	}
	copyItem := make(map[string]any, len(item))
	for k, v := range item {
		copyItem[k] = v
	}
	copyItem["reply_status"], copyItem["reply_msg_id"] = "not_started", ""
	f.checkItem(copyItem, index)
}

// Real HTTP/TCP gRPC, production Agent/store and production Agent mTLS client.
// User, IM permission, Task, IMBot business and SQL are substitutes; no model,
// real MySQL/Kafka or migrations run. Each reply retry leaves Task counts fixed.
func TestMultiDraftReplyHTTPThroughAgentAndTLSIsIndependent(t *testing.T) {
	f := newMultiReplyHTTPFlow(t)
	f.bot.plan(0, codes.DeadlineExceeded)
	f.expectRead()
	f.expectFreeze(0)
	f.expectComplete(0, false)
	f.expectReplyPrepare(0, false, false)
	f.itemReply(f.request(http.MethodPost, f.path(0, "/confirm"), f.body(0, 1), http.StatusOK), 0, "pending")
	// A second item creates and replies independently despite item 0 pending.
	f.expectRead()
	f.expectFreeze(1)
	f.expectComplete(1, false)
	f.expectReplyPrepare(1, false, false)
	f.expectAccept(1, false)
	f.itemReply(f.request(http.MethodPost, f.path(1, "/confirm"), f.body(1, 1), http.StatusOK), 1, "accepted")
	f.expectRead()
	f.expectReplyRead(0, true, false)
	f.expectReplyRead(1, true, true)
	data := f.request(http.MethodGet, fmt.Sprintf("/runs/%d/drafts", f.runID.Load()), "", http.StatusOK)["data"].(map[string]any)
	items := data["items"].([]any)
	if items[0].(map[string]any)["reply_status"] != "pending" || items[1].(map[string]any)["reply_status"] != "accepted" {
		t.Fatal(items)
	}
	// Success confirmation replay is read-only, even while posting is pending.
	f.expectRead()
	f.expectFreeze(0)
	f.expectReplyRead(0, true, false)
	f.itemReply(f.request(http.MethodPost, f.path(0, "/confirm"), f.body(0, 1), http.StatusOK), 0, "pending")
	f.bot.plan(0, codes.OK)
	f.expectRead()
	f.expectReplyPrepare(0, true, false)
	f.expectAccept(0, false)
	f.itemReply(f.request(http.MethodPost, f.path(0, "/reply/retry"), "", http.StatusOK), 0, "accepted")
	// Accepted replay still authorizes but does not issue another IM or Task call.
	f.expectRead()
	f.expectReplyPrepare(0, true, true)
	f.itemReply(f.request(http.MethodPost, f.path(0, "/reply/retry"), "", http.StatusOK), 0, "accepted")
	counts, old := f.bot.counts()
	if counts != [2]int{2, 1} || old != 0 || f.task.count() != 2 || f.member.checks.Load() != 0 {
		t.Fatalf("bot=%v old=%d Task=%d members=%d", counts, old, f.task.count(), f.member.checks.Load())
	}
	f.expectRead()
	f.im.deny.Store(true)
	f.request(http.MethodPost, f.path(0, "/reply/retry"), "", http.StatusForbidden)
	after, _ := f.bot.counts()
	if after != counts || f.task.count() != 2 {
		t.Fatal("revoked replay touched write services")
	}
}

func TestMultiDraftReplyHTTPAcceptanceSaveFailureOnlyRetriesTheCard(t *testing.T) {
	f := newMultiReplyHTTPFlow(t)
	f.expectRead()
	f.expectFreeze(1)
	f.expectComplete(1, false)
	f.expectReplyPrepare(1, false, false)
	f.expectAccept(1, true)
	f.itemReply(f.request(http.MethodPost, f.path(1, "/confirm"), f.body(1, 1), http.StatusOK), 1, "pending")
	f.expectRead()
	f.expectReplyRead(1, true, false)
	f.itemReply(f.request(http.MethodGet, f.path(1, ""), "", http.StatusOK), 1, "pending")
	f.expectRead()
	f.expectReplyPrepare(1, true, false)
	f.expectAccept(1, false)
	f.itemReply(f.request(http.MethodPost, f.path(1, "/reply/retry"), "", http.StatusOK), 1, "accepted")
	counts, old := f.bot.counts()
	if counts != [2]int{0, 2} || old != 0 || f.task.count() != 1 || f.states[0] != "waiting_confirmation" || f.ids[0] != 0 {
		t.Fatal("retry rebuilt a Task or changed the other item")
	}
}

func TestMultiDraftReplyHTTPUnsavedIntentReturnsUnknownAndCanRecover(t *testing.T) {
	f := newMultiReplyHTTPFlow(t)
	f.expectRead()
	f.expectFreeze(0)
	f.expectComplete(0, false)
	f.expectLock()
	f.expectReplyRead(0, false, false)
	f.mock.ExpectExec("INSERT INTO agent_task_replies").WillReturnError(errors.New("private reply insert failed"))
	f.mock.ExpectRollback()
	f.itemReply(f.request(http.MethodPost, f.path(0, "/confirm"), f.body(0, 1), http.StatusOK), 0, "unknown")
	counts, _ := f.bot.counts()
	if counts != [2]int{} || f.task.count() != 1 {
		t.Fatal("failed intent was posted or lost Task success")
	}
	f.expectRead()
	f.expectReplyRead(0, false, false)
	f.itemReply(f.request(http.MethodGet, f.path(0, ""), "", http.StatusOK), 0, "not_started")
	f.expectRead()
	f.expectReplyPrepare(0, false, false)
	f.expectAccept(0, false)
	f.itemReply(f.request(http.MethodPost, f.path(0, "/reply/retry"), "", http.StatusOK), 0, "accepted")
	// GET storage failure must not return a fabricated acceptance.
	f.expectRead()
	f.mock.ExpectQuery(`SELECT run_id, item_index, task_id[\s\S]*FROM agent_task_replies`).WithArgs(f.runID.Load(), int64(0), int64(400)).WillReturnError(errors.New("private reply lookup failed"))
	f.request(http.MethodGet, f.path(0, ""), "", http.StatusServiceUnavailable)
	counts, old := f.bot.counts()
	if counts != [2]int{1, 0} || old != 0 || f.task.count() != 1 {
		t.Fatal("recovery created another Task or GET posted")
	}
}
