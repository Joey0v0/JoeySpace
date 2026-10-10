package ws

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/repository"
	"github.com/yjydist/go-im/internal/rpcauth"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Only the production write pump is started. Its heartbeat stays local even if
// a slow test reaches the heartbeat interval; no real Redis client is created.
type notificationSocketFlowRedis struct{ repository.RedisRepository }

func (notificationSocketFlowRedis) SetOnline(context.Context, int64, string, time.Duration) error {
	return nil
}
func (notificationSocketFlowRedis) SetOnlineLease(context.Context, int64, string, string, time.Duration) error {
	return nil
}
func (notificationSocketFlowRedis) RefreshOnlineLease(context.Context, int64, string, time.Duration) (bool, error) {
	return true, nil
}
func (notificationSocketFlowRedis) DelOnlineLease(context.Context, int64, string) error { return nil }

type notificationSocketFlow struct {
	event       model.TaskNotificationEvent
	wire        []byte
	client      *Client
	peer        *websocket.Conn
	httpClient  *http.Client
	url         string
	userCode    atomic.Int32
	userCalls   atomic.Int32
	pumpDone    chan struct{}
	pumpStarted bool
}

func newNotificationSocketFlow(t *testing.T) *notificationSocketFlow {
	t.Helper()
	f := &notificationSocketFlow{
		event: model.TaskNotificationEvent{Version: 1, Type: model.TaskNotificationEventType,
			NotificationID: math.MaxInt64, TeamID: math.MaxInt64 - 1, RecipientID: 42},
		pumpDone: make(chan struct{}),
	}
	var err error
	f.wire, err = json.Marshal(f.event)
	if err != nil {
		t.Fatal(err)
	}

	// Upgrade a real loopback socket without Client.Start, readPump or Hub.Run.
	accepted := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{HandshakeTimeout: 3 * time.Second}
	socketServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		accepted <- conn
	}))
	t.Cleanup(func() {
		socketServer.Close()
		// Also release an accepted socket if setup failed before taking it.
		select {
		case conn := <-accepted:
			_ = conn.Close()
		default:
		}
	})
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	f.peer, _, err = dialer.DialContext(ctx, "ws"+strings.TrimPrefix(socketServer.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.peer.Close() })
	var serverConn *websocket.Conn
	select {
	case serverConn = <-accepted:
	case <-ctx.Done():
		t.Fatal("WebSocket upgrade did not provide the server connection")
	}
	hub := NewHub(zap.NewNop())
	f.client = NewClient(f.event.RecipientID, "private-socket-flow-token", serverConn, hub,
		nil, notificationSocketFlowRedis{}, nil, "unused-flow-address", zap.NewNop())
	hub.mu.Lock()
	hub.clients[f.event.RecipientID] = f.client
	hub.mu.Unlock()
	t.Cleanup(func() {
		f.client.Close()
		if f.pumpStarted {
			select {
			case <-f.pumpDone:
			case <-time.After(3 * time.Second):
				t.Error("production WebSocket write pump did not stop")
			}
		}
	})

	teams := notificationTeamsStub(func(ctx context.Context, req *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
		f.userCalls.Add(1)
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != f.event.TeamID || len(md) != 1 ||
			len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer private-socket-flow-token" {
			t.Error("TLS delivery did not authorize the current connection and event team")
		}
		if code := codes.Code(f.userCode.Load()); code != codes.OK {
			return nil, status.Error(code, "private authority failure detail")
		}
		return &userpb.CheckTeamMemberResponse{UserId: f.event.RecipientID, Role: 2}, nil
	})
	serverFiles, pushFiles := notificationTLSFiles(t)
	serverTLS, err := rpcauth.NewNotificationServerTLSConfig(serverFiles, notificationPushName)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(NewTaskNotificationHandler(hub, teams, notificationPushName))
	server.TLS = serverTLS
	server.StartTLS()
	t.Cleanup(server.Close)
	clientTLS, err := rpcauth.NewNotificationClientTLSConfig(pushFiles, "ws.joeyspace.internal")
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{TLSClientConfig: clientTLS}
	t.Cleanup(transport.CloseIdleConnections)
	// This exercises the TLS handler with the production TLS configuration,
	// not Push's sender (the ws package cannot import push without a cycle).
	f.httpClient = &http.Client{Transport: transport, Timeout: 3 * time.Second}
	f.url = server.URL + model.TaskNotificationPushPath
	return f
}

func (f *notificationSocketFlow) post(t *testing.T, userCode codes.Code, wantStatus int, wantOutcome string, wantQueue int) {
	t.Helper()
	f.userCode.Store(int32(userCode))
	before := f.userCalls.Load()
	// Every manual retry sends the original bytes and notification identity.
	response, err := f.httpClient.Post(f.url, "application/json", bytes.NewReader(f.wire))
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("delivery response read=%v close=%v", readErr, closeErr)
	}
	if response.StatusCode != wantStatus || f.userCalls.Load() != before+1 || len(f.client.send) != wantQueue {
		t.Fatalf("status=%d calls=%d queue=%d body=%s", response.StatusCode,
			f.userCalls.Load()-before, len(f.client.send), body)
	}
	if wantOutcome != "" {
		var delivery model.TaskNotificationDelivery
		if err := json.Unmarshal(body, &delivery); err != nil || delivery.Validate(f.event.NotificationID) != nil || delivery.Outcome != wantOutcome {
			t.Fatalf("delivery=%s error=%v", body, err)
		}
	}
	for _, private := range []string{"private", "recipient", "content", "task_id", "unused-flow-address"} {
		if bytes.Contains(body, []byte(private)) {
			t.Fatalf("delivery response leaked %q: %s", private, body)
		}
	}
	select {
	case <-f.client.closeCh:
		t.Fatal("delivery attempt closed the existing chat connection")
	default:
	}
}

func (f *notificationSocketFlow) readQueuedHint(t *testing.T) {
	t.Helper()
	f.pumpStarted = true
	go func() {
		defer close(f.pumpDone)
		f.client.writePump()
	}()
	if err := f.peer.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	messageType, wire, err := f.peer.ReadMessage()
	if err != nil {
		// Read errors end this test. A timed-out WebSocket is never reused.
		t.Fatal(err)
	}
	if messageType != websocket.TextMessage {
		t.Fatalf("WebSocket message type=%d", messageType)
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(wire, &message); err != nil || len(message) != 2 ||
		string(message["type"]) != `"task_notification_changed"` {
		t.Fatalf("unexpected WebSocket envelope=%s error=%v", wire, err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(message["data"], &data); err != nil || len(data) != 3 ||
		string(data["version"]) != "1" || string(data["notification_id"]) != `"9223372036854775807"` ||
		string(data["team_id"]) != `"9223372036854775806"` {
		t.Fatalf("WebSocket hint lost precise IDs or included extra fields: %s error=%v", wire, err)
	}
	for _, private := range []string{"private", "token", "recipient", "content", "task_id", "unused-flow-address"} {
		if bytes.Contains(wire, []byte(private)) {
			t.Fatalf("actual WebSocket frame leaked %q: %s", private, wire)
		}
	}
}

func TestTaskNotificationSocketFlowTransientFailureThenManualRetryWritesMinimalFrame(t *testing.T) {
	f := newNotificationSocketFlow(t)
	// The pump is deliberately paused: queue length proves no failed hint was
	// enqueued without relying on read timeouts or scheduler-dependent sleeps.
	f.post(t, codes.Unavailable, http.StatusServiceUnavailable, "", 0)
	f.post(t, codes.OK, http.StatusOK, model.TaskNotificationQueued, 1)
	f.readQueuedHint(t)
}

func TestTaskNotificationSocketFlowDeniedHintsPreserveConnectionForAuthorizedRecovery(t *testing.T) {
	f := newNotificationSocketFlow(t)
	f.post(t, codes.PermissionDenied, http.StatusOK, model.TaskNotificationDenied, 0)
	f.post(t, codes.Unauthenticated, http.StatusOK, model.TaskNotificationDenied, 0)
	// Only a later successful check of this same connection permits the frame;
	// neither denied attempt created a hidden queued hint or closed the socket.
	f.post(t, codes.OK, http.StatusOK, model.TaskNotificationQueued, 1)
	f.readQueuedHint(t)
}
