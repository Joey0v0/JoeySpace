package main

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/agent/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type collectionSkipperFunc func(context.Context, *pb.SkipTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error)

func (f collectionSkipperFunc) SkipTaskDraftItem(ctx context.Context, r *pb.SkipTaskDraftItemRequest, _ ...grpc.CallOption) (*pb.GetTaskDraftItemResponse, error) {
	return f(ctx, r)
}

func collectionSkipHTTPRequest(run, index, body string) *http.Request {
	r := collectionEditHTTPRequest(run, index, body)
	r.Method = http.MethodPost
	return r
}
func collectionSkipResponse(index int32, revision int64) *pb.GetTaskDraftItemResponse {
	r := collectionEditResponse(index, revision)
	r.Item.Status = "skipped"
	return r
}

func TestSkipCollectionItemHTTPForwardsIdentityAndKeepsMaxVersionOnReplay(t *testing.T) {
	for _, index := range []int32{0, 1} {
		calls := 0
		client := collectionSkipperFunc(func(ctx context.Context, req *pb.SkipTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
			calls++
			md, _ := metadata.FromOutgoingContext(ctx)
			if req.GetRunId() != 9007199254740999 || req.ItemIndex == nil || req.GetItemIndex() != index || req.GetExpectedRevision() != math.MaxInt64 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" || len(md.Get("idempotency-key")) != 0 {
				t.Fatalf("request %v md %v", req, md)
			}
			return collectionSkipResponse(index, math.MaxInt64), nil
		})
		for i := 0; i < 2; i++ {
			r := collectionSkipHTTPRequest("9007199254740999", strconv.Itoa(int(index)), `{"expected_revision":"9223372036854775807"}`)
			r.Header.Set("Idempotency-Key", "ignored-key")
			w := httptest.NewRecorder()
			skipTaskDraftItemHandler(client)(w, r)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"revision":"9223372036854775807"`) || !strings.Contains(w.Body.String(), `"task_id":"0"`) || !strings.Contains(w.Body.String(), `"reply_status":"disabled"`) {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		}
		if calls != 2 {
			t.Fatal(calls)
		}
	}
}

func TestSkipCollectionItemHTTPPreservesUnresolvedEvidence(t *testing.T) {
	result := collectionSkipResponse(1, 9007199254740993)
	result.Item.Draft.AssigneeId = 0
	result.Item.Draft.AssigneeResolution = "ambiguous"
	result.Item.Draft.Deadline = &pb.TaskDraftDeadline{Text: "明天下午", Source: "message", SourceMessageId: 9007199254740995, ReferenceUnixMs: 1791097200123, InstructionReferenceUnixMs: 1791097200999, Timezone: "Asia/Shanghai", Resolution: "needs_input", Reason: "unsupported_expression"}
	client := collectionSkipperFunc(func(context.Context, *pb.SkipTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
		return result, nil
	})
	w := httptest.NewRecorder()
	skipTaskDraftItemHandler(client)(w, collectionSkipHTTPRequest("9007199254740999", "1", `{"expected_revision":"9007199254740993"}`))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"assignee_resolution":"ambiguous"`) || !strings.Contains(w.Body.String(), `"resolution":"needs_input"`) || !strings.Contains(w.Body.String(), `"source_message_id":"9007199254740995"`) || !strings.Contains(w.Body.String(), `"reference_unix_ms":1791097200123`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestSkipCollectionItemHTTPRejectsInvalidBodyBeforeRPC(t *testing.T) {
	client := collectionSkipperFunc(func(context.Context, *pb.SkipTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
		t.Fatal("invalid body reached RPC")
		return nil, nil
	})
	for _, body := range []string{`{}`, `null`, `{"expected_revision":null}`, `{"expected_revision":1}`, `{"expected_revision":""}`, `{"expected_revision":"0"}`, `{"expected_revision":"01"}`, `{"expected_revision":"+1"}`, `{"expected_revision":"-1"}`, `{"expected_revision":"9223372036854775808"}`, `{"expected_revision":"1","user_id":"1"}`, `{"expected_revision":"1","item_index":0}`, `{"expected_revision":"1"}{}`, `{"expected_revision":"1"}` + strings.Repeat(" ", 1024)} {
		w := httptest.NewRecorder()
		skipTaskDraftItemHandler(client)(w, collectionSkipHTTPRequest("1", "0", body))
		if w.Code != 400 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
}

func TestSkipCollectionItemHTTPRejectsInvalidIdentityOrTokenBeforeRPC(t *testing.T) {
	client := collectionSkipperFunc(func(context.Context, *pb.SkipTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
		t.Fatal("invalid identity reached RPC")
		return nil, nil
	})
	for _, tc := range []struct{ run, index string }{{"0", "0"}, {"01", "0"}, {"+1", "0"}, {"9223372036854775808", "0"}, {"1", ""}, {"1", "-1"}, {"1", "5"}, {"1", "00"}, {"1", "+0"}, {"1", "2147483648"}} {
		w := httptest.NewRecorder()
		skipTaskDraftItemHandler(client)(w, collectionSkipHTTPRequest(tc.run, tc.index, `{"expected_revision":"1"}`))
		if w.Code != 400 {
			t.Fatalf("%+v: %d", tc, w.Code)
		}
	}
	for _, auth := range []string{"", "Basic token", "Bearer token other"} {
		r := collectionSkipHTTPRequest("1", "0", `{"expected_revision":"1"}`)
		r.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		skipTaskDraftItemHandler(client)(w, r)
		if w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	r := collectionSkipHTTPRequest("1", "0", `{"expected_revision":"1"}`)
	r.Header.Add("Authorization", "Bearer other")
	w := httptest.NewRecorder()
	skipTaskDraftItemHandler(client)(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}

func TestSkipCollectionItemHTTPRejectsChangedVersionOrFalseSuccess(t *testing.T) {
	for _, mutate := range []func(*pb.GetTaskDraftItemResponse){
		func(r *pb.GetTaskDraftItemResponse) { r.RunId = 1 }, func(r *pb.GetTaskDraftItemResponse) { r.TeamId = 0 }, func(r *pb.GetTaskDraftItemResponse) { r.GroupId = 0 }, func(r *pb.GetTaskDraftItemResponse) { r.ItemCount = 0 }, func(r *pb.GetTaskDraftItemResponse) { r.ItemCount = 1 }, func(r *pb.GetTaskDraftItemResponse) { r.ItemCount = 6 }, func(r *pb.GetTaskDraftItemResponse) { r.Item = nil }, func(r *pb.GetTaskDraftItemResponse) { r.Item.ItemIndex = nil }, func(r *pb.GetTaskDraftItemResponse) { v := int32(0); r.Item.ItemIndex = &v },
		func(r *pb.GetTaskDraftItemResponse) { r.Item.Status = "waiting_confirmation" }, func(r *pb.GetTaskDraftItemResponse) { r.Item.Status = "creating" }, func(r *pb.GetTaskDraftItemResponse) { r.Item.Status = "succeeded"; r.Item.TaskId = 99 }, func(r *pb.GetTaskDraftItemResponse) { r.Item.TaskId = 9 }, func(r *pb.GetTaskDraftItemResponse) { r.Item.ReplyStatus = "not_started" }, func(r *pb.GetTaskDraftItemResponse) { r.Item.ReplyStatus = "accepted" }, func(r *pb.GetTaskDraftItemResponse) { r.Item.ReplyMsgId = "fake" },
		func(r *pb.GetTaskDraftItemResponse) { r.Item.Draft = nil }, func(r *pb.GetTaskDraftItemResponse) { r.Item.Draft.Revision = 0 }, func(r *pb.GetTaskDraftItemResponse) { r.Item.Draft.Revision = 2 }, func(r *pb.GetTaskDraftItemResponse) { r.Item.Draft.Revision = 3 }, func(r *pb.GetTaskDraftItemResponse) { r.Item.Draft.Deadline = nil }, func(r *pb.GetTaskDraftItemResponse) { r.Item.Draft.AssigneeResolution = "" },
	} {
		result := collectionSkipResponse(1, 1)
		mutate(result)
		client := collectionSkipperFunc(func(context.Context, *pb.SkipTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
			return result, nil
		})
		w := httptest.NewRecorder()
		skipTaskDraftItemHandler(client)(w, collectionSkipHTTPRequest("9007199254740999", "1", `{"expected_revision":"1"}`))
		if w.Code != 502 || strings.Contains(w.Body.String(), `"data"`) {
			t.Fatalf("bad result %v: %d %s", result, w.Code, w.Body.String())
		}
	}
	client := collectionSkipperFunc(func(context.Context, *pb.SkipTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
		return nil, nil
	})
	w := httptest.NewRecorder()
	skipTaskDraftItemHandler(client)(w, collectionSkipHTTPRequest("9007199254740999", "0", `{"expected_revision":"1"}`))
	if w.Code != 502 {
		t.Fatal(w.Code)
	}
}

func TestSkipCollectionItemHTTPMapsRPCFailuresWithoutDetails(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{{codes.Aborted, 409}, {codes.FailedPrecondition, 409}, {codes.PermissionDenied, 403}, {codes.NotFound, 404}, {codes.Unauthenticated, 401}, {codes.InvalidArgument, 400}, {codes.Unavailable, 503}, {codes.DeadlineExceeded, 504}, {codes.Internal, 502}} {
		client := collectionSkipperFunc(func(context.Context, *pb.SkipTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
			return nil, status.Error(tc.code, "private skip detail")
		})
		w := httptest.NewRecorder()
		skipTaskDraftItemHandler(client)(w, collectionSkipHTTPRequest("1", "0", `{"expected_revision":"1"}`))
		if w.Code != tc.want || strings.Contains(w.Body.String(), "private") {
			t.Fatalf("%s: %d %s", tc.code, w.Code, w.Body.String())
		}
	}
}
