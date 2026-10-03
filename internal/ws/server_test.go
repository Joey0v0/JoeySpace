package ws

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/yjydist/go-im/internal/config"
	"github.com/yjydist/go-im/internal/model"
	"go.uber.org/zap"
)

func TestInternalPushSendsExactStringIDsToBrowser(t *testing.T) {
	hub := NewHub(zap.NewNop())
	client := &Client{UserID: 42, send: make(chan []byte, 1), closeCh: make(chan struct{}), hub: hub, logger: zap.NewNop()}
	hub.clients[42] = client
	server := &Server{hub: hub, logger: zap.NewNop()}
	body := `{"user_id":42,"data":{"id":9007199254740993,"msg_id":"m1","from_id":9007199254740995,"to_id":9007199254740997,"chat_type":2,"content_type":1,"content":"hello"}}`
	w := httptest.NewRecorder()
	server.HandleInternalPush(w, httptest.NewRequest(http.MethodPost, "/internal/push", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("push status = %d: %s", w.Code, w.Body.String())
	}
	var delivered struct {
		Type string `json:"type"`
		Data struct {
			ID          string `json:"id"`
			FromID      string `json:"from_id"`
			ToID        string `json:"to_id"`
			SenderType  int8   `json:"sender_type"`
			InitiatorID string `json:"initiator_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(<-client.send, &delivered); err != nil {
		t.Fatal(err)
	}
	if delivered.Type != "chat" || delivered.Data.ID != "9007199254740993" || delivered.Data.FromID != "9007199254740995" || delivered.Data.ToID != "9007199254740997" || delivered.Data.SenderType != 1 || delivered.Data.InitiatorID != "0" {
		t.Fatalf("browser message = %+v", delivered)
	}
}

func TestInternalPushKeepsBotIdentityAndExactInitiatorID(t *testing.T) {
	hub := NewHub(zap.NewNop())
	client := &Client{UserID: 42, send: make(chan []byte, 1), closeCh: make(chan struct{}), hub: hub, logger: zap.NewNop()}
	hub.clients[42] = client
	server := &Server{hub: hub, logger: zap.NewNop()}
	content, err := model.EncodeTaskCreatedCard(9007199254740993, "已确认标题")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(model.Message{ID: 9007199254740993, MsgID: "bot-result", FromID: 42,
		ToID: 100, SenderType: 2, InitiatorID: 9007199254740995, ChatType: 2,
		ContentType: model.MessageContentTaskCard, Content: content})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(PushMsg{UserID: 42, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	server.HandleInternalPush(w, httptest.NewRequest(http.MethodPost, "/internal/push", strings.NewReader(string(body))))
	if w.Code != http.StatusOK {
		t.Fatalf("push status=%d: %s", w.Code, w.Body.String())
	}
	var delivered struct {
		Data BrowserChatMessage `json:"data"`
	}
	if err := json.Unmarshal(<-client.send, &delivered); err != nil {
		t.Fatal(err)
	}
	if delivered.Data.SenderType != 2 || delivered.Data.FromID != "42" || delivered.Data.InitiatorID != "9007199254740995" {
		t.Fatalf("bot identity changed: %+v", delivered.Data)
	}
	if delivered.Data.ContentType != model.MessageContentTaskCard || delivered.Data.Content != content {
		t.Fatalf("WS changed task result: %+v", delivered.Data)
	}
}

func TestNewServerWSRPCAddr(t *testing.T) {
	cfg := &config.Config{
		WSServer: config.WSServerConfig{RPCPort: 9091},
		Kafka: config.KafkaConfig{
			Brokers:   []string{"127.0.0.1:9092"},
			TopicChat: "test",
		},
	}

	t.Run("uses environment in container", func(t *testing.T) {
		t.Setenv("WS_RPC_ADDR", "im-ws:9091")
		t.Setenv("IM_RPC_ADDR", "")
		server := NewServer(cfg, NewHub(zap.NewNop()), nil, zap.NewNop())
		defer server.Close()
		if server.wsRPCAddr != "im-ws:9091" {
			t.Fatalf("expected im-ws:9091, got %q", server.wsRPCAddr)
		}
	})

	t.Run("falls back for local run", func(t *testing.T) {
		t.Setenv("WS_RPC_ADDR", "")
		t.Setenv("IM_RPC_ADDR", "")
		server := NewServer(cfg, NewHub(zap.NewNop()), nil, zap.NewNop())
		defer server.Close()
		if server.wsRPCAddr != "localhost:9091" {
			t.Fatalf("expected localhost:9091, got %q", server.wsRPCAddr)
		}
	})
	t.Run("configures IM RPC client", func(t *testing.T) {
		t.Setenv("IM_RPC_ADDR", "127.0.0.1:9002")
		server := NewServer(cfg, NewHub(zap.NewNop()), nil, zap.NewNop())
		defer server.Close()
		if server.imClient == nil || server.imConn == nil {
			t.Fatal("IM RPC client was not configured")
		}
	})
}

func TestInternalPushReportsQueueFailure(t *testing.T) {
	connections := make(chan *websocket.Conn, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			connections <- conn
		}
	}))
	defer wsServer.Close()
	peer, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	conn := <-connections
	hub := NewHub(zap.NewNop())
	client := &Client{UserID: 42, conn: conn, send: make(chan []byte, 1), closeCh: make(chan struct{}), hub: hub, logger: zap.NewNop()}
	defer client.Close()
	hub.clients[42] = client
	server := &Server{hub: hub, logger: zap.NewNop()}
	request := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		server.HandleInternalPush(w, httptest.NewRequest(http.MethodPost, "/internal/push", strings.NewReader(`{"user_id":42,"data":{"msg_id":"m1"}}`)))
		return w
	}
	if w := request(); w.Code != http.StatusOK || len(client.send) != 1 {
		t.Fatalf("first push: status=%d, queued=%d", w.Code, len(client.send))
	}
	if w := request(); w.Code != http.StatusServiceUnavailable || len(client.send) != 1 {
		t.Fatalf("full queue: status=%d, queued=%d", w.Code, len(client.send))
	}
	if w := request(); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed connection: status=%d", w.Code)
	}
}
