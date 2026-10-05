package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/yjydist/go-im/internal/middleware"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/internal/service"
	"github.com/yjydist/go-im/rpc/im/pb"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type historyService struct {
	service.MessageService
	err error
}

type offlineHandlerService struct {
	service.MessageService
	calls int
}

func (s *offlineHandlerService) GetOfflineMessages(context.Context, int64) ([]model.Message, error) {
	s.calls++
	return nil, nil
}

func (s *offlineHandlerService) AckOfflineMessages(context.Context, int64, []int64) error {
	s.calls++
	return nil
}

type offlineHandlerClient struct {
	list func(context.Context, *pb.ListOfflineMessagesRequest) (*pb.ListOfflineMessagesResponse, error)
	ack  func(context.Context, *pb.AckOfflineMessagesRequest) (*pb.AckOfflineMessagesResponse, error)
}

func (s offlineHandlerClient) ListOfflineMessages(ctx context.Context, req *pb.ListOfflineMessagesRequest, _ ...grpc.CallOption) (*pb.ListOfflineMessagesResponse, error) {
	return s.list(ctx, req)
}

func (s offlineHandlerClient) AckOfflineMessages(ctx context.Context, req *pb.AckOfflineMessagesRequest, _ ...grpc.CallOption) (*pb.AckOfflineMessagesResponse, error) {
	return s.ack(ctx, req)
}

func offlineHandlerRequest(h *MessageHandler, method, body string, ctx context.Context, headers http.Header) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, "/api/v1/message/offline", strings.NewReader(body)).WithContext(ctx)
	c.Request.Header = headers
	c.Set(middleware.ContextKeyUserID, int64(42))
	if method == http.MethodPost {
		h.AckOfflineMessages(c)
	} else {
		h.GetOfflineMessages(c)
	}
	return w
}

func offlineTestHeaders() http.Header {
	return http.Header{"Authorization": {"Bearer caller-token"}}
}

func validOfflineTestMessage() *pb.OfflineMessage {
	return &pb.OfflineMessage{Id: 9007199254740993, MsgId: "m1", FromId: 9007199254740994, ToId: 9007199254740996, ChatType: 2, ContentType: 1, Content: "private body", CreatedAt: timestamppb.New(time.Date(2026, 10, 5, 1, 2, 3, 0, time.UTC))}
}

func assertOfflineCode(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	var result struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != http.StatusOK || result.Code != want {
		t.Fatalf("want HTTP200 code=%d, got HTTP%d %s (decode=%v)", want, w.Code, w.Body, err)
	}
	if want != 0 && strings.Contains(w.Body.String(), `"data"`) {
		t.Fatalf("failed request returned data: %s", w.Body)
	}
}

func TestOfflineHandlerReturnsStringIDsAndAcknowledgesThem(t *testing.T) {
	const messageID int64 = 9007199254740993
	bot := validOfflineTestMessage()
	bot.Id, bot.MsgId, bot.SenderType, bot.InitiatorId = messageID+3, "bot", 2, messageID+2
	bot.ContentType, bot.Content = 4, `{"task_id":"9007199254740993"}`
	var ackIDs []int64
	stub := offlineHandlerClient{
		list: func(context.Context, *pb.ListOfflineMessagesRequest) (*pb.ListOfflineMessagesResponse, error) {
			return &pb.ListOfflineMessagesResponse{Messages: []*pb.OfflineMessage{validOfflineTestMessage(), bot}}, nil
		},
		ack: func(_ context.Context, req *pb.AckOfflineMessagesRequest) (*pb.AckOfflineMessagesResponse, error) {
			ackIDs = append(ackIDs, req.MessageIds...)
			return &pb.AckOfflineMessagesResponse{}, nil
		},
	}
	h := &MessageHandler{offlineClient: stub, logger: zap.NewNop()}
	w := offlineHandlerRequest(h, "GET", "", context.Background(), offlineTestHeaders())
	assertOfflineCode(t, w, 0)
	if !strings.Contains(w.Body.String(), `"id":"9007199254740993"`) || !strings.Contains(w.Body.String(), `"from_id":"9007199254740994"`) {
		t.Fatalf("unsafe offline IDs: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"sender_type":1`) || !strings.Contains(w.Body.String(), `"sender_type":2`) || !strings.Contains(w.Body.String(), `"initiator_id":"9007199254740995"`) {
		t.Fatalf("lost offline sender identity: %s", w.Body.String())
	}
	var result struct {
		Data []offlineMessageResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || len(result.Data) != 2 || result.Data[1].Content != bot.Content || result.Data[1].ToID != bot.ToId || !result.Data[1].CreatedAt.Equal(bot.CreatedAt.AsTime()) {
		t.Fatalf("lost offline card/time/recipient: %s", w.Body)
	}
	w = offlineHandlerRequest(h, "POST", `{"message_ids":["9007199254740993"]}`, context.Background(), offlineTestHeaders())
	assertOfflineCode(t, w, 0)
	if !reflect.DeepEqual(ackIDs, []int64{messageID}) {
		t.Fatalf("ack IDs=%v", ackIDs)
	}
}

func TestOfflineAckRejectsInvalidIDsBeforeService(t *testing.T) {
	for _, body := range []string{`{}`, `{"message_ids":null}`, `{"message_ids":[]}`, `{"message_ids":[9007199254740993]}`, `{"message_ids":[null]}`, `{"message_ids":["0"]}`, `{"message_ids":["-1"]}`, `{"message_ids":["oops"]}`, `{"message_ids":["9223372036854775808"]}`, `{"message_ids":["1"]} {}`, `{"message_ids":["1","2"`, `{"message_ids":[` + strings.Repeat(`"1",`, 1000) + `"1"]}`, `{"message_ids":["1"]}` + strings.Repeat(" ", 32*1024)} {
		calls := 0
		h := &MessageHandler{offlineClient: offlineHandlerClient{ack: func(context.Context, *pb.AckOfflineMessagesRequest) (*pb.AckOfflineMessagesResponse, error) {
			calls++
			return &pb.AckOfflineMessagesResponse{}, nil
		}}, logger: zap.NewNop()}
		w := offlineHandlerRequest(h, "POST", body, context.Background(), offlineTestHeaders())
		assertOfflineCode(t, w, errcode.ErrBadRequest)
		if calls != 0 {
			t.Fatalf("invalid IDs called RPC: %s", body)
		}
	}
}

func TestOfflineAckAcceptsUpperBoundAndDuplicates(t *testing.T) {
	ids := make([]string, 1000)
	for i := range ids {
		ids[i] = "9223372036854775807"
	}
	body, err := json.Marshal(offlineAckRequest{MessageIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	h := &MessageHandler{offlineClient: offlineHandlerClient{ack: func(_ context.Context, req *pb.AckOfflineMessagesRequest) (*pb.AckOfflineMessagesResponse, error) {
		calls++
		if len(req.MessageIds) != 1000 {
			t.Fatalf("ACK length=%d", len(req.MessageIds))
		}
		for _, id := range req.MessageIds {
			if id != 9223372036854775807 {
				t.Fatalf("ACK ID=%d", id)
			}
		}
		return &pb.AckOfflineMessagesResponse{}, nil
	}}}
	w := offlineHandlerRequest(h, "POST", string(body), context.Background(), offlineTestHeaders())
	assertOfflineCode(t, w, 0)
	if calls != 1 {
		t.Fatalf("ACK calls=%d", calls)
	}
}

func TestOfflineHandlerRequiresOneBearerWithoutForwardingOtherHeaders(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		for _, headers := range []http.Header{
			{}, {"Authorization": {""}}, {"Authorization": {"Basic token"}}, {"Authorization": {"Bearer"}},
			{"Authorization": {"Bearer "}}, {"Authorization": {"Bearer token extra"}},
			{"Authorization": {"Bearer one", "Bearer two"}}, {"Authorization": {"Bearer one,Bearer two"}},
			{"Authorization": {"Bearer one,two"}}, {"Authorization": {"Bearer token\t"}},
		} {
			calls := 0
			client := offlineHandlerClient{
				list: func(context.Context, *pb.ListOfflineMessagesRequest) (*pb.ListOfflineMessagesResponse, error) {
					calls++
					return &pb.ListOfflineMessagesResponse{}, nil
				},
				ack: func(context.Context, *pb.AckOfflineMessagesRequest) (*pb.AckOfflineMessagesResponse, error) {
					calls++
					return &pb.AckOfflineMessagesResponse{}, nil
				},
			}
			local := &offlineHandlerService{}
			h := &MessageHandler{offlineClient: client, msgService: local}
			w := offlineHandlerRequest(h, method, `{"message_ids":["1"]}`, context.Background(), headers)
			assertOfflineCode(t, w, errcode.ErrUnAuth)
			if calls != 0 || local.calls != 0 {
				t.Fatalf("%s invalid credential called RPC/local=%d/%d", method, calls, local.calls)
			}
		}
	}
}

func TestOfflineHandlerPropagatesCredentialDeadlineAndCancellation(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		for _, mode := range []string{"bounded", "short upstream", "cancelled upstream", "expired upstream"} {
			t.Run(method+"/"+mode, func(t *testing.T) {
				parent := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer service-secret", "x-user-id", "777", "idempotency-key", "secret-key"))
				cancelParent := func() {}
				switch mode {
				case "short upstream":
					parent, cancelParent = context.WithTimeout(parent, 500*time.Millisecond)
				case "cancelled upstream":
					parent, cancelParent = context.WithCancel(parent)
					cancelParent()
				case "expired upstream":
					parent, cancelParent = context.WithDeadline(parent, time.Now().Add(-time.Second))
				}
				defer cancelParent()
				var forwarded context.Context
				calls := 0
				check := func(ctx context.Context) error {
					calls++
					forwarded = ctx
					md, ok := metadata.FromOutgoingContext(ctx)
					if !ok || !reflect.DeepEqual(md, metadata.Pairs("authorization", "Bearer caller-token")) {
						t.Fatalf("unexpected RPC metadata: %v", md)
					}
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) > 3*time.Second {
						t.Fatalf("RPC deadline missing or too long: %v", deadline)
					}
					if mode == "bounded" && time.Until(deadline) < 2*time.Second {
						t.Fatalf("unexpected short deadline: %v", deadline)
					}
					if expected, ok := parent.Deadline(); ok && !deadline.Equal(expected) {
						t.Fatalf("upstream deadline changed: %v -> %v", expected, deadline)
					}
					if err := parent.Err(); err != nil {
						if ctx.Err() != err {
							t.Fatalf("lost cancellation: %v -> %v", err, ctx.Err())
						}
						return status.FromContextError(err).Err()
					}
					return nil
				}
				client := offlineHandlerClient{
					list: func(ctx context.Context, _ *pb.ListOfflineMessagesRequest) (*pb.ListOfflineMessagesResponse, error) {
						if err := check(ctx); err != nil {
							return nil, err
						}
						return &pb.ListOfflineMessagesResponse{}, nil
					},
					ack: func(ctx context.Context, _ *pb.AckOfflineMessagesRequest) (*pb.AckOfflineMessagesResponse, error) {
						if err := check(ctx); err != nil {
							return nil, err
						}
						return &pb.AckOfflineMessagesResponse{}, nil
					},
				}
				headers := offlineTestHeaders()
				headers.Set("X-User-ID", "999")
				headers.Set("Idempotency-Key", "do-not-forward")
				h := &MessageHandler{offlineClient: client}
				w := offlineHandlerRequest(h, method, `{"message_ids":["1"]}`, parent, headers)
				code := 0
				if parent.Err() != nil {
					code = errcode.ErrInternal
				}
				assertOfflineCode(t, w, code)
				if calls != 1 || forwarded.Err() != context.Canceled && forwarded.Err() != context.DeadlineExceeded {
					t.Fatalf("RPC calls=%d child context was not released", calls)
				}
			})
		}
	}
}

func TestOfflineHandlerRPCFailuresNeverFallBackOrExposeError(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		for _, tc := range []struct {
			name         string
			rpcCode      codes.Code
			businessCode int
		}{
			{"bad argument", codes.InvalidArgument, errcode.ErrBadRequest},
			{"unauthenticated", codes.Unauthenticated, errcode.ErrUnAuth},
			{"forbidden", codes.PermissionDenied, errcode.ErrForbidden},
			{"unavailable", codes.Unavailable, errcode.ErrInternal},
			{"deadline", codes.DeadlineExceeded, errcode.ErrInternal},
			{"cancelled", codes.Canceled, errcode.ErrInternal},
			{"unknown", codes.Unknown, errcode.ErrInternal},
			{"internal", codes.Internal, errcode.ErrInternal},
		} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				local := &offlineHandlerService{}
				core, logs := observer.New(zap.DebugLevel)
				client := offlineHandlerClient{
					list: func(context.Context, *pb.ListOfflineMessagesRequest) (*pb.ListOfflineMessagesResponse, error) {
						return &pb.ListOfflineMessagesResponse{Messages: []*pb.OfflineMessage{validOfflineTestMessage()}}, status.Error(tc.rpcCode, "private body caller-token database-secret")
					},
					ack: func(context.Context, *pb.AckOfflineMessagesRequest) (*pb.AckOfflineMessagesResponse, error) {
						return &pb.AckOfflineMessagesResponse{}, status.Error(tc.rpcCode, "private body caller-token database-secret")
					},
				}
				h := &MessageHandler{offlineClient: client, msgService: local, logger: zap.New(core)}
				w := offlineHandlerRequest(h, method, `{"message_ids":["1"]}`, context.Background(), offlineTestHeaders())
				assertOfflineCode(t, w, tc.businessCode)
				if local.calls != 0 || strings.Contains(w.Body.String(), "database-secret") || strings.Contains(w.Body.String(), "caller-token") || strings.Contains(w.Body.String(), "private body") || logs.Len() != 0 {
					t.Fatalf("failure leaked or fell back: local=%d response=%s logs=%d", local.calls, w.Body, logs.Len())
				}
			})
		}
		for _, configured := range []bool{false, true} {
			local := &offlineHandlerService{}
			h := &MessageHandler{msgService: local}
			if configured {
				h.offlineClient = offlineHandlerClient{
					list: func(context.Context, *pb.ListOfflineMessagesRequest) (*pb.ListOfflineMessagesResponse, error) {
						return nil, nil
					},
					ack: func(context.Context, *pb.AckOfflineMessagesRequest) (*pb.AckOfflineMessagesResponse, error) {
						return nil, nil
					},
				}
			}
			w := offlineHandlerRequest(h, method, `{"message_ids":["1"]}`, context.Background(), offlineTestHeaders())
			assertOfflineCode(t, w, errcode.ErrInternal)
			if local.calls != 0 {
				t.Fatalf("%s configured=%v fell back to old service", method, configured)
			}
		}
	}
}

func TestOfflineHandlerRejectsMalformedFullPageWithoutPartialData(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*pb.OfflineMessage)
	}{
		{"message ID", func(m *pb.OfflineMessage) { m.Id = 0 }},
		{"client message ID", func(m *pb.OfflineMessage) { m.MsgId = "" }},
		{"sender ID", func(m *pb.OfflineMessage) { m.FromId = -1 }},
		{"recipient ID", func(m *pb.OfflineMessage) { m.ToId = 0 }},
		{"missing time", func(m *pb.OfflineMessage) { m.CreatedAt = nil }},
		{"invalid time", func(m *pb.OfflineMessage) { m.CreatedAt = &timestamppb.Timestamp{Seconds: 253402300800} }},
		{"invalid nanos", func(m *pb.OfflineMessage) { m.CreatedAt = &timestamppb.Timestamp{Nanos: -1} }},
		{"chat type", func(m *pb.OfflineMessage) { m.ChatType = 258 }},
		{"content type", func(m *pb.OfflineMessage) { m.ContentType = 257 }},
		{"sender type", func(m *pb.OfflineMessage) { m.SenderType = 258 }},
		{"user initiator", func(m *pb.OfflineMessage) { m.SenderType = 1; m.InitiatorId = 7 }},
		{"bot initiator", func(m *pb.OfflineMessage) { m.SenderType = 2; m.InitiatorId = 0 }},
		{"legacy initiator", func(m *pb.OfflineMessage) { m.InitiatorId = -1 }},
		{"nil item", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var invalid *pb.OfflineMessage
			if tc.mutate != nil {
				invalid = validOfflineTestMessage()
				tc.mutate(invalid)
			}
			local := &offlineHandlerService{}
			h := &MessageHandler{msgService: local, offlineClient: offlineHandlerClient{list: func(context.Context, *pb.ListOfflineMessagesRequest) (*pb.ListOfflineMessagesResponse, error) {
				return &pb.ListOfflineMessagesResponse{Messages: []*pb.OfflineMessage{validOfflineTestMessage(), invalid}}, nil
			}}}
			w := offlineHandlerRequest(h, "GET", "", context.Background(), offlineTestHeaders())
			assertOfflineCode(t, w, errcode.ErrInternal)
			if local.calls != 0 || strings.Contains(w.Body.String(), "private body") {
				t.Fatalf("malformed page leaked or fell back: %s", w.Body)
			}
		})
	}
}

func TestOfflineHandlerEmptyMessagesAreJSONArray(t *testing.T) {
	h := &MessageHandler{offlineClient: offlineHandlerClient{list: func(context.Context, *pb.ListOfflineMessagesRequest) (*pb.ListOfflineMessagesResponse, error) {
		return &pb.ListOfflineMessagesResponse{}, nil
	}}}
	w := offlineHandlerRequest(h, "GET", "", context.Background(), offlineTestHeaders())
	assertOfflineCode(t, w, 0)
	if !strings.Contains(w.Body.String(), `"data":[]`) {
		t.Fatalf("empty list=%s", w.Body)
	}
}

func (s historyService) GetHistory(context.Context, int64, int64, int8, int64, int) ([]model.Message, error) {
	return nil, s.err
}

func TestHistoryHandlerMapsMembershipDenial(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int
	}{
		{"non-member", fmt.Errorf("%w: %d", service.ErrBusiness, errcode.ErrGroupNotMember), errcode.ErrGroupNotMember},
		{"team group via legacy endpoint", fmt.Errorf("%w: %d", service.ErrBusiness, errcode.ErrForbidden), errcode.ErrForbidden},
		{"database failure", fmt.Errorf("database disconnected"), errcode.ErrInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("GET", "/api/v1/message/history?target_id=100&chat_type=2", nil)
			c.Set(middleware.ContextKeyUserID, int64(42))
			h := &MessageHandler{msgService: historyService{err: tc.err}, logger: zap.NewNop()}
			h.GetHistory(c)
			if w.Code != 200 || !strings.Contains(w.Body.String(), fmt.Sprintf(`"code":%d`, tc.code)) || strings.Contains(w.Body.String(), `"data"`) {
				t.Fatalf("unexpected history response: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
