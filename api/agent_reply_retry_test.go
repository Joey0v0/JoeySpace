package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/agent/pb"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type replyRetryFunc func(context.Context, *pb.GetTaskDraftRequest) (*pb.GetTaskDraftResponse, error)

func (f replyRetryFunc) RetryTaskReply(ctx context.Context, req *pb.GetTaskDraftRequest, _ ...grpc.CallOption) (*pb.GetTaskDraftResponse, error) {
	return f(ctx, req)
}

func retryReplyRequest(id, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/agent/runs/"+id+"/reply/retry", strings.NewReader(body))
	r = pathvar.WithVars(r, map[string]string{"run_id": id})
	r.Header.Set("Authorization", "Bearer user-token")
	return r
}

func TestReplyRetryHTTPForwardsOnlyRunAndOriginalToken(t *testing.T) {
	client := replyRetryFunc(func(ctx context.Context, req *pb.GetTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.RunId != 9007199254740997 || len(md) != 1 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" {
			t.Fatalf("retry = %v, %v", req, md)
		}
		result := replyDraftResult()
		result.ReplyStatus = "accepted"
		result.ReplyMsgId = "bot-task:9007199254740997"
		return result, nil
	})
	r := retryReplyRequest("9007199254740997", "")
	r.Header.Set("Idempotency-Key", "caller-key")
	w := httptest.NewRecorder()
	retryTaskReplyHandler(client)(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"task_id":"9007199254740999"`) || !strings.Contains(w.Body.String(), `"reply_status":"accepted"`) {
		t.Fatalf("HTTP = %d %s", w.Code, w.Body.String())
	}
}

func TestReplyRetryHTTPRejectsInvalidInputsBeforeRPC(t *testing.T) {
	for _, tc := range []struct {
		id, body, query string
		anonymous       bool
		want            int
	}{
		{"0", "", "", false, 400}, {"overflow", "", "", false, 400},
		{"9", `{"group_id":"other"}`, "", false, 400}, {"9", `{}`, "", false, 400},
		{"9", "", "group_id=other", false, 400}, {"9", strings.Repeat("x", 1025), "", false, 400},
		{"9", "", "", true, 401},
	} {
		client := replyRetryFunc(func(context.Context, *pb.GetTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
			t.Fatal("invalid retry reached RPC")
			return nil, nil
		})
		r := retryReplyRequest(tc.id, tc.body)
		r.URL.RawQuery = tc.query
		if tc.anonymous {
			r.Header.Del("Authorization")
		}
		w := httptest.NewRecorder()
		retryTaskReplyHandler(client)(w, r)
		if w.Code != tc.want {
			t.Fatalf("%+v = %d %s", tc, w.Code, w.Body.String())
		}
	}
}

func TestReplyRetryHTTPMasksFailuresAndRejectsBadAcceptance(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{
		{codes.Unauthenticated, 401}, {codes.PermissionDenied, 403}, {codes.NotFound, 404},
		{codes.FailedPrecondition, 409}, {codes.AlreadyExists, 409}, {codes.Aborted, 409},
		{codes.Unavailable, 503}, {codes.DeadlineExceeded, 504},
	} {
		client := replyRetryFunc(func(context.Context, *pb.GetTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
			return nil, status.Error(tc.code, "private detail")
		})
		w := httptest.NewRecorder()
		retryTaskReplyHandler(client)(w, retryReplyRequest("9007199254740997", ""))
		if w.Code != tc.want || strings.Contains(w.Body.String(), "private") {
			t.Fatalf("%v: %d %s", tc, w.Code, w.Body.String())
		}
	}
	for _, state := range []string{"", "pending", "unknown", "accepted"} {
		result := replyDraftResult()
		result.ReplyStatus = state
		result.ReplyMsgId = "wrong-message"
		client := replyRetryFunc(func(context.Context, *pb.GetTaskDraftRequest) (*pb.GetTaskDraftResponse, error) { return result, nil })
		w := httptest.NewRecorder()
		retryTaskReplyHandler(client)(w, retryReplyRequest("9007199254740997", ""))
		if w.Code != 502 {
			t.Fatalf("bad acceptance = %d %s", w.Code, w.Body.String())
		}
	}
}
