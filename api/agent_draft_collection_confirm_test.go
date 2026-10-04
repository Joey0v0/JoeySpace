package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

type collectionConfirmerFunc func(context.Context, *pb.ConfirmTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error)

func (f collectionConfirmerFunc) ConfirmTaskDraftItem(ctx context.Context, r *pb.ConfirmTaskDraftItemRequest, _ ...grpc.CallOption) (*pb.GetTaskDraftItemResponse, error) {
	return f(ctx, r)
}

const validCollectionConfirmBody = `{"expected_title":"Task","expected_description":"Notes","expected_revision":"1","expected_assignee_id":"9007199254740995","expected_due_at_unix_ms":0,"expected_deadline_resolution":"none"}`

func collectionConfirmHTTPRequest(run, index, body string) *http.Request {
	r := collectionEditHTTPRequest(run, index, body)
	r.Method = http.MethodPost
	return r
}
func collectionConfirmResponse() *pb.GetTaskDraftItemResponse {
	r := collectionEditResponse(0, 1)
	r.Item.Status = "succeeded"
	r.Item.TaskId = 9007199254740997
	return r
}
func collectionConfirmBodyField(t *testing.T, field, value string) string {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(validCollectionConfirmBody), &fields); err != nil {
		t.Fatal(err)
	}
	if value == "" {
		delete(fields, field)
	} else {
		fields[field] = json.RawMessage(value)
	}
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestConfirmCollectionItemHTTPForwardsCompleteReviewAndExactResult(t *testing.T) {
	for _, tc := range []struct {
		index         int32
		assignee, due int64
		resolution    string
		revision      int64
	}{{0, 0, 0, "none", 1}, {1, 9007199254740995, 1791097200123, "selected", 9007199254740993}} {
		client := collectionConfirmerFunc(func(ctx context.Context, r *pb.ConfirmTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
			md, _ := metadata.FromOutgoingContext(ctx)
			if r.GetRunId() != 9007199254740999 || r.ItemIndex == nil || r.GetItemIndex() != tc.index || r.GetExpectedTitle() != "Task" || r.GetExpectedDescription() != "Notes" || r.GetExpectedRevision() != tc.revision ||
				r.ExpectedAssigneeId == nil || r.GetExpectedAssigneeId() != tc.assignee || r.ExpectedDueAtUnixMs == nil || r.GetExpectedDueAtUnixMs() != tc.due || r.GetExpectedDeadlineResolution() != tc.resolution ||
				len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" || len(md.Get("idempotency-key")) != 0 {
				t.Fatalf("review %v md %v", r, md)
			}
			result := collectionEditResponse(tc.index, tc.revision)
			result.Item.Status = "succeeded"
			result.Item.TaskId = 9223372036854775806
			result.Item.Draft.AssigneeId = tc.assignee
			if tc.assignee == 0 {
				result.Item.Draft.AssigneeName = ""
				result.Item.Draft.AssigneeResolution = "none"
			}
			result.Item.Draft.DueAtUnixMs = tc.due
			result.Item.Draft.Deadline.Resolution = tc.resolution
			return result, nil
		})
		body := `{"expected_title":"Task","expected_description":"Notes","expected_revision":"` + strconv.FormatInt(tc.revision, 10) + `","expected_assignee_id":"` + strconv.FormatInt(tc.assignee, 10) + `","expected_due_at_unix_ms":` + strconv.FormatInt(tc.due, 10) + `,"expected_deadline_resolution":"` + tc.resolution + `"}`
		r := collectionConfirmHTTPRequest("9007199254740999", strconv.Itoa(int(tc.index)), body)
		r.Header.Set("Idempotency-Key", "ignored-browser-key")
		w := httptest.NewRecorder()
		confirmTaskDraftItemHandler(client)(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"task_id":"9223372036854775806"`) || !strings.Contains(w.Body.String(), `"revision":"`+strconv.FormatInt(tc.revision, 10)+`"`) || strings.Contains(w.Body.String(), "accepted") {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
}

func TestConfirmCollectionItemHTTPRequiresEveryReviewField(t *testing.T) {
	client := collectionConfirmerFunc(func(context.Context, *pb.ConfirmTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
		t.Fatal("incomplete review reached RPC")
		return nil, nil
	})
	for _, field := range []string{"expected_title", "expected_description", "expected_revision", "expected_assignee_id", "expected_due_at_unix_ms", "expected_deadline_resolution"} {
		for _, value := range []string{"", "null"} {
			body := collectionConfirmBodyField(t, field, value)
			w := httptest.NewRecorder()
			confirmTaskDraftItemHandler(client)(w, collectionConfirmHTTPRequest("1", "0", body))
			if w.Code != 400 {
				t.Fatalf("%s=%q: %d %s", field, value, w.Code, w.Body.String())
			}
		}
	}
	for _, tc := range []struct{ field, value string }{
		{"expected_title", `1`}, {"expected_title", `""`}, {"expected_title", `" Task "`}, {"expected_description", `0`}, {"expected_description", `" Notes "`},
		{"expected_revision", `1`}, {"expected_revision", `"0"`}, {"expected_revision", `"01"`}, {"expected_revision", `"+1"`}, {"expected_revision", `"9223372036854775808"`},
		{"expected_assignee_id", `0`}, {"expected_assignee_id", `"-1"`}, {"expected_assignee_id", `"00"`}, {"expected_assignee_id", `"9223372036854775808"`},
		{"expected_due_at_unix_ms", `"0"`}, {"expected_due_at_unix_ms", `-1`}, {"expected_due_at_unix_ms", `1.5`}, {"expected_due_at_unix_ms", `1e3`}, {"expected_due_at_unix_ms", `253402300800000`},
		{"expected_deadline_resolution", `1`}, {"expected_deadline_resolution", `""`}, {"expected_deadline_resolution", `"unknown"`}, {"expected_deadline_resolution", `" parsed"`},
		{"user_id", `"1"`}, {"request_key", `"fake"`},
	} {
		body := collectionConfirmBodyField(t, tc.field, tc.value)
		w := httptest.NewRecorder()
		confirmTaskDraftItemHandler(client)(w, collectionConfirmHTTPRequest("1", "0", body))
		if w.Code != 400 {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
	for _, body := range []string{validCollectionConfirmBody + `{}`, validCollectionConfirmBody + strings.Repeat(" ", 32768)} {
		w := httptest.NewRecorder()
		confirmTaskDraftItemHandler(client)(w, collectionConfirmHTTPRequest("1", "0", body))
		if w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
}

func TestConfirmCollectionItemHTTPRejectsIdentityAndAuthorization(t *testing.T) {
	client := collectionConfirmerFunc(func(context.Context, *pb.ConfirmTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
		t.Fatal("invalid identity reached RPC")
		return nil, nil
	})
	for _, tc := range []struct{ run, index string }{{"0", "0"}, {"01", "0"}, {"+1", "0"}, {"9223372036854775808", "0"}, {"1", ""}, {"1", "5"}, {"1", "00"}, {"1", "-1"}} {
		w := httptest.NewRecorder()
		confirmTaskDraftItemHandler(client)(w, collectionConfirmHTTPRequest(tc.run, tc.index, validCollectionConfirmBody))
		if w.Code != 400 {
			t.Fatalf("%+v: %d", tc, w.Code)
		}
	}
	r := collectionConfirmHTTPRequest("1", "0", validCollectionConfirmBody)
	r.Header.Del("Authorization")
	w := httptest.NewRecorder()
	confirmTaskDraftItemHandler(client)(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	r = collectionConfirmHTTPRequest("1", "0", validCollectionConfirmBody)
	r.Header.Add("Authorization", "Bearer other")
	w = httptest.NewRecorder()
	confirmTaskDraftItemHandler(client)(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}

func TestConfirmCollectionItemHTTPRejectsInconsistentSavedResults(t *testing.T) {
	for _, mutate := range []func(*pb.GetTaskDraftItemResponse){
		func(r *pb.GetTaskDraftItemResponse) { r.RunId = 1 }, func(r *pb.GetTaskDraftItemResponse) { r.TeamId = 0 }, func(r *pb.GetTaskDraftItemResponse) { r.GroupId = 0 }, func(r *pb.GetTaskDraftItemResponse) { r.ItemCount = 0 }, func(r *pb.GetTaskDraftItemResponse) { r.ItemCount = 6 }, func(r *pb.GetTaskDraftItemResponse) { r.Item = nil }, func(r *pb.GetTaskDraftItemResponse) { r.Item.ItemIndex = nil }, func(r *pb.GetTaskDraftItemResponse) { v := int32(1); r.Item.ItemIndex = &v },
		func(r *pb.GetTaskDraftItemResponse) { r.Item.Status = "waiting_confirmation"; r.Item.TaskId = 0 }, func(r *pb.GetTaskDraftItemResponse) { r.Item.Status = "creating"; r.Item.TaskId = 0 }, func(r *pb.GetTaskDraftItemResponse) { r.Item.TaskId = 0 }, func(r *pb.GetTaskDraftItemResponse) { r.Item.TaskId = -1 }, func(r *pb.GetTaskDraftItemResponse) { r.Item.ReplyStatus = "accepted" }, func(r *pb.GetTaskDraftItemResponse) { r.Item.ReplyMsgId = "fake" },
		func(r *pb.GetTaskDraftItemResponse) { r.Item.Status = "skipped"; r.Item.TaskId = 0 },
		func(r *pb.GetTaskDraftItemResponse) { r.Item.Draft = nil }, func(r *pb.GetTaskDraftItemResponse) { r.Item.Draft.Revision = 2 }, func(r *pb.GetTaskDraftItemResponse) { r.Item.Draft.Title = "Other" }, func(r *pb.GetTaskDraftItemResponse) { r.Item.Draft.Description = "Other" }, func(r *pb.GetTaskDraftItemResponse) { r.Item.Draft.AssigneeId++ }, func(r *pb.GetTaskDraftItemResponse) { r.Item.Draft.Deadline = nil }, func(r *pb.GetTaskDraftItemResponse) {
			r.Item.Draft.DueAtUnixMs = 1791097200123
			r.Item.Draft.Deadline.Resolution = "selected"
		}, func(r *pb.GetTaskDraftItemResponse) { r.Item.Draft.Deadline.Resolution = "unset" },
	} {
		result := collectionConfirmResponse()
		mutate(result)
		client := collectionConfirmerFunc(func(context.Context, *pb.ConfirmTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
			return result, nil
		})
		w := httptest.NewRecorder()
		confirmTaskDraftItemHandler(client)(w, collectionConfirmHTTPRequest("9007199254740999", "0", validCollectionConfirmBody))
		if w.Code != 502 || strings.Contains(w.Body.String(), `"data"`) {
			t.Fatalf("bad result %v: %d %s", result, w.Code, w.Body.String())
		}
	}
	client := collectionConfirmerFunc(func(context.Context, *pb.ConfirmTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
		return nil, nil
	})
	w := httptest.NewRecorder()
	confirmTaskDraftItemHandler(client)(w, collectionConfirmHTTPRequest("9007199254740999", "0", validCollectionConfirmBody))
	if w.Code != 502 {
		t.Fatal(w.Code)
	}
}

func TestConfirmCollectionItemHTTPMapsFailuresAndDelegatesNeedsInput(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{{codes.Aborted, 409}, {codes.FailedPrecondition, 409}, {codes.AlreadyExists, 409}, {codes.PermissionDenied, 403}, {codes.NotFound, 404}, {codes.Unauthenticated, 401}, {codes.InvalidArgument, 400}, {codes.Unavailable, 503}, {codes.DeadlineExceeded, 504}, {codes.Internal, 502}} {
		client := collectionConfirmerFunc(func(context.Context, *pb.ConfirmTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
			return nil, status.Error(tc.code, "private Task result")
		})
		w := httptest.NewRecorder()
		confirmTaskDraftItemHandler(client)(w, collectionConfirmHTTPRequest("9007199254740999", "0", validCollectionConfirmBody))
		if w.Code != tc.want || strings.Contains(w.Body.String(), "private") {
			t.Fatalf("%s: %d %s", tc.code, w.Code, w.Body.String())
		}
		if tc.code == codes.AlreadyExists {
			var response agentDraftCollectionResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Code != errcode.ErrTaskRequestConflict || strings.Contains(response.Msg, "Idempotency-Key") {
				t.Fatalf("wrong Task conflict: %s", w.Body.String())
			}
		}
	}
	client := collectionConfirmerFunc(func(_ context.Context, req *pb.ConfirmTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
		if req.GetExpectedDeadlineResolution() != "needs_input" {
			t.Fatal(req)
		}
		return nil, status.Error(codes.FailedPrecondition, "clarification required")
	})
	body := collectionConfirmBodyField(t, "expected_deadline_resolution", `"needs_input"`)
	w := httptest.NewRecorder()
	confirmTaskDraftItemHandler(client)(w, collectionConfirmHTTPRequest("9007199254740999", "0", body))
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
}

func TestConfirmCollectionItemHTTPKeepsUnicodeSnapshotAtLimits(t *testing.T) {
	client := collectionConfirmerFunc(func(_ context.Context, req *pb.ConfirmTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
		if len([]rune(req.GetExpectedTitle())) != 200 || len([]rune(req.GetExpectedDescription())) != 2000 {
			t.Fatal(req)
		}
		result := collectionConfirmResponse()
		result.Item.Draft.Title = req.GetExpectedTitle()
		result.Item.Draft.Description = req.GetExpectedDescription()
		return result, nil
	})
	body := `{"expected_title":"` + strings.Repeat(`\ud83d\ude00`, 200) + `","expected_description":"` + strings.Repeat(`\ud83d\ude00`, 2000) + `","expected_revision":"1","expected_assignee_id":"9007199254740995","expected_due_at_unix_ms":0,"expected_deadline_resolution":"none"}`
	w := httptest.NewRecorder()
	confirmTaskDraftItemHandler(client)(w, collectionConfirmHTTPRequest("9007199254740999", "0", body))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
