package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type offlineMessagesClient struct {
	pb.IMClient
	call    func(context.Context) (*pb.ListOfflineMessagesResponse, error)
	ackCall func(context.Context, *pb.AckOfflineMessagesRequest) error
}

func (c offlineMessagesClient) ListOfflineMessages(ctx context.Context, _ *pb.ListOfflineMessagesRequest, _ ...grpc.CallOption) (*pb.ListOfflineMessagesResponse, error) {
	return c.call(ctx)
}

func (c offlineMessagesClient) AckOfflineMessages(ctx context.Context, req *pb.AckOfflineMessagesRequest, _ ...grpc.CallOption) (*pb.AckOfflineMessagesResponse, error) {
	if err := c.ackCall(ctx, req); err != nil {
		return nil, err
	}
	return &pb.AckOfflineMessagesResponse{}, nil
}

func offlineMessagesRequest(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/message/offline", nil)
	if token != "" {
		r.Header.Set("Authorization", token)
	}
	return r
}

func TestOfflineMessagesHTTPForwardsTokenAndKeepsStringIDs(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 123000000, time.UTC)
	client := offlineMessagesClient{call: func(ctx context.Context) (*pb.ListOfflineMessagesResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer test-token" {
			t.Fatalf("authorization = %v", got)
		}
		return &pb.ListOfflineMessagesResponse{Messages: []*pb.OfflineMessage{{
			Id: 9007199254740993, MsgId: "m1", FromId: 9007199254740994, ToId: 9007199254740995,
			ChatType: 2, ContentType: 1, Content: "hello", CreatedAt: timestamppb.New(now),
		}}}, nil
	}}
	w := httptest.NewRecorder()
	listOfflineMessagesHandler(client)(w, offlineMessagesRequest("Bearer test-token"))
	var result struct {
		Code int `json:"code"`
		Data []struct {
			ID        string `json:"id"`
			FromID    string `json:"from_id"`
			ToID      string `json:"to_id"`
			CreatedAt string `json:"created_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || result.Code != 0 || len(result.Data) != 1 || result.Data[0].ID != "9007199254740993" || result.Data[0].FromID != "9007199254740994" || result.Data[0].ToID != "9007199254740995" || result.Data[0].CreatedAt != now.Format(time.RFC3339Nano) {
		t.Fatalf("response: %d %s", w.Code, w.Body.String())
	}
}

func TestOfflineMessagesHTTPPreservesBotAndLegacySenderMetadata(t *testing.T) {
	content, err := model.EncodeTaskCreatedCard(9007199254740993, "已确认标题")
	if err != nil {
		t.Fatal(err)
	}
	client := offlineMessagesClient{call: func(context.Context) (*pb.ListOfflineMessagesResponse, error) {
		return &pb.ListOfflineMessagesResponse{Messages: []*pb.OfflineMessage{
			{Id: 9, MsgId: "bot", FromId: 42, SenderType: 2, InitiatorId: 9007199254740995, ContentType: int32(model.MessageContentTaskCard), Content: content},
			{Id: 8, MsgId: "legacy", FromId: 42},
		}}, nil
	}}
	w := httptest.NewRecorder()
	listOfflineMessagesHandler(client)(w, offlineMessagesRequest("Bearer token"))
	var result struct {
		Data []struct {
			SenderType  int32  `json:"sender_type"`
			Initiator   string `json:"initiator_id"`
			From        string `json:"from_id"`
			ContentType int32  `json:"content_type"`
			Content     string `json:"content"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || len(result.Data) != 2 {
		t.Fatalf("invalid offline response: %s err=%v", w.Body, err)
	}
	bot, user := result.Data[0], result.Data[1]
	if bot.SenderType != 2 || bot.Initiator != "9007199254740995" || bot.From != "42" || user.SenderType != 1 || user.Initiator != "0" {
		t.Fatalf("sender metadata changed: %s", w.Body)
	}
	if bot.ContentType != int32(model.MessageContentTaskCard) || bot.Content != content {
		t.Fatalf("HTTP offline changed task result: %s", w.Body)
	}
}

func TestOfflineMessagesHTTPRejectsBadTokenAndMapsRPCFailure(t *testing.T) {
	client := offlineMessagesClient{call: func(context.Context) (*pb.ListOfflineMessagesResponse, error) {
		t.Fatal("invalid token reached IM RPC")
		return nil, nil
	}}
	for _, token := range []string{"", "Basic token"} {
		w := httptest.NewRecorder()
		listOfflineMessagesHandler(client)(w, offlineMessagesRequest(token))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: %d %s", token, w.Code, w.Body.String())
		}
	}
	for _, tc := range []struct {
		code codes.Code
		want int
	}{{codes.Unauthenticated, 401}, {codes.Unavailable, 503}, {codes.DeadlineExceeded, 504}, {codes.Internal, 502}} {
		client := offlineMessagesClient{call: func(context.Context) (*pb.ListOfflineMessagesResponse, error) {
			return nil, status.Error(tc.code, "private detail")
		}}
		w := httptest.NewRecorder()
		listOfflineMessagesHandler(client)(w, offlineMessagesRequest("Bearer token"))
		if w.Code != tc.want || strings.Contains(w.Body.String(), "private detail") {
			t.Fatalf("RPC %v: %d %s", tc.code, w.Code, w.Body.String())
		}
	}
}

func TestOfflineMessagesHTTPReturnsEmptyArray(t *testing.T) {
	client := offlineMessagesClient{call: func(context.Context) (*pb.ListOfflineMessagesResponse, error) {
		return &pb.ListOfflineMessagesResponse{}, nil
	}}
	w := httptest.NewRecorder()
	listOfflineMessagesHandler(client)(w, offlineMessagesRequest("Bearer token"))
	if !strings.Contains(w.Body.String(), `"data":[]`) {
		t.Fatalf("empty response: %s", w.Body.String())
	}
}

func newOfflineAckRequest(body, token string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/message/offline/ack", strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", token)
	}
	return r
}

func TestOfflineAckHTTPForwardsExactIDsAndToken(t *testing.T) {
	called := 0
	client := offlineMessagesClient{ackCall: func(ctx context.Context, req *pb.AckOfflineMessagesRequest) error {
		called++
		md, _ := metadata.FromOutgoingContext(ctx)
		if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer test-token" {
			t.Fatalf("authorization = %v", got)
		}
		if ids := req.GetMessageIds(); len(ids) != 2 || ids[0] != 9007199254740993 || ids[1] != 9007199254740994 {
			t.Fatalf("message IDs = %v", ids)
		}
		return nil
	}}
	w := httptest.NewRecorder()
	ackOfflineMessagesHandler(client)(w, newOfflineAckRequest(`{"message_ids":["9007199254740993","9007199254740994"]}`, "Bearer test-token"))
	if w.Code != http.StatusOK || called != 1 || !strings.Contains(w.Body.String(), `"code":0`) {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, called, w.Body.String())
	}
}

func TestOfflineAckHTTPRejectsInvalidBodyBeforeRPC(t *testing.T) {
	client := offlineMessagesClient{ackCall: func(context.Context, *pb.AckOfflineMessagesRequest) error {
		t.Fatal("invalid request reached IM RPC")
		return nil
	}}
	tooMany := `{"message_ids":[` + strings.Repeat(`"1",`, 1000) + `"1"]}`
	for _, body := range []string{
		`{"message_ids":[]}`, `{"message_ids":[9007199254740993]}`,
		`{"message_ids":["0"]}`, `{"message_ids":["9223372036854775808"]}`,
		`{"message_ids":["1"]}{}`, tooMany,
	} {
		w := httptest.NewRecorder()
		ackOfflineMessagesHandler(client)(w, newOfflineAckRequest(body, "Bearer test-token"))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d result=%s", body[:min(len(body), 80)], w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	ackOfflineMessagesHandler(client)(w, newOfflineAckRequest(`{"message_ids":["1"]}`, ""))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: %d %s", w.Code, w.Body.String())
	}
}

func TestOfflineAckHTTPMapsRPCFailure(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{{codes.InvalidArgument, 400}, {codes.Unauthenticated, 401}, {codes.Unavailable, 503}, {codes.DeadlineExceeded, 504}, {codes.Internal, 502}} {
		client := offlineMessagesClient{ackCall: func(context.Context, *pb.AckOfflineMessagesRequest) error {
			return status.Error(tc.code, "private detail")
		}}
		w := httptest.NewRecorder()
		ackOfflineMessagesHandler(client)(w, newOfflineAckRequest(`{"message_ids":["7"]}`, "Bearer token"))
		if w.Code != tc.want || strings.Contains(w.Body.String(), "private detail") {
			t.Fatalf("RPC %v: %d %s", tc.code, w.Code, w.Body.String())
		}
	}
}
