package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
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

const replyFlowTaskID int64 = 9223372036854775806

type replyFlowTask struct {
	taskpb.UnimplementedTaskServer
	calls atomic.Int32
}

func (s *replyFlowTask) CreateTask(ctx context.Context, req *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" || len(md.Get("idempotency-key")) != 1 || md.Get("idempotency-key")[0] != "agent-task-9001-0" || req.GetTeamId() != flowTeamID || req.GetTitle() != "Task" || req.GetDescription() != "" {
		return nil, status.Error(codes.InvalidArgument, "unexpected task creation")
	}
	s.calls.Add(1)
	return &taskpb.CreateTaskResponse{TaskId: replyFlowTaskID}, nil
}

type replyFlowBot struct {
	impb.UnimplementedIMBotServer
	calls   atomic.Int32
	content string
	im      *confirmFlowIM
}

func (s *replyFlowBot) PostTaskCreatedCard(ctx context.Context, req *impb.PostTaskCreatedCardRequest) (*impb.PostTaskCreatedCardResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" {
		return nil, status.Error(codes.Unauthenticated, "wrong original identity")
	}
	if s.im.deny.Load() {
		return nil, status.Error(codes.PermissionDenied, "revoked")
	}
	if req.GetRunId() != 9001 || req.GetTeamId() != flowTeamID || req.GetGroupId() != flowGroupID || req.GetContent() != s.content {
		return nil, status.Error(codes.AlreadyExists, "changed frozen reply")
	}
	if s.calls.Add(1) == 1 {
		return nil, status.Error(codes.DeadlineExceeded, "private IM response lost")
	}
	return &impb.PostTaskCreatedCardResponse{MsgId: "bot-task:9001", Accepted: true}, nil
}

// Test-only CA and separate leaf keys. No deployment credentials or provider calls.
func replyFlowCertificates(t *testing.T) (rpcauth.CertificateFiles, rpcauth.CertificateFiles) {
	t.Helper()
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	var files [2]rpcauth.CertificateFiles
	for i, name := range []string{"im.go-im.internal", "agent.go-im.internal"} {
		leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		usage := x509.ExtKeyUsageServerAuth
		if i == 1 {
			usage = x509.ExtKeyUsageClientAuth
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(int64(i + 2)), DNSNames: []string{name}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
		if err != nil {
			t.Fatal(err)
		}
		files[i] = rpcauth.CertificateFiles{CertFile: filepath.Join(dir, name+".pem"), KeyFile: filepath.Join(dir, name+".key"), CAFile: caPath}
		if err := os.WriteFile(files[i].CertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(files[i].KeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return files[0], files[1]
}

func TestReplyHTTPThroughAgentAndMutualTLSRecoversWithoutAnotherTask(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormmysql.New(gormmysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	im := &confirmFlowIM{}
	tasks := &replyFlowTask{}
	content, err := model.EncodeTaskCreatedCard(replyFlowTaskID, "Task")
	if err != nil {
		t.Fatal(err)
	}
	bot := &replyFlowBot{content: content, im: im}
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
	impb.RegisterIMBotServer(botServer, bot)
	go func() { _ = botServer.Serve(listener) }()
	t.Cleanup(botServer.Stop)
	values := map[string]string{"AGENT_IM_BOT_ADDR": listener.Addr().String(), "AGENT_IM_BOT_SERVER_NAME": "im.go-im.internal", "AGENT_IM_BOT_TLS_CERT_FILE": clientFiles.CertFile, "AGENT_IM_BOT_TLS_KEY_FILE": clientFiles.KeyFile, "AGENT_IM_BOT_TLS_CA_FILE": clientFiles.CAFile}
	botClient, closeBot, err := agent.NewBotReplyClient(func(n string) string { return values[n] })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeBot)
	dial := func(addr string) *grpc.ClientConn {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	userAddr := startFlowRPC(t, func(s *grpc.Server) { userpb.RegisterUserServer(s, &confirmFlowUser{}) })
	imAddr := startFlowRPC(t, func(s *grpc.Server) { impb.RegisterIMServer(s, im) })
	taskAddr := startFlowRPC(t, func(s *grpc.Server) { taskpb.RegisterTaskServer(s, tasks) })
	impl := agent.NewServer(nil)
	impl.ConfigureDraftAccess(db, userpb.NewUserClient(dial(userAddr)), impb.NewIMClient(dial(imAddr)))
	impl.ConfigureDraftConfirmation(db, taskpb.NewTaskClient(dial(taskAddr)))
	impl.ConfigureDraftReplies(db, botClient)
	agentAddr := startFlowRPC(t, func(s *grpc.Server) { agentpb.RegisterAgentServer(s, impl) })
	agentClient := agentpb.NewAgentClient(dial(agentAddr))
	mux := http.NewServeMux()
	wrap := func(handler http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			handler(w, pathvar.WithVars(r, map[string]string{"run_id": r.PathValue("run_id")}))
		}
	}
	mux.HandleFunc("POST /api/v1/agent/runs/{run_id}/confirm", wrap(confirmTaskDraftHandler(agentClient)))
	mux.HandleFunc("GET /api/v1/agent/runs/{run_id}/draft", wrap(getTaskDraftHandler(agentClient)))
	mux.HandleFunc("POST /api/v1/agent/runs/{run_id}/reply/retry", wrap(retryTaskReplyHandler(agentClient)))
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)
	client := &http.Client{Timeout: 5 * time.Second}
	request := func(method, suffix, body string, want int) agentDraftResponse {
		req, err := http.NewRequest(method, httpServer.URL+"/api/v1/agent/runs/9001/"+suffix, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer user-token")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		bytes, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want || strings.Contains(string(bytes), "private") {
			t.Fatalf("HTTP = %d %s", resp.StatusCode, bytes)
		}
		var result agentDraftResponse
		if err := json.Unmarshal(bytes, &result); err != nil {
			t.Fatal(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	expectRead := func(state string, id int64) {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT r.team_id")).WithArgs(int64(9001), int64(400)).WillReturnRows(confirmFlowRows(state, id))
	}
	expectLock := func(state string, id int64) {
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT r\.team_id[\s\S]*FOR UPDATE`).WithArgs(int64(9001), int64(400)).WillReturnRows(confirmFlowRows(state, id))
	}
	replyRows := func(accepted bool) *sqlmock.Rows {
		return sqlmock.NewRows([]string{"run_id", "task_id", "team_id", "group_id", "initiator_id", "msg_id", "content", "accepted"}).AddRow(9001, replyFlowTaskID, flowTeamID, flowGroupID, 400, "bot-task:9001", content, accepted)
	}
	emptyReply := sqlmock.NewRows([]string{"run_id", "task_id", "team_id", "group_id", "initiator_id", "msg_id", "content", "accepted"})
	expectReplyRead := func(rows *sqlmock.Rows) {
		mock.ExpectQuery(`SELECT run_id, task_id[\s\S]*FROM agent_task_replies`).WithArgs(int64(9001), int64(400)).WillReturnRows(rows)
	}

	// Confirm creates one task, then freezes the reply before the first TLS call.
	expectRead("waiting_confirmation", 0)
	expectLock("waiting_confirmation", 0)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_task_drafts SET task_request_key = ?")).WithArgs("agent-task-9001-0", int64(9001)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_runs SET status = ?")).WithArgs("creating", int64(9001), int64(400)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	expectLock("creating", 0)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_task_drafts SET task_id = ?")).WithArgs(replyFlowTaskID, int64(9001)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_runs SET status = ?")).WithArgs("succeeded", int64(9001), int64(400)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	expectLock("succeeded", replyFlowTaskID)
	expectReplyRead(emptyReply)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO agent_task_replies")).WithArgs(int64(9001), replyFlowTaskID, flowTeamID, flowGroupID, int64(400), "bot-task:9001", content, false).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	result := request(http.MethodPost, "confirm", validDraftConfirmBody, 200)
	if result.Data == nil || result.Data.Status != "succeeded" || result.Data.TaskID != replyFlowTaskID || result.Data.ReplyStatus != "pending" || result.Data.ReplyMsgID != "bot-task:9001" {
		t.Fatalf("initial = %+v", result.Data)
	}

	// Reading never posts. The first explicit retry reaches IM, but Agent cannot
	// save the acknowledgement. Another read/retry uses the same fixed content.
	expectRead("succeeded", replyFlowTaskID)
	expectReplyRead(replyRows(false))
	if got := request(http.MethodGet, "draft", "", 200); got.Data.ReplyStatus != "pending" {
		t.Fatal(got)
	}
	if bot.calls.Load() != 1 {
		t.Fatal("read posted")
	}
	for attempt := 0; attempt < 2; attempt++ {
		expectRead("succeeded", replyFlowTaskID)
		expectLock("succeeded", replyFlowTaskID)
		expectReplyRead(replyRows(false))
		mock.ExpectCommit()
		update := mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_task_replies SET accepted = 1")).WithArgs(int64(9001), int64(400), "bot-task:9001")
		want := 200
		if attempt == 0 {
			want = 503
			update.WillReturnError(io.ErrUnexpectedEOF)
		} else {
			update.WillReturnResult(sqlmock.NewResult(0, 1))
		}
		got := request(http.MethodPost, "reply/retry", "", want)
		if attempt == 1 && (got.Data == nil || got.Data.TaskID != replyFlowTaskID || got.Data.ReplyStatus != "accepted") {
			t.Fatalf("retry = %+v", got)
		}
		if attempt == 0 {
			expectRead("succeeded", replyFlowTaskID)
			expectReplyRead(replyRows(false))
			request(http.MethodGet, "draft", "", 200)
		}
	}
	// Accepted replays and confirmation reads do not send or create again.
	expectRead("succeeded", replyFlowTaskID)
	expectLock("succeeded", replyFlowTaskID)
	expectReplyRead(replyRows(true))
	mock.ExpectCommit()
	request(http.MethodPost, "reply/retry", "", 200)
	expectRead("succeeded", replyFlowTaskID)
	expectLock("succeeded", replyFlowTaskID)
	mock.ExpectCommit()
	expectReplyRead(replyRows(true))
	request(http.MethodPost, "confirm", validDraftConfirmBody, 200)
	im.deny.Store(true)
	expectRead("succeeded", replyFlowTaskID)
	request(http.MethodPost, "reply/retry", "", 403)
	if tasks.calls.Load() != 1 || bot.calls.Load() != 3 {
		t.Fatalf("unexpected side effects: task=%d bot=%d", tasks.calls.Load(), bot.calls.Load())
	}
}
