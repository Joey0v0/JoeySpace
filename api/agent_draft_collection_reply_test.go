package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/agent/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type itemReplyRetryFunc func(context.Context, *pb.GetTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error)

func (f itemReplyRetryFunc) RetryTaskReplyItem(ctx context.Context, r *pb.GetTaskDraftItemRequest, _ ...grpc.CallOption) (*pb.GetTaskDraftItemResponse, error) {
	return f(ctx, r)
}

func itemReplyRetryHTTPRequest(run, index, body string) *http.Request {
	r := collectionEditHTTPRequest(run, index, body)
	r.Method = http.MethodPost
	return r
}

func acceptedItemReplyHTTPResult(index int32) *pb.GetTaskDraftItemResponse {
	r := validHTTPCollection()
	item := r.Items[0]
	item.ItemIndex = &index
	item.Status, item.TaskId, item.ReplyStatus = "succeeded", 9007199254740997, "accepted"
	item.ReplyMsgId = "bot-task:9007199254740999"
	if index > 0 {
		item.ReplyMsgId += ":" + strconv.FormatInt(int64(index), 10)
	}
	item.Draft.DueAtUnixMs = 1791180000456
	item.Draft.Deadline = &pb.TaskDraftDeadline{
		Text: "明天18:00", Source: "message", SourceMessageId: 9007199254740997,
		ReferenceUnixMs: 1791097200123, Timezone: "Asia/Shanghai", Resolution: "parsed",
		ParsedUnixMs: 1791180000456, InstructionReferenceUnixMs: 1791097200789,
	}
	return &pb.GetTaskDraftItemResponse{RunId: r.RunId, TeamId: r.TeamId, GroupId: r.GroupId, ItemCount: 5, Item: item}
}

func TestRetryCollectionItemHTTPForwardsOnlyOriginalIdentityAndPreservesEvidence(t *testing.T) {
	for _, index := range []int32{0, 1, 4} {
		result := acceptedItemReplyHTTPResult(index)
		calls := 0
		client := itemReplyRetryFunc(func(ctx context.Context, req *pb.GetTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
			calls++
			md, _ := metadata.FromOutgoingContext(ctx)
			if req.GetRunId() != 9007199254740999 || req.ItemIndex == nil || req.GetItemIndex() != index ||
				len(md) != 1 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" {
				t.Fatalf("request %v metadata %v", req, md)
			}
			return result, nil
		})
		r := itemReplyRetryHTTPRequest("9007199254740999", strconv.FormatInt(int64(index), 10), " \r\n\t")
		r.Header.Set("Idempotency-Key", "must-not-forward")
		r.Header.Set("X-Team-ID", "1")
		r = r.WithContext(metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer forged", "scope", "other")))
		w := httptest.NewRecorder()
		retryTaskReplyItemHandler(client)(w, r)
		var body agentDraftCollectionResponse
		if w.Code != 200 || calls != 1 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Code != errcode.Success || body.Data == nil ||
			body.Data.RunID != result.RunId || body.Data.TeamID != result.TeamId || body.Data.GroupID != result.GroupId || body.Data.ItemCount != 5 ||
			!reflect.DeepEqual(body.Data.Item, draftCollectionHTTPItem(result.Item)) || body.Data.Items != nil {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		for _, field := range []string{`"run_id":"9007199254740999"`, `"task_id":"9007199254740997"`, `"revision":"9007199254740993"`, `"source_message_id":"9007199254740997"`} {
			if !strings.Contains(w.Body.String(), field) {
				t.Fatalf("lost precision: %s", w.Body.String())
			}
		}
		var raw struct {
			Data struct {
				Item struct {
					Draft struct {
						Deadline map[string]json.RawMessage `json:"deadline"`
					} `json:"draft"`
				} `json:"item"`
			} `json:"data"`
		}
		if json.Unmarshal(w.Body.Bytes(), &raw) != nil || len(raw.Data.Item.Draft.Deadline) != 9 {
			t.Fatalf("deadline evidence missing: %s", w.Body.String())
		}
	}
}

func TestRetryCollectionItemHTTPRejectsCallerContentOrInvalidIdentityBeforeRPC(t *testing.T) {
	client := itemReplyRetryFunc(func(context.Context, *pb.GetTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
		t.Fatal("invalid request reached RPC")
		return nil, nil
	})
	for _, content := range []string{"{}", "null", `{"task_id":"1"}`, `{"content":"new"}`, `{"team_id":"1"}`, `{"expected_revision":"1"}`, strings.Repeat(" ", 1025)} {
		w := httptest.NewRecorder()
		retryTaskReplyItemHandler(client)(w, itemReplyRetryHTTPRequest("1", "0", content))
		if w.Code != 400 {
			t.Fatalf("body %q: %d", content, w.Code)
		}
	}
	for _, run := range []string{"", "0", "-1", "01", "+1", "9223372036854775808"} {
		w := httptest.NewRecorder()
		retryTaskReplyItemHandler(client)(w, itemReplyRetryHTTPRequest(run, "0", ""))
		if w.Code != 400 {
			t.Fatalf("run %q: %d", run, w.Code)
		}
	}
	for _, index := range []string{"", "-1", "5", "00", "+0", "2147483648"} {
		w := httptest.NewRecorder()
		retryTaskReplyItemHandler(client)(w, itemReplyRetryHTTPRequest("1", index, ""))
		if w.Code != 400 {
			t.Fatalf("index %q: %d", index, w.Code)
		}
	}
	for _, modify := range []func(*http.Request){
		func(r *http.Request) { r.Header.Del("Authorization") },
		func(r *http.Request) { r.Header.Set("Authorization", "Basic token") },
		func(r *http.Request) { r.Header.Add("Authorization", "Bearer other") },
	} {
		r := itemReplyRetryHTTPRequest("1", "0", "")
		modify(r)
		w := httptest.NewRecorder()
		retryTaskReplyItemHandler(client)(w, r)
		if w.Code != 401 {
			t.Fatalf("auth: %d %s", w.Code, w.Body.String())
		}
	}
	r := itemReplyRetryHTTPRequest("1", "0", "")
	r.URL.RawQuery = "team_id=1"
	w := httptest.NewRecorder()
	retryTaskReplyItemHandler(client)(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}

func TestRetryCollectionItemHTTPMapsMaskedRPCFailures(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{
		{codes.InvalidArgument, 400}, {codes.Unauthenticated, 401}, {codes.PermissionDenied, 403}, {codes.NotFound, 404},
		{codes.Aborted, 409}, {codes.FailedPrecondition, 409}, {codes.AlreadyExists, 409},
		{codes.Unavailable, 503}, {codes.DeadlineExceeded, 504}, {codes.Internal, 502}, {codes.Unimplemented, 502},
	} {
		client := itemReplyRetryFunc(func(context.Context, *pb.GetTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
			return nil, status.Error(tc.code, "private storage/token details")
		})
		w := httptest.NewRecorder()
		retryTaskReplyItemHandler(client)(w, itemReplyRetryHTTPRequest("1", "0", ""))
		var body agentDraftCollectionResponse
		if w.Code != tc.want || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), `"data"`) || json.Unmarshal(w.Body.Bytes(), &body) != nil ||
			(tc.want == 409 && body.Code != errcode.ErrAgentDraftConflict) {
			t.Fatalf("%s: %d %s", tc.code, w.Code, w.Body.String())
		}
	}
}

func TestRetryCollectionItemHTTPRequiresExactAcceptedSucceededItem(t *testing.T) {
	for name, mutate := range map[string]func(*pb.GetTaskDraftItemResponse){
		"run":           func(r *pb.GetTaskDraftItemResponse) { r.RunId++ },
		"team":          func(r *pb.GetTaskDraftItemResponse) { r.TeamId = 0 },
		"group":         func(r *pb.GetTaskDraftItemResponse) { r.GroupId = 0 },
		"count low":     func(r *pb.GetTaskDraftItemResponse) { r.ItemCount = 1 },
		"count high":    func(r *pb.GetTaskDraftItemResponse) { r.ItemCount = 6 },
		"nil item":      func(r *pb.GetTaskDraftItemResponse) { r.Item = nil },
		"missing index": func(r *pb.GetTaskDraftItemResponse) { r.Item.ItemIndex = nil },
		"wrong index":   func(r *pb.GetTaskDraftItemResponse) { index := int32(0); r.Item.ItemIndex = &index },
		"task zero":     func(r *pb.GetTaskDraftItemResponse) { r.Item.TaskId = 0 },
		"task negative": func(r *pb.GetTaskDraftItemResponse) { r.Item.TaskId = -1 },
		"waiting":       func(r *pb.GetTaskDraftItemResponse) { r.Item.Status, r.Item.TaskId = "waiting_confirmation", 0 },
		"creating":      func(r *pb.GetTaskDraftItemResponse) { r.Item.Status, r.Item.TaskId = "creating", 0 },
		"skipped": func(r *pb.GetTaskDraftItemResponse) {
			r.Item.Status, r.Item.TaskId, r.Item.ReplyStatus, r.Item.ReplyMsgId = "skipped", 0, "disabled", ""
		},
		"pending":            func(r *pb.GetTaskDraftItemResponse) { r.Item.ReplyStatus = "pending" },
		"unknown":            func(r *pb.GetTaskDraftItemResponse) { r.Item.ReplyStatus, r.Item.ReplyMsgId = "unknown", "" },
		"disabled":           func(r *pb.GetTaskDraftItemResponse) { r.Item.ReplyStatus, r.Item.ReplyMsgId = "disabled", "" },
		"not started":        func(r *pb.GetTaskDraftItemResponse) { r.Item.ReplyStatus, r.Item.ReplyMsgId = "not_started", "" },
		"invalid reply":      func(r *pb.GetTaskDraftItemResponse) { r.Item.ReplyStatus = "delivered" },
		"empty reply":        func(r *pb.GetTaskDraftItemResponse) { r.Item.ReplyStatus = "" },
		"wrong message run":  func(r *pb.GetTaskDraftItemResponse) { r.Item.ReplyMsgId = "bot-task:9007199254740998:1" },
		"wrong message item": func(r *pb.GetTaskDraftItemResponse) { r.Item.ReplyMsgId = "bot-task:9007199254740999" },
		"missing message":    func(r *pb.GetTaskDraftItemResponse) { r.Item.ReplyMsgId = "" },
		"nil draft":          func(r *pb.GetTaskDraftItemResponse) { r.Item.Draft = nil },
		"revision":           func(r *pb.GetTaskDraftItemResponse) { r.Item.Draft.Revision = 0 },
		"deadline missing":   func(r *pb.GetTaskDraftItemResponse) { r.Item.Draft.Deadline = nil },
		"deadline corrupt":   func(r *pb.GetTaskDraftItemResponse) { r.Item.Draft.Deadline.ParsedUnixMs++ },
		"unreviewed assignee": func(r *pb.GetTaskDraftItemResponse) {
			r.Item.Draft.AssigneeId, r.Item.Draft.AssigneeResolution = 0, "ambiguous"
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := acceptedItemReplyHTTPResult(1)
			mutate(result)
			client := itemReplyRetryFunc(func(context.Context, *pb.GetTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
				return result, nil
			})
			w := httptest.NewRecorder()
			retryTaskReplyItemHandler(client)(w, itemReplyRetryHTTPRequest("9007199254740999", "1", ""))
			if w.Code != 502 || strings.Contains(w.Body.String(), `"data"`) {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
	w := httptest.NewRecorder()
	retryTaskReplyItemHandler(itemReplyRetryFunc(func(context.Context, *pb.GetTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
		return nil, nil
	}))(w, itemReplyRetryHTTPRequest("1", "0", ""))
	if w.Code != 502 {
		t.Fatal(w.Code)
	}
}

func TestConfirmCollectionItemHTTPPreservesUncertainReplyWithoutUndoingTask(t *testing.T) {
	for _, reply := range []string{"pending", "accepted", "unknown"} {
		result := collectionConfirmResponse()
		result.Item.ReplyStatus = reply
		if reply != "unknown" {
			result.Item.ReplyMsgId = "bot-task:9007199254740999"
		}
		client := collectionConfirmerFunc(func(context.Context, *pb.ConfirmTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
			return result, nil
		})
		w := httptest.NewRecorder()
		confirmTaskDraftItemHandler(client)(w, collectionConfirmHTTPRequest("9007199254740999", "0", validCollectionConfirmBody))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"task_id":"9007199254740997"`) || !strings.Contains(w.Body.String(), `"reply_status":"`+reply+`"`) {
			t.Fatalf("%s: %d %s", reply, w.Code, w.Body.String())
		}
	}
}

func TestCollectionEditAndSkipHTTPStillRejectSucceededItemsWithValidReplies(t *testing.T) {
	for _, reply := range []string{"pending", "accepted", "unknown"} {
		for _, kind := range []string{"text", "assignee", "deadline", "skip"} {
			result := collectionEditResponse(0, 1)
			result.Item.Status, result.Item.TaskId, result.Item.ReplyStatus = "succeeded", 9007199254740997, reply
			if reply != "unknown" {
				result.Item.ReplyMsgId = "bot-task:9007199254740999"
			}
			// Each response satisfies the edit-specific content checks; only its frozen state prohibits the edit.
			if kind == "assignee" {
				result.Item.Draft.AssigneeId, result.Item.Draft.AssigneeResolution = 0, "unassigned"
			}
			if kind == "deadline" {
				result.Item.Draft.Deadline.Resolution = "unset"
			}
			w := httptest.NewRecorder()
			if kind == "skip" {
				client := collectionSkipperFunc(func(context.Context, *pb.SkipTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
					return result, nil
				})
				skipTaskDraftItemHandler(client)(w, collectionSkipHTTPRequest("9007199254740999", "0", `{"expected_revision":"1"}`))
			} else {
				collectionEditHandler(kind, collectionResultClient(result, nil))(w, collectionEditHTTPRequest("9007199254740999", "0", validCollectionEditBody(kind)))
			}
			if w.Code != 502 || strings.Contains(w.Body.String(), `"data"`) {
				t.Fatalf("%s %s: %d %s", kind, reply, w.Code, w.Body.String())
			}
		}
	}
}
