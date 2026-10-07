package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yjydist/go-im/rpc/im/pb"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type directHTTPClient struct {
	pb.IMClient
	history func(context.Context, *pb.ListDirectMessagesRequest) (*pb.ListDirectMessagesResponse, error)
	count   func(context.Context, *pb.GetDirectUnreadRequest) (*pb.GetDirectUnreadResponse, error)
	mark    func(context.Context, *pb.MarkDirectMessagesReadRequest) (*pb.MarkDirectMessagesReadResponse, error)
}

func (c directHTTPClient) ListDirectMessages(ctx context.Context, req *pb.ListDirectMessagesRequest, _ ...grpc.CallOption) (*pb.ListDirectMessagesResponse, error) {
	return c.history(ctx, req)
}
func (c directHTTPClient) GetDirectUnread(ctx context.Context, req *pb.GetDirectUnreadRequest, _ ...grpc.CallOption) (*pb.GetDirectUnreadResponse, error) {
	return c.count(ctx, req)
}
func (c directHTTPClient) MarkDirectMessagesRead(ctx context.Context, req *pb.MarkDirectMessagesReadRequest, _ ...grpc.CallOption) (*pb.MarkDirectMessagesReadResponse, error) {
	return c.mark(ctx, req)
}

func directHTTPRequest(method, peer, query, body, authorization string) *http.Request {
	r := httptest.NewRequest(method, "/api/v1/direct/"+peer+"/messages"+query, strings.NewReader(body))
	r = pathvar.WithVars(r, map[string]string{"peer_id": peer})
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	return r
}

func assertDirectForward(t *testing.T, ctx context.Context) {
	t.Helper()
	md, _ := metadata.FromOutgoingContext(ctx)
	if !reflect.DeepEqual(md.Get("authorization"), []string{"Bearer original-token"}) || len(md) != 1 {
		t.Fatalf("incorrect RPC metadata: %v", md)
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 3*time.Second {
		t.Fatalf("missing bounded RPC deadline: %v", deadline)
	}
}

func TestDirectHTTPForwardsAndKeepsLargeIDsAsStrings(t *testing.T) {
	const peer = int64(9007199254740993)
	const message = int64(9007199254740995)
	client := directHTTPClient{
		history: func(ctx context.Context, req *pb.ListDirectMessagesRequest) (*pb.ListDirectMessagesResponse, error) {
			assertDirectForward(t, ctx)
			if req.PeerId != peer || req.BeforeMessageId != message+2 || req.Limit != 2 {
				t.Fatalf("history request: %v", req)
			}
			return &pb.ListDirectMessagesResponse{Messages: []*pb.DirectMessage{{Id: message, MsgId: "m1", FromId: peer, ToId: 42, ContentType: 1, Content: "hello"}}, NextBeforeMessageId: message}, nil
		},
		count: func(ctx context.Context, req *pb.GetDirectUnreadRequest) (*pb.GetDirectUnreadResponse, error) {
			assertDirectForward(t, ctx)
			if req.PeerId != peer {
				t.Fatalf("unread request: %v", req)
			}
			return &pb.GetDirectUnreadResponse{PeerId: peer, UnreadCount: message}, nil
		},
		mark: func(ctx context.Context, req *pb.MarkDirectMessagesReadRequest) (*pb.MarkDirectMessagesReadResponse, error) {
			assertDirectForward(t, ctx)
			if req.PeerId != peer || !reflect.DeepEqual(req.MessageIds, []int64{message, message + 1}) {
				t.Fatalf("mark request: %v", req)
			}
			return &pb.MarkDirectMessagesReadResponse{PeerId: peer, MessageIds: req.MessageIds, UnreadCount: 0}, nil
		},
	}
	for _, tc := range []struct {
		name                string
		handler             http.HandlerFunc
		method, query, body string
		want                []string
	}{
		{"history", listDirectMessagesHandler(client), http.MethodGet, "?before_message_id=9007199254740997&limit=2", "", []string{`"id":"9007199254740995"`, `"from_id":"9007199254740993"`, `"next_before_message_id":"9007199254740995"`}},
		{"unread", getDirectUnreadHandler(client), http.MethodGet, "", "", []string{`"peer_id":"9007199254740993"`, `"unread_count":"9007199254740995"`}},
		{"mark", markDirectMessagesReadHandler(client), http.MethodPost, "", `{"message_ids":["9007199254740996","9007199254740995","9007199254740996"]}`, []string{`"message_ids":["9007199254740995","9007199254740996"]`, `"unread_count":"0"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.handler(w, directHTTPRequest(tc.method, "9007199254740993", tc.query, tc.body, "Bearer original-token"))
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			for _, want := range tc.want {
				if !strings.Contains(w.Body.String(), want) {
					t.Fatalf("missing %s in %s", want, w.Body)
				}
			}
		})
	}
}

func TestDirectHTTPRejectsInvalidRequestBeforeRPC(t *testing.T) {
	client := directHTTPClient{
		history: func(context.Context, *pb.ListDirectMessagesRequest) (*pb.ListDirectMessagesResponse, error) {
			t.Fatal("invalid history reached RPC")
			return nil, nil
		},
		count: func(context.Context, *pb.GetDirectUnreadRequest) (*pb.GetDirectUnreadResponse, error) {
			t.Fatal("invalid count reached RPC")
			return nil, nil
		},
		mark: func(context.Context, *pb.MarkDirectMessagesReadRequest) (*pb.MarkDirectMessagesReadResponse, error) {
			t.Fatal("invalid mark reached RPC")
			return nil, nil
		},
	}
	for _, tc := range []struct {
		handler                         http.HandlerFunc
		method, peer, query, body, auth string
		want                            int
	}{
		{listDirectMessagesHandler(client), "GET", "43", "", "", "", 401},
		{listDirectMessagesHandler(client), "GET", "43", "", "", "Basic token", 401},
		{listDirectMessagesHandler(client), "GET", "0", "", "", "Bearer token", 400},
		{listDirectMessagesHandler(client), "GET", "43", "?before_message_id=-1", "", "Bearer token", 400},
		{listDirectMessagesHandler(client), "GET", "43", "?limit=101", "", "Bearer token", 400},
		{listDirectMessagesHandler(client), "GET", "43", "?limit=1&limit=2", "", "Bearer token", 400},
		{listDirectMessagesHandler(client), "GET", "43", "?unknown=1", "", "Bearer token", 400},
		{getDirectUnreadHandler(client), "GET", "43", "?limit=1", "", "Bearer token", 400},
		{markDirectMessagesReadHandler(client), "POST", "43", "", `{"message_ids":[1]}`, "Bearer token", 400},
		{markDirectMessagesReadHandler(client), "POST", "43", "", `{"message_ids":["0"]}`, "Bearer token", 400},
		{markDirectMessagesReadHandler(client), "POST", "43", "", `{"message_ids":["1"],"user_id":"42"}`, "Bearer token", 400},
		{markDirectMessagesReadHandler(client), "POST", "43", "", `{"message_ids":["1"]}{}`, "Bearer token", 400},
	} {
		w := httptest.NewRecorder()
		tc.handler(w, directHTTPRequest(tc.method, tc.peer, tc.query, tc.body, tc.auth))
		if w.Code != tc.want {
			t.Fatalf("%s %s %s: %d %s", tc.method, tc.peer, tc.query, w.Code, w.Body)
		}
	}
}

func TestDirectHTTPRejectsMismatchedRPCResponse(t *testing.T) {
	for _, tc := range []struct {
		handler      http.HandlerFunc
		method, body string
	}{
		{listDirectMessagesHandler(directHTTPClient{history: func(context.Context, *pb.ListDirectMessagesRequest) (*pb.ListDirectMessagesResponse, error) {
			return &pb.ListDirectMessagesResponse{Messages: []*pb.DirectMessage{{Id: 1, FromId: 8, ToId: 9, Content: "private"}}}, nil
		}}), "GET", ""},
		{getDirectUnreadHandler(directHTTPClient{count: func(context.Context, *pb.GetDirectUnreadRequest) (*pb.GetDirectUnreadResponse, error) {
			return &pb.GetDirectUnreadResponse{PeerId: 8}, nil
		}}), "GET", ""},
		{markDirectMessagesReadHandler(directHTTPClient{mark: func(context.Context, *pb.MarkDirectMessagesReadRequest) (*pb.MarkDirectMessagesReadResponse, error) {
			return &pb.MarkDirectMessagesReadResponse{PeerId: 43, MessageIds: []int64{2}}, nil
		}}), "POST", `{"message_ids":["1"]}`},
	} {
		w := httptest.NewRecorder()
		tc.handler(w, directHTTPRequest(tc.method, "43", "", tc.body, "Bearer token"))
		if w.Code != 502 || strings.Contains(w.Body.String(), "private") {
			t.Fatalf("invalid response exposed: %d %s", w.Code, w.Body)
		}
	}
}

func TestDirectHTTPMapsRPCErrorsWithoutLeakingDetails(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{{codes.InvalidArgument, 400}, {codes.Unauthenticated, 401}, {codes.PermissionDenied, 403}, {codes.NotFound, 404}, {codes.Unavailable, 503}, {codes.DeadlineExceeded, 504}, {codes.Internal, 502}} {
		client := directHTTPClient{count: func(context.Context, *pb.GetDirectUnreadRequest) (*pb.GetDirectUnreadResponse, error) {
			return nil, status.Error(tc.code, "private-db")
		}}
		w := httptest.NewRecorder()
		getDirectUnreadHandler(client)(w, directHTTPRequest("GET", "43", "", "", "Bearer token"))
		var result struct {
			Code int             `json:"code"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(w.Body.Bytes(), &result) != nil || w.Code != tc.want || len(result.Data) != 0 || strings.Contains(w.Body.String(), "private-db") {
			t.Fatalf("code %v: %d %s", tc.code, w.Code, w.Body)
		}
	}
}
