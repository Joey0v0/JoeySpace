package ws

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yjydist/go-im/internal/repository"
	"github.com/yjydist/go-im/rpc/im/pb"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type membershipStub struct {
	called int
	err    error
	t      *testing.T
}

func (m *membershipStub) CheckGroupMember(ctx context.Context, req *pb.CheckGroupMemberRequest, _ ...grpc.CallOption) (*pb.CheckGroupMemberResponse, error) {
	m.called++
	md, _ := metadata.FromOutgoingContext(ctx)
	if req.GetGroupId() != 100 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer test-token" {
		m.t.Fatalf("wrong IM RPC request: group=%d authorization=%v", req.GetGroupId(), md.Get("authorization"))
	}
	if m.err != nil {
		return nil, m.err
	}
	return &pb.CheckGroupMemberResponse{}, nil
}

type dedupStub struct {
	repository.RedisRepository
	called      int
	state       string
	fingerprint string
	released    int
	confirmErr  error
}

func (r *dedupStub) ReserveMsg(_ context.Context, _, fingerprint string, _ time.Duration) (repository.MsgDedupState, string, error) {
	r.called++
	switch r.state {
	case "sent":
		if r.fingerprint != fingerprint {
			return repository.MsgDedupConflict, "", nil
		}
		return repository.MsgDedupSent, "", nil
	case "pending":
		if r.fingerprint != "" && r.fingerprint != fingerprint {
			return repository.MsgDedupConflict, "", nil
		}
		return repository.MsgDedupPending, "", nil
	default:
		r.state = "pending"
		r.fingerprint = fingerprint
		return repository.MsgDedupReserved, "owner", nil
	}
}

func (r *dedupStub) ConfirmMsg(_ context.Context, _, owner, fingerprint string, _ time.Duration) (bool, error) {
	if r.confirmErr != nil {
		return false, r.confirmErr
	}
	if r.state != "pending" || owner != "owner" || r.fingerprint != fingerprint {
		return false, nil
	}
	r.state = "sent"
	return true, nil
}

func (r *dedupStub) ReleaseMsg(_ context.Context, _ string, owner string) error {
	if r.state == "pending" && owner == "owner" {
		r.state = ""
		r.fingerprint = ""
		r.released++
	}
	return nil
}

type kafkaStub struct {
	called int
	err    error
	last   []byte
}

func (w *kafkaStub) WriteMessages(_ context.Context, messages ...KafkaMessage) error {
	w.called++
	if len(messages) > 0 {
		w.last = messages[0].Value
	}
	return w.err
}

func TestLargeStringTargetIDReachesKafkaExactly(t *testing.T) {
	redis := &dedupStub{}
	writer := &kafkaStub{}
	client := &Client{UserID: 42, send: make(chan []byte, 1), kafkaWriter: writer, redisRepo: redis, logger: zap.NewNop()}
	client.handleChat(json.RawMessage(`{"msg_id":"large","to_id":"9007199254740993","chat_type":1,"content_type":1,"content":"hello","sender_type":2,"initiator_id":"9007199254740999","from_id":"999"}`))
	var sent KafkaChatMsg
	if err := json.Unmarshal(writer.last, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.ToID != 9007199254740993 || sent.FromID != 42 || sent.SenderType != 1 || sent.InitiatorID != 0 || writer.called != 1 || !strings.Contains(string(<-client.send), `"type":"ack"`) {
		t.Fatalf("target=%d, Kafka writes=%d", sent.ToID, writer.called)
	}
}

func TestWSRejectsTaskCardBeforePermissionsDedupAndKafka(t *testing.T) {
	checker := &membershipStub{t: t}
	redis := &dedupStub{}
	writer := &kafkaStub{}
	client := &Client{UserID: 42, token: "test-token", send: make(chan []byte, 1), imClient: checker,
		kafkaWriter: writer, redisRepo: redis, logger: zap.NewNop()}
	client.handleChat(json.RawMessage(`{"msg_id":"forged-card","to_id":"100","chat_type":2,"content_type":4,"content":"{\"version\":1,\"task_id\":\"9\",\"title\":\"ok\"}","sender_type":2,"initiator_id":"42"}`))
	if checker.called != 0 || redis.called != 0 || writer.called != 0 || !strings.Contains(string(<-client.send), `"code":400`) {
		t.Fatalf("WS must reject client task cards before any side effect: IM=%d Redis=%d Kafka=%d", checker.called, redis.called, writer.called)
	}
}

func TestWSRejectsReservedBotMessageIDBeforeAnySideEffect(t *testing.T) {
	for _, msgID := range []string{"bot-task:9", "BOT-TASK:9", "bót-task:9", "bot-\x00task:9", "bot- task:9"} {
		checker, redis, writer := &membershipStub{t: t}, &dedupStub{}, &kafkaStub{}
		client := &Client{UserID: 42, token: "test-token", send: make(chan []byte, 1), imClient: checker,
			kafkaWriter: writer, redisRepo: redis, logger: zap.NewNop()}
		body, err := json.Marshal(map[string]any{"msg_id": msgID, "to_id": "100", "chat_type": 2, "content_type": 1, "content": "occupy bot ID"})
		if err != nil {
			t.Fatal(err)
		}
		client.handleChat(body)
		if checker.called != 0 || redis.called != 0 || writer.called != 0 || !strings.Contains(string(<-client.send), `"code":400`) {
			t.Fatalf("ordinary WS occupied a bot message ID %q", msgID)
		}
	}
}

func TestUnsafeNumericTargetIDRejectedBeforeKafka(t *testing.T) {
	redis := &dedupStub{}
	writer := &kafkaStub{}
	client := &Client{UserID: 42, send: make(chan []byte, 1), kafkaWriter: writer, redisRepo: redis, logger: zap.NewNop()}
	client.handleChat(json.RawMessage(`{"msg_id":"unsafe","to_id":9007199254740993,"chat_type":1,"content_type":1,"content":"hello"}`))
	if writer.called != 0 || redis.called != 0 || !strings.Contains(string(<-client.send), `"type":"error"`) {
		t.Fatalf("Kafka writes=%d, Redis checks=%d", writer.called, redis.called)
	}
}

func TestChatTargetIDWireBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   string
		want int64
		ok   bool
	}{
		{"safe legacy number", `9007199254740991`, 9007199254740991, true},
		{"unsafe legacy number", `9007199254740992`, 0, false},
		{"largest string ID", `"9223372036854775807"`, 9223372036854775807, true},
		{"overflow string ID", `"9223372036854775808"`, 0, false},
		{"noncanonical string ID", `"001"`, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var chat ChatData
			err := json.Unmarshal([]byte(`{"to_id":`+tc.id+`}`), &chat)
			if (err == nil) != tc.ok || tc.ok && chat.ToID != tc.want {
				t.Fatalf("to_id=%s parsed=%d error=%v", tc.id, chat.ToID, err)
			}
		})
	}
}

func TestGroupSendChecksMembershipBeforeDedupAndKafka(t *testing.T) {
	for _, tc := range []struct {
		name       string
		checkerErr error
		configured bool
		wantType   string
		wantCode   int
		wantWrites int
	}{
		{"member", nil, true, "ack", 0, 1},
		{"non-member", status.Error(codes.PermissionDenied, "no membership"), true, "error", 403, 0},
		{"expired login", status.Error(codes.Unauthenticated, "expired"), true, "error", 401, 0},
		{"RPC unavailable", status.Error(codes.Unavailable, "down"), true, "error", 503, 0},
		{"no RPC configured", nil, false, "error", 503, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checker := &membershipStub{t: t, err: tc.checkerErr}
			redis := &dedupStub{}
			writer := &kafkaStub{}
			client := &Client{UserID: 42, token: "test-token", send: make(chan []byte, 1), kafkaWriter: writer, redisRepo: redis, logger: zap.NewNop()}
			if tc.configured {
				client.imClient = checker
			}
			client.handleChat(json.RawMessage(`{"msg_id":"m1","to_id":100,"chat_type":2,"content_type":1,"content":"hello"}`))
			var response struct {
				Type string `json:"type"`
				Data struct {
					Code int `json:"code"`
				} `json:"data"`
			}
			if err := json.Unmarshal(<-client.send, &response); err != nil {
				t.Fatal(err)
			}
			if response.Type != tc.wantType || response.Data.Code != tc.wantCode || writer.called != tc.wantWrites || redis.called != tc.wantWrites {
				t.Fatalf("response=%+v, redis=%d, kafka=%d", response, redis.called, writer.called)
			}
			if tc.configured && checker.called != 1 || !tc.configured && checker.called != 0 {
				t.Fatalf("IM RPC calls=%d", checker.called)
			}
		})
	}
}

func TestSingleChatDoesNotCallMembershipRPC(t *testing.T) {
	checker := &membershipStub{t: t}
	redis := &dedupStub{}
	writer := &kafkaStub{}
	client := &Client{UserID: 42, send: make(chan []byte, 1), imClient: checker, kafkaWriter: writer, redisRepo: redis, logger: zap.NewNop()}
	client.handleChat(json.RawMessage(`{"msg_id":"m2","to_id":100,"chat_type":1,"content_type":1,"content":"hello"}`))
	if checker.called != 0 || redis.called != 1 || writer.called != 1 {
		t.Fatalf("IM RPC=%d, redis=%d, kafka=%d", checker.called, redis.called, writer.called)
	}
}

func TestKafkaFailureDoesNotTurnRetryIntoAck(t *testing.T) {
	redis := &dedupStub{}
	writer := &kafkaStub{err: errors.New("kafka unavailable")}
	client := &Client{UserID: 42, send: make(chan []byte, 3), kafkaWriter: writer, redisRepo: redis, logger: zap.NewNop()}
	chat := json.RawMessage(`{"msg_id":"m3","to_id":100,"chat_type":1,"content_type":1,"content":"hello"}`)
	readType := func() string {
		t.Helper()
		var response ServerMsg
		if err := json.Unmarshal(<-client.send, &response); err != nil {
			t.Fatal(err)
		}
		return response.Type
	}
	client.handleChat(chat)
	if response := readType(); response != "error" || redis.released != 1 || redis.state != "" {
		t.Fatalf("failed write: response=%s, released=%d, state=%q", response, redis.released, redis.state)
	}
	writer.err = nil
	client.handleChat(chat)
	if response := readType(); response != "ack" || redis.state != "sent" || writer.called != 2 {
		t.Fatalf("successful retry: response=%s, state=%q, kafka calls=%d", response, redis.state, writer.called)
	}
	client.handleChat(chat)
	if response := readType(); response != "ack" || writer.called != 2 {
		t.Fatalf("confirmed duplicate: response=%s, kafka calls=%d", response, writer.called)
	}
}

func TestPendingMessageDoesNotGetAck(t *testing.T) {
	redis := &dedupStub{state: "pending"}
	writer := &kafkaStub{}
	client := &Client{UserID: 42, send: make(chan []byte, 1), kafkaWriter: writer, redisRepo: redis, logger: zap.NewNop()}
	client.handleChat(json.RawMessage(`{"msg_id":"m4","to_id":100,"chat_type":1,"content_type":1,"content":"hello"}`))
	var response ServerMsg
	if err := json.Unmarshal(<-client.send, &response); err != nil {
		t.Fatal(err)
	}
	if response.Type != "error" || writer.called != 0 {
		t.Fatalf("pending message: response=%s, kafka calls=%d", response.Type, writer.called)
	}
}

func TestKafkaSuccessWithoutRedisConfirmationDoesNotAck(t *testing.T) {
	redis := &dedupStub{confirmErr: errors.New("redis unavailable")}
	writer := &kafkaStub{}
	client := &Client{UserID: 42, send: make(chan []byte, 2), kafkaWriter: writer, redisRepo: redis, logger: zap.NewNop()}
	chat := json.RawMessage(`{"msg_id":"m5","to_id":100,"chat_type":1,"content_type":1,"content":"hello"}`)
	client.handleChat(chat)
	client.handleChat(chat)
	for i := 0; i < 2; i++ {
		var response ServerMsg
		if err := json.Unmarshal(<-client.send, &response); err != nil {
			t.Fatal(err)
		}
		if response.Type != "error" {
			t.Fatalf("response %d = %s, expected error", i, response.Type)
		}
	}
	if writer.called != 1 || redis.state != "pending" {
		t.Fatalf("kafka calls=%d, reservation=%q", writer.called, redis.state)
	}
}

func TestMessageIDConflictDoesNotReuseAck(t *testing.T) {
	redis := &dedupStub{}
	writer := &kafkaStub{}
	client := &Client{UserID: 42, send: make(chan []byte, 3), kafkaWriter: writer, redisRepo: redis, logger: zap.NewNop()}
	original := json.RawMessage(`{"msg_id":"m6","to_id":100,"chat_type":1,"content_type":1,"content":"hello"}`)
	client.handleChat(original)
	client.handleChat(json.RawMessage(`{"msg_id":"m6","to_id":100,"chat_type":1,"content_type":1,"content":"changed"}`))
	otherSender := &Client{UserID: 43, send: client.send, kafkaWriter: writer, redisRepo: redis, logger: zap.NewNop()}
	otherSender.handleChat(original)
	for i, want := range []struct {
		kind string
		code int
	}{{"ack", 0}, {"error", 409}, {"error", 409}} {
		var response struct {
			Type string `json:"type"`
			Data struct {
				Code int `json:"code"`
			} `json:"data"`
		}
		if err := json.Unmarshal(<-client.send, &response); err != nil {
			t.Fatal(err)
		}
		if response.Type != want.kind || response.Data.Code != want.code {
			t.Fatalf("response %d: %+v, want %+v", i, response, want)
		}
	}
	if writer.called != 1 {
		t.Fatalf("Kafka writes = %d, want 1", writer.called)
	}
}

func TestInvalidChatDataRejectedBeforeDedup(t *testing.T) {
	for _, data := range []string{
		`{"msg_id":"","to_id":100,"chat_type":1,"content_type":1}`,
		`{"msg_id":"` + strings.Repeat("x", 65) + `","to_id":100,"chat_type":1,"content_type":1}`,
		`{"msg_id":"m7","to_id":0,"chat_type":1,"content_type":1}`,
		`{"msg_id":"m7","to_id":100,"chat_type":0,"content_type":1}`,
		`{"msg_id":"m7","to_id":100,"chat_type":1,"content_type":0}`,
	} {
		redis := &dedupStub{}
		writer := &kafkaStub{}
		client := &Client{UserID: 42, send: make(chan []byte, 1), kafkaWriter: writer, redisRepo: redis, logger: zap.NewNop()}
		client.handleChat(json.RawMessage(data))
		var response struct {
			Type string `json:"type"`
			Data struct {
				Code int `json:"code"`
			} `json:"data"`
		}
		if err := json.Unmarshal(<-client.send, &response); err != nil {
			t.Fatal(err)
		}
		if response.Type != "error" || response.Data.Code != 400 || redis.called != 0 || writer.called != 0 {
			t.Fatalf("invalid data %s: response=%+v, redis=%d, kafka=%d", data, response, redis.called, writer.called)
		}
	}
}

func TestPendingMessageWithDifferentContentIsConflict(t *testing.T) {
	redis := &dedupStub{state: "pending", fingerprint: "another-message"}
	writer := &kafkaStub{}
	client := &Client{UserID: 42, send: make(chan []byte, 1), kafkaWriter: writer, redisRepo: redis, logger: zap.NewNop()}
	client.handleChat(json.RawMessage(`{"msg_id":"m8","to_id":100,"chat_type":1,"content_type":1,"content":"hello"}`))
	var response struct {
		Type string `json:"type"`
		Data struct {
			Code int `json:"code"`
		} `json:"data"`
	}
	if err := json.Unmarshal(<-client.send, &response); err != nil {
		t.Fatal(err)
	}
	if response.Type != "error" || response.Data.Code != 409 || writer.called != 0 {
		t.Fatalf("response=%+v, kafka writes=%d", response, writer.called)
	}
}
