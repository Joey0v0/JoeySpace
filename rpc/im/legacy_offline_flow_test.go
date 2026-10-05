package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/yjydist/go-im/internal/config"
	"github.com/yjydist/go-im/internal/handler"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type legacyOfflineFlow struct {
	http     *httptest.Server
	token    string
	rpcCalls atomic.Int64
}

type legacyOfflineFlowResponse struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
}

type legacyOfflineFlowMessage struct {
	ID          string    `json:"id"`
	MsgID       string    `json:"msg_id"`
	FromID      string    `json:"from_id"`
	SenderType  int       `json:"sender_type"`
	InitiatorID string    `json:"initiator_id"`
	ToID        string    `json:"to_id"`
	ChatType    int       `json:"chat_type"`
	ContentType int       `json:"content_type"`
	Content     string    `json:"content"`
	CreatedAt   time.Time `json:"created_at"`
}

// HTTP and gRPC use real loopback connections; SQL and User remain controlled
// substitutes. Tests are serial because the production Gin Auth uses a global.
func newLegacyOfflineFlow(t *testing.T, impl *imServer) *legacyOfflineFlow {
	t.Helper()
	flow := &legacyOfflineFlow{token: "Bearer " + validIMToken(t)}
	previous := config.GlobalConfig
	config.GlobalConfig = &config.Config{JWT: config.JWTConfig{Secret: imTestSecret}}
	t.Cleanup(func() { config.GlobalConfig = previous })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		flow.rpcCalls.Add(1)
		md, _ := metadata.FromIncomingContext(ctx)
		if values := md.Get("authorization"); len(values) != 1 || values[0] != flow.token {
			t.Error("legacy HTTP did not forward exactly the original Bearer")
		}
		for _, key := range []string{"user_id", "idempotency-key", "x-service-token"} {
			if len(md.Get(key)) != 0 {
				t.Errorf("legacy HTTP forwarded caller-controlled %s", key)
			}
		}
		return next(ctx, req)
	}))
	pb.RegisterIMServer(server, impl)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("legacy offline gRPC server did not stop")
		}
	})
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	router := gin.New()
	handler.RegisterRoutes(router, zap.NewNop(), pb.NewIMClient(conn))
	flow.http = httptest.NewServer(router)
	flow.http.Client().Timeout = 5 * time.Second
	t.Cleanup(flow.http.Close)
	return flow
}

func (flow *legacyOfflineFlow) request(t *testing.T, method, path, body string, authenticated bool) (legacyOfflineFlowResponse, string) {
	t.Helper()
	req, err := http.NewRequest(method, flow.http.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if authenticated {
		req.Header.Set("Authorization", flow.token)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("user_id", "999")
	req.Header.Set("idempotency-key", "caller-key-must-not-reach-im")
	req.Header.Set("x-service-token", "caller-service-value-must-not-reach-im")
	resp, err := flow.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	wire, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("legacy HTTP status = %d", resp.StatusCode)
	}
	var result legacyOfflineFlowResponse
	if err := json.Unmarshal(wire, &result); err != nil {
		t.Fatal(err)
	}
	return result, string(wire)
}

func legacyOfflineFlowMessages(t *testing.T, response legacyOfflineFlowResponse) []legacyOfflineFlowMessage {
	t.Helper()
	if response.Code != errcode.Success || len(response.Data) == 0 || string(response.Data) == "null" {
		t.Fatalf("offline pull code = %d, missing array = %t", response.Code, len(response.Data) == 0)
	}
	var messages []legacyOfflineFlowMessage
	if err := json.Unmarshal(response.Data, &messages); err != nil {
		t.Fatal(err)
	}
	return messages
}

func TestLegacyOfflineHTTPProductionRPCPreservesStringIDsAndBotCard(t *testing.T) {
	impl, mock := testIMServer(t)
	flow := newLegacyOfflineFlow(t, impl)
	const groupID int64 = 9007199254740997
	now := time.Date(2026, 10, 5, 12, 0, 0, 123000000, time.UTC)
	card, err := model.EncodeTaskCreatedCard(9007199254740998, "已确认标题")
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(offlineMessagesQuery)).WithArgs(int64(42)).WillReturnRows(offlineAccessRows().
		AddRow(int64(9007199254740993), "legacy-direct", int64(9007199254740994), 0, 0, 42, 1, 1, "legacy user content", now).
		AddRow(int64(9007199254740995), "bot-card", int64(9007199254740994), 2, int64(9007199254740996), groupID, 2, model.MessageContentTaskCard, card, now))
	mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(groupID, int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(int64(200)))
	teamCalls := 0
	impl.teamClient = teamCheckFunc(func(ctx context.Context, req *userpb.CheckTeamMemberRequest) error {
		teamCalls++
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 200 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != flow.token {
			t.Error("production IM lost the original Bearer or current team scope")
		}
		return nil
	})
	response, _ := flow.request(t, http.MethodGet, "/api/v1/message/offline?user_id=999", "", true)
	messages := legacyOfflineFlowMessages(t, response)
	if len(messages) != 2 {
		t.Fatalf("offline message count = %d", len(messages))
	}
	user, bot := messages[0], messages[1]
	if user.ID != "9007199254740993" || user.FromID != "9007199254740994" || user.ToID != "42" || user.SenderType != 1 || user.InitiatorID != "0" || user.MsgID != "legacy-direct" || user.Content != "legacy user content" || !user.CreatedAt.Equal(now) {
		t.Fatal("legacy user identity, body or timestamp did not survive HTTP/RPC conversion")
	}
	if bot.ID != "9007199254740995" || bot.FromID != "9007199254740994" || bot.InitiatorID != "9007199254740996" || bot.ToID != "9007199254740997" || bot.SenderType != 2 || bot.ChatType != 2 || bot.ContentType != int(model.MessageContentTaskCard) || bot.MsgID != "bot-card" || bot.Content != card || !bot.CreatedAt.Equal(now) {
		t.Fatal("bot card or large string identity did not survive HTTP/RPC conversion")
	}
	if flow.rpcCalls.Load() != 1 || teamCalls != 1 {
		t.Fatalf("RPC calls = %d, team checks = %d", flow.rpcCalls.Load(), teamCalls)
	}
}

func TestLegacyOfflineHTTPFiltersRevokedTeamAndRestoresRetainedMessage(t *testing.T) {
	impl, mock := testIMServer(t)
	flow := newLegacyOfflineFlow(t, impl)
	teamCalls := 0
	impl.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error {
		teamCalls++
		if teamCalls == 1 {
			return status.Error(codes.PermissionDenied, "left team")
		}
		return nil
	})
	for pull := 0; pull < 2; pull++ {
		// The group-members row still exists; only current User membership changes.
		// The same stored delivery must be queried again without any pull DELETE.
		mock.ExpectQuery(regexp.QuoteMeta(offlineMessagesQuery)).WithArgs(int64(42)).WillReturnRows(offlineAccessRows().
			AddRow(10, "direct", 7, 1, 0, 42, 1, 1, "direct content", time.Now()).
			AddRow(11, "retained-team-bot", 8, 2, 42, 302, 2, 1, "retained bot content", time.Now()))
		mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(302), int64(42), 1).
			WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(int64(200)))
		response, _ := flow.request(t, http.MethodGet, "/api/v1/message/offline", "", true)
		messages := legacyOfflineFlowMessages(t, response)
		if len(messages) != pull+1 || messages[0].MsgID != "direct" {
			t.Fatalf("pull %d returned %d messages", pull, len(messages))
		}
		if pull == 1 && (messages[1].ID != "11" || messages[1].MsgID != "retained-team-bot" || messages[1].SenderType != 2 || messages[1].Content != "retained bot content") {
			t.Fatal("rejoined member did not recover the retained bot delivery")
		}
	}
	if teamCalls != 2 || flow.rpcCalls.Load() != 2 {
		t.Fatal("legacy pull reused authorization across requests")
	}
}

func TestLegacyOfflineHTTPAuthorizationFailureReturnsNoPartialBody(t *testing.T) {
	impl, mock := testIMServer(t)
	flow := newLegacyOfflineFlow(t, impl)
	impl.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error {
		return status.Error(codes.Unavailable, "private upstream detail")
	})
	mock.ExpectQuery(regexp.QuoteMeta(offlineMessagesQuery)).WithArgs(int64(42)).WillReturnRows(offlineAccessRows().
		AddRow(10, "direct", 7, 1, 0, 42, 1, 1, "partial-body-must-not-appear", time.Now()).
		AddRow(11, "team", 7, 1, 0, 302, 2, 1, "unchecked-team-body", time.Now()))
	mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(302), int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(int64(200)))
	response, wire := flow.request(t, http.MethodGet, "/api/v1/message/offline", "", true)
	if response.Code != errcode.ErrInternal || len(response.Data) != 0 || strings.Contains(wire, "partial-body-must-not-appear") || strings.Contains(wire, "unchecked-team-body") || strings.Contains(wire, "private upstream detail") {
		t.Fatal("temporary authorization failure returned partial content or private error")
	}
	if flow.rpcCalls.Load() != 1 {
		t.Fatal("authorization failure bypassed the production IM RPC")
	}
}

func TestLegacyOfflineHTTPAckIsExplicitScopedAndIdempotent(t *testing.T) {
	impl, mock := testIMServer(t)
	flow := newLegacyOfflineFlow(t, impl)
	for _, affected := range []int64{2, 0} {
		mock.ExpectBegin()
		mock.ExpectExec(regexp.QuoteMeta("DELETE FROM `offline_messages` WHERE user_id = ? AND message_id IN (?,?)")).
			WithArgs(int64(42), int64(9007199254740993), int64(9007199254740994)).
			WillReturnResult(sqlmock.NewResult(0, affected))
		mock.ExpectCommit()
		response, _ := flow.request(t, http.MethodPost, "/api/v1/message/offline/ack", `{"user_id":"999","message_ids":["9007199254740993","9007199254740994"]}`, true)
		if response.Code != errcode.Success {
			t.Fatalf("explicit ACK code = %d", response.Code)
		}
	}
	if flow.rpcCalls.Load() != 2 {
		t.Fatal("explicit ACK did not reach IM twice")
	}
	for _, body := range []string{`{"message_ids":["oops"]}`, `{"message_ids":[9007199254740993]}`} {
		response, _ := flow.request(t, http.MethodPost, "/api/v1/message/offline/ack", body, true)
		if response.Code != errcode.ErrBadRequest {
			t.Fatalf("invalid ACK code = %d", response.Code)
		}
	}
	for _, request := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/message/offline", ""},
		{http.MethodPost, "/api/v1/message/offline/ack", `{"message_ids":["9007199254740993"]}`},
	} {
		response, _ := flow.request(t, request.method, request.path, request.body, false)
		if response.Code != errcode.ErrUnAuth {
			t.Fatalf("missing Bearer code = %d", response.Code)
		}
	}
	if flow.rpcCalls.Load() != 2 {
		t.Fatal("invalid ACK or missing Bearer reached IM/SQL")
	}
}
