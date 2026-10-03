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

type referenceFlowModel struct{ calls atomic.Int32 }

func (*referenceFlowModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream not used")
}

func (m *referenceFlowModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if len(input) != 2 || !strings.Contains(input[1].Content, "明天 15:30 完成缓存修复") {
		return nil, errors.New("missing original instruction")
	}
	if m.calls.Add(1) == 1 {
		return nil, errors.New("private first model failure")
	}
	// This batch preserves the original model output, without automatic dates.
	return schema.AssistantMessage(`{"title":"修复缓存","description":"","source_message_id":"600","assignee_name":""}`, nil), nil
}

type referenceFlowIM struct {
	impb.UnimplementedIMServer
	deny atomic.Bool
}

func (s *referenceFlowIM) ListTeamGroupMessages(ctx context.Context, req *impb.ListTeamGroupMessagesRequest) (*impb.ListTeamGroupMessagesResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if values := md.Get("authorization"); len(values) != 1 || values[0] != "Bearer user-token" ||
		req.GetTeamId() != flowTeamID || req.GetGroupId() != flowGroupID || len(md.Get("idempotency-key")) != 0 {
		return nil, status.Error(codes.InvalidArgument, "wrong scope or token")
	}
	if s.deny.Load() {
		return nil, status.Error(codes.PermissionDenied, "private lost membership")
	}
	return &impb.ListTeamGroupMessagesResponse{Messages: []*impb.TeamGroupMessage{{Id: 600, ContentType: 1, Content: "修复缓存"}}}, nil
}

type captureReferenceFlowRun struct{ id *atomic.Int64 }

func (c captureReferenceFlowRun) Match(value driver.Value) bool {
	id, ok := value.(int64)
	if ok && id > 0 {
		c.id.Store(id)
		return true
	}
	return false
}

// Actual HTTP/TCP gRPC, Eino and production Agent storage adapters; database,
// User/IM and model are substitutes. No automatic time extraction in this batch.
func TestDeadlineReferenceHTTPAgentPersistenceAndReplay(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	db, err := gorm.Open(gormmysql.New(gormmysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	im, chat := &referenceFlowIM{}, &referenceFlowModel{}
	userAddr := startFlowRPC(t, func(s *grpc.Server) { userpb.RegisterUserServer(s, &confirmFlowUser{}) })
	imAddr := startFlowRPC(t, func(s *grpc.Server) { impb.RegisterIMServer(s, im) })
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
	impl.ConfigureDraftPreparation(db, userpb.NewUserClient(dial(userAddr)), impb.NewIMClient(dial(imAddr)), generator, node)
	agentAddr := startFlowRPC(t, func(s *grpc.Server) { agentpb.RegisterAgentServer(s, impl) })
	handler := prepareTaskDraftHandler(agentpb.NewAgentClient(dial(agentAddr)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler(w, pathvar.WithVars(r, map[string]string{"team_id": fmt.Sprint(flowTeamID), "group_id": fmt.Sprint(flowGroupID)}))
	}))
	t.Cleanup(server.Close)
	client := &http.Client{Timeout: 5 * time.Second}
	// Shanghai just before midnight: the retry sends these same bytes, regardless
	// of when the server handles it; no current clock is added by either layer.
	reference := time.Date(2026, 10, 3, 23, 59, 59, 123000000, time.FixedZone("test Shanghai", 8*3600)).UnixMilli()
	const instruction = "明天 15:30 完成缓存修复"
	body := fmt.Sprintf(`{"instruction":%q,"instruction_reference_unix_ms":%d}`, instruction, reference)
	identity := fmt.Sprintf(`{"team_id":%d,"group_id":%d,"instruction":%q,"instruction_reference_unix_ms":%d}`, flowTeamID, flowGroupID, instruction, reference)
	hash := sha256.Sum256([]byte(identity))
	fingerprint := hex.EncodeToString(hash[:])
	var runID atomic.Int64
	request := func(content string, want int) int64 {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(content))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer user-token")
		req.Header.Set("Idempotency-Key", "reference-flow-1")
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want || strings.Contains(string(data), "private ") {
			t.Fatalf("status %d: %s", resp.StatusCode, data)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		var result agentDraftResponse
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if want == 200 && (result.Code != 0 || result.Data == nil || result.Data.RunID != runID.Load()) {
			t.Fatalf("changed run: %s", data)
		}
		if result.Data != nil {
			return result.Data.RunID
		}
		return 0
	}
	lookup := regexp.QuoteMeta("SELECT id, team_id, group_id, request_fingerprint FROM agent_runs")
	expectLookup := func(saved bool) {
		rows := sqlmock.NewRows([]string{"id", "team_id", "group_id", "request_fingerprint"})
		if saved {
			rows.AddRow(runID.Load(), flowTeamID, flowGroupID, fingerprint)
		}
		mock.ExpectQuery(lookup).WithArgs(int64(400), "reference-flow-1").WillReturnRows(rows)
	}
	expectLookup(false)
	request(body, 503)
	expectLookup(false)
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO agent_runs").WithArgs(captureReferenceFlowRun{&runID}, flowTeamID, flowGroupID, int64(400), "reference-flow-1", fingerprint, "waiting_confirmation").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO agent_task_drafts").WithArgs(sqlmock.AnyArg(), "修复缓存", "", int64(0), int64(0), int64(600), "", "none").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	request(body, 200)
	expectLookup(true)
	request(body, 200)
	expectLookup(true)
	request(fmt.Sprintf(`{"instruction":%q,"instruction_reference_unix_ms":%d}`, instruction, reference+1000), 409)
	expectLookup(true)
	request(fmt.Sprintf(`{"instruction":%q}`, instruction), 409)
	if chat.calls.Load() != 2 {
		t.Fatalf("replayed model: %d", chat.calls.Load())
	}
	im.deny.Store(true)
	request(body, 403)
}
