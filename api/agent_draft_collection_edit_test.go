package main

import (
	"context"
	"io"
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

type collectionEditClient struct {
	text     func(context.Context, *pb.EditTaskDraftItemTextRequest) (*pb.GetTaskDraftItemResponse, error)
	assignee func(context.Context, *pb.SelectTaskDraftItemAssigneeRequest) (*pb.GetTaskDraftItemResponse, error)
	deadline func(context.Context, *pb.EditTaskDraftItemDeadlineRequest) (*pb.GetTaskDraftItemResponse, error)
}

func (c collectionEditClient) EditTaskDraftItemText(ctx context.Context, r *pb.EditTaskDraftItemTextRequest, _ ...grpc.CallOption) (*pb.GetTaskDraftItemResponse, error) {
	return c.text(ctx, r)
}
func (c collectionEditClient) SelectTaskDraftItemAssignee(ctx context.Context, r *pb.SelectTaskDraftItemAssigneeRequest, _ ...grpc.CallOption) (*pb.GetTaskDraftItemResponse, error) {
	return c.assignee(ctx, r)
}
func (c collectionEditClient) EditTaskDraftItemDeadline(ctx context.Context, r *pb.EditTaskDraftItemDeadlineRequest, _ ...grpc.CallOption) (*pb.GetTaskDraftItemResponse, error) {
	return c.deadline(ctx, r)
}

func collectionEditHTTPRequest(run, index, body string) *http.Request {
	r := collectionHTTPRequest(run, index, true)
	r.Method = http.MethodPut
	r.Body = io.NopCloser(strings.NewReader(body))
	return r
}
func collectionEditResponse(index int32, revision int64) *pb.GetTaskDraftItemResponse {
	r := validHTTPCollection()
	item := r.Items[index]
	item.Draft.Revision = revision
	return &pb.GetTaskDraftItemResponse{RunId: r.RunId, TeamId: r.TeamId, GroupId: r.GroupId, ItemCount: r.ItemCount, Item: item}
}
func collectionEditHandler(kind string, client collectionEditClient) http.HandlerFunc {
	switch kind {
	case "text":
		return editTaskDraftItemTextHandler(client)
	case "assignee":
		return selectTaskDraftItemAssigneeHandler(client)
	default:
		return editTaskDraftItemDeadlineHandler(client)
	}
}
func validCollectionEditBody(kind string) string {
	switch kind {
	case "text":
		return `{"title":"Task","description":"Notes","expected_revision":"1"}`
	case "assignee":
		return `{"assignee_id":"0","expected_revision":"1"}`
	default:
		return `{"due_at_unix_ms":0,"expected_revision":"1"}`
	}
}
func collectionResultClient(result *pb.GetTaskDraftItemResponse, err error) collectionEditClient {
	return collectionEditClient{
		text: func(context.Context, *pb.EditTaskDraftItemTextRequest) (*pb.GetTaskDraftItemResponse, error) {
			return result, err
		},
		assignee: func(context.Context, *pb.SelectTaskDraftItemAssigneeRequest) (*pb.GetTaskDraftItemResponse, error) {
			return result, err
		},
		deadline: func(context.Context, *pb.EditTaskDraftItemDeadlineRequest) (*pb.GetTaskDraftItemResponse, error) {
			return result, err
		},
	}
}

func TestCollectionEditsHTTPForwardExactIdentityTokenAndExplicitValues(t *testing.T) {
	const runID int64 = 9007199254740999
	const revision int64 = 9007199254740993
	check := func(ctx context.Context, run int64, index *int32, rev int64) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if run != runID || index == nil || *index != 0 || rev != revision || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" || len(md.Get("idempotency-key")) != 0 {
			t.Fatalf("identity run=%d index=%v rev=%d md=%v", run, index, rev, md)
		}
	}
	client := collectionEditClient{
		text: func(ctx context.Context, req *pb.EditTaskDraftItemTextRequest) (*pb.GetTaskDraftItemResponse, error) {
			check(ctx, req.RunId, req.ItemIndex, req.ExpectedRevision)
			if req.Title != "Task" || req.Description != "Notes" {
				t.Fatal(req)
			}
			return collectionEditResponse(0, revision+1), nil
		},
		assignee: func(ctx context.Context, req *pb.SelectTaskDraftItemAssigneeRequest) (*pb.GetTaskDraftItemResponse, error) {
			check(ctx, req.RunId, req.ItemIndex, req.ExpectedRevision)
			if req.AssigneeId == nil {
				t.Fatal("explicit ID lost")
			}
			r := collectionEditResponse(0, revision+1)
			r.Item.Draft.AssigneeId = req.GetAssigneeId()
			r.Item.Draft.AssigneeResolution = "selected"
			if req.GetAssigneeId() == 0 {
				r.Item.Draft.AssigneeResolution = "unassigned"
			}
			return r, nil
		},
		deadline: func(ctx context.Context, req *pb.EditTaskDraftItemDeadlineRequest) (*pb.GetTaskDraftItemResponse, error) {
			check(ctx, req.RunId, req.ItemIndex, req.ExpectedRevision)
			if req.DueAtUnixMs == nil {
				t.Fatal("explicit deadline lost")
			}
			r := collectionEditResponse(0, revision+1)
			r.Item.Draft.DueAtUnixMs = req.GetDueAtUnixMs()
			r.Item.Draft.Deadline.Resolution = "selected"
			if req.GetDueAtUnixMs() == 0 {
				r.Item.Draft.Deadline.Resolution = "unset"
			}
			return r, nil
		},
	}
	for _, tc := range []struct{ kind, body string }{
		{"text", `{"title":" Task ","description":" Notes ","expected_revision":"9007199254740993"}`},
		{"assignee", `{"assignee_id":"0","expected_revision":"9007199254740993"}`},
		{"assignee", `{"assignee_id":"9007199254740995","expected_revision":"9007199254740993"}`},
		{"deadline", `{"due_at_unix_ms":0,"expected_revision":"9007199254740993"}`},
		{"deadline", `{"due_at_unix_ms":1791097200123,"expected_revision":"9007199254740993"}`},
		{"deadline", `{"due_at_unix_ms":253402300799999,"expected_revision":"9007199254740993"}`},
	} {
		w := httptest.NewRecorder()
		r := collectionEditHTTPRequest("9007199254740999", "0", tc.body)
		r.Header.Set("Idempotency-Key", "ignored-browser-key")
		collectionEditHandler(tc.kind, client)(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"item_index":0`) || !strings.Contains(w.Body.String(), `"revision":"9007199254740994"`) {
			t.Fatalf("%s: %d %s", tc.kind, w.Code, w.Body.String())
		}
	}
}

func TestCollectionEditsHTTPRejectBadBodiesBeforeRPC(t *testing.T) {
	client := collectionEditClient{
		text: func(context.Context, *pb.EditTaskDraftItemTextRequest) (*pb.GetTaskDraftItemResponse, error) {
			t.Fatal("bad text reached RPC")
			return nil, nil
		},
		assignee: func(context.Context, *pb.SelectTaskDraftItemAssigneeRequest) (*pb.GetTaskDraftItemResponse, error) {
			t.Fatal("bad assignee reached RPC")
			return nil, nil
		},
		deadline: func(context.Context, *pb.EditTaskDraftItemDeadlineRequest) (*pb.GetTaskDraftItemResponse, error) {
			t.Fatal("bad deadline reached RPC")
			return nil, nil
		},
	}
	for _, kind := range []string{"text", "assignee", "deadline"} {
		valid := validCollectionEditBody(kind)
		bodies := []string{`null`, `{}`, valid + `{}`, strings.TrimSuffix(valid, "}") + `,"team_id":"2"}`, strings.TrimSuffix(valid, "}") + `,"expected_title":"old"}`, valid + strings.Repeat(" ", 32768)}
		for _, value := range []string{`null`, `1`, `"0"`, `"01"`, `"+1"`, `"9223372036854775808"`} {
			bodies = append(bodies, strings.Replace(valid, `"expected_revision":"1"`, `"expected_revision":`+value, 1))
		}
		bodies = append(bodies, strings.Replace(valid, `,"expected_revision":"1"`, "", 1))
		switch kind {
		case "text":
			bodies = append(bodies, `{"title":"Task","expected_revision":"1"}`, `{"description":"Notes","expected_revision":"1"}`, `{"title":null,"description":"Notes","expected_revision":"1"}`, `{"title":"Task","description":null,"expected_revision":"1"}`, `{"title":" ","description":"","expected_revision":"1"}`, `{"title":"`+strings.Repeat("字", 201)+`","description":"","expected_revision":"1"}`, `{"title":"Task","description":"`+strings.Repeat("字", 2001)+`","expected_revision":"1"}`)
		case "assignee":
			for _, value := range []string{`null`, `0`, `"-1"`, `"00"`, `"+1"`, `"9223372036854775808"`} {
				bodies = append(bodies, `{"assignee_id":`+value+`,"expected_revision":"1"}`)
			}
			bodies = append(bodies, `{"expected_revision":"1"}`)
		case "deadline":
			for _, value := range []string{`null`, `"0"`, `-1`, `1.5`, `1e3`, `253402300800000`} {
				bodies = append(bodies, `{"due_at_unix_ms":`+value+`,"expected_revision":"1"}`)
			}
			bodies = append(bodies, `{"expected_revision":"1"}`)
		}
		for _, body := range bodies {
			w := httptest.NewRecorder()
			collectionEditHandler(kind, client)(w, collectionEditHTTPRequest("1", "0", body))
			if w.Code != 400 {
				t.Fatalf("%s %s: %d %s", kind, body, w.Code, w.Body.String())
			}
		}
	}
}

func TestCollectionEditsHTTPRejectBadIdentityOrAuthorizationBeforeRPC(t *testing.T) {
	client := collectionEditClient{
		text: func(context.Context, *pb.EditTaskDraftItemTextRequest) (*pb.GetTaskDraftItemResponse, error) {
			t.Fatal("bad identity reached RPC")
			return nil, nil
		},
		assignee: func(context.Context, *pb.SelectTaskDraftItemAssigneeRequest) (*pb.GetTaskDraftItemResponse, error) {
			t.Fatal("bad identity reached RPC")
			return nil, nil
		},
		deadline: func(context.Context, *pb.EditTaskDraftItemDeadlineRequest) (*pb.GetTaskDraftItemResponse, error) {
			t.Fatal("bad identity reached RPC")
			return nil, nil
		},
	}
	for _, kind := range []string{"text", "assignee", "deadline"} {
		for _, tc := range []struct{ run, index string }{{"0", "0"}, {"01", "0"}, {"+1", "0"}, {"9223372036854775808", "0"}, {"1", ""}, {"1", "-1"}, {"1", "5"}, {"1", "00"}, {"1", "+0"}, {"1", "2147483648"}} {
			w := httptest.NewRecorder()
			collectionEditHandler(kind, client)(w, collectionEditHTTPRequest(tc.run, tc.index, validCollectionEditBody(kind)))
			if w.Code != 400 {
				t.Fatalf("%s %+v: %d", kind, tc, w.Code)
			}
		}
		for _, auth := range []string{"", "Basic token", "Bearer token other"} {
			r := collectionEditHTTPRequest("1", "0", validCollectionEditBody(kind))
			r.Header.Set("Authorization", auth)
			w := httptest.NewRecorder()
			collectionEditHandler(kind, client)(w, r)
			if w.Code != 401 {
				t.Fatalf("auth %q: %d", auth, w.Code)
			}
		}
		r := collectionEditHTTPRequest("1", "0", validCollectionEditBody(kind))
		r.Header.Add("Authorization", "Bearer other")
		w := httptest.NewRecorder()
		collectionEditHandler(kind, client)(w, r)
		if w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
}

func TestCollectionEditTextHTTPAcceptsEscapedUnicodeAtLimits(t *testing.T) {
	client := collectionEditClient{text: func(_ context.Context, req *pb.EditTaskDraftItemTextRequest) (*pb.GetTaskDraftItemResponse, error) {
		if len([]rune(req.Title)) != 200 || len([]rune(req.Description)) != 2000 {
			t.Fatal(req)
		}
		r := collectionEditResponse(1, 2)
		r.Item.Draft.Title, r.Item.Draft.Description = req.Title, req.Description
		return r, nil
	}}
	body := `{"title":"` + strings.Repeat(`\ud83d\ude00`, 200) + `","description":"` + strings.Repeat(`\ud83d\ude00`, 2000) + `","expected_revision":"1"}`
	w := httptest.NewRecorder()
	editTaskDraftItemTextHandler(client)(w, collectionEditHTTPRequest("9007199254740999", "1", body))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestCollectionEditsHTTPRejectBadResultIdentityMetadataOrContent(t *testing.T) {
	for _, kind := range []string{"text", "assignee", "deadline"} {
		for _, mutate := range []func(*pb.GetTaskDraftItemResponse){
			func(r *pb.GetTaskDraftItemResponse) { r.RunId = 1 }, func(r *pb.GetTaskDraftItemResponse) { r.TeamId = 0 }, func(r *pb.GetTaskDraftItemResponse) { r.GroupId = 0 }, func(r *pb.GetTaskDraftItemResponse) { r.ItemCount = 0 }, func(r *pb.GetTaskDraftItemResponse) { r.ItemCount = 6 }, func(r *pb.GetTaskDraftItemResponse) { r.ItemCount = 1 }, func(r *pb.GetTaskDraftItemResponse) { r.Item = nil }, func(r *pb.GetTaskDraftItemResponse) { r.Item.ItemIndex = nil }, func(r *pb.GetTaskDraftItemResponse) { v := int32(0); r.Item.ItemIndex = &v },
			func(r *pb.GetTaskDraftItemResponse) { r.Item.Status = "creating" }, func(r *pb.GetTaskDraftItemResponse) { r.Item.TaskId = 9 }, func(r *pb.GetTaskDraftItemResponse) { r.Item.ReplyStatus = "accepted" }, func(r *pb.GetTaskDraftItemResponse) { r.Item.ReplyMsgId = "fake" }, func(r *pb.GetTaskDraftItemResponse) { r.Item.Draft = nil }, func(r *pb.GetTaskDraftItemResponse) { r.Item.Draft.Revision = 0 }, func(r *pb.GetTaskDraftItemResponse) { r.Item.Draft.Revision = 3 }, func(r *pb.GetTaskDraftItemResponse) { r.Item.Draft.Deadline = nil }, func(r *pb.GetTaskDraftItemResponse) { r.Item.Draft.AssigneeResolution = "" },
		} {
			r := collectionEditResponse(1, 2)
			if kind == "assignee" {
				r.Item.Draft.AssigneeId = 0
				r.Item.Draft.AssigneeResolution = "unassigned"
			}
			if kind == "deadline" {
				r.Item.Draft.Deadline.Resolution = "unset"
			}
			mutate(r)
			w := httptest.NewRecorder()
			collectionEditHandler(kind, collectionResultClient(r, nil))(w, collectionEditHTTPRequest("9007199254740999", "1", validCollectionEditBody(kind)))
			if w.Code != 502 {
				t.Fatalf("%s result %v: %d %s", kind, r, w.Code, w.Body.String())
			}
		}
		w := httptest.NewRecorder()
		collectionEditHandler(kind, collectionResultClient(nil, nil))(w, collectionEditHTTPRequest("9007199254740999", "1", validCollectionEditBody(kind)))
		if w.Code != 502 {
			t.Fatal(w.Code)
		}
	}
	for _, kind := range []string{"text", "assignee", "deadline"} {
		r := collectionEditResponse(1, 2)
		switch kind {
		case "text":
			r.Item.Draft.Title = "Different saved text"
		case "assignee":
			r.Item.Draft.AssigneeId = 0
			r.Item.Draft.AssigneeResolution = "none"
			r.Item.Draft.AssigneeName = ""
		case "deadline":
			r.Item.Draft.Deadline.Resolution = "none"
		}
		w := httptest.NewRecorder()
		collectionEditHandler(kind, collectionResultClient(r, nil))(w, collectionEditHTTPRequest("9007199254740999", "1", validCollectionEditBody(kind)))
		if w.Code != 502 {
			t.Fatalf("%s inconsistent write: %d", kind, w.Code)
		}
	}
	for _, kind := range []string{"text", "assignee", "deadline"} {
		r := collectionEditResponse(1, 2)
		switch kind {
		case "text":
			r.Item.Draft.Description = "Different saved description"
		case "assignee":
			r.Item.Draft.AssigneeResolution = "selected"
		case "deadline":
			r.Item.Draft.DueAtUnixMs = 1791097200123
			r.Item.Draft.Deadline.Resolution = "selected"
		}
		w := httptest.NewRecorder()
		collectionEditHandler(kind, collectionResultClient(r, nil))(w, collectionEditHTTPRequest("9007199254740999", "1", validCollectionEditBody(kind)))
		if w.Code != 502 {
			t.Fatalf("%s saved a different value: %d", kind, w.Code)
		}
	}
}

func TestCollectionEditsHTTPOnlyAllowSameOrSafeNextRevision(t *testing.T) {
	for _, kind := range []string{"text", "assignee", "deadline"} {
		for _, tc := range []struct {
			expected, returned int64
			want               int
		}{{2, 1, 502}, {2, 2, 200}, {2, 3, 200}, {2, 4, 502}, {math.MaxInt64, math.MaxInt64, 200}, {math.MaxInt64, math.MinInt64, 502}} {
			r := collectionEditResponse(1, tc.returned)
			if kind == "assignee" {
				r.Item.Draft.AssigneeId = 0
				r.Item.Draft.AssigneeResolution = "unassigned"
			}
			if kind == "deadline" {
				r.Item.Draft.Deadline.Resolution = "unset"
			}
			body := strings.Replace(validCollectionEditBody(kind), `"expected_revision":"1"`, `"expected_revision":"`+strconv.FormatInt(tc.expected, 10)+`"`, 1)
			w := httptest.NewRecorder()
			collectionEditHandler(kind, collectionResultClient(r, nil))(w, collectionEditHTTPRequest("9007199254740999", "1", body))
			if w.Code != tc.want {
				t.Fatalf("%s %+v: %d %s", kind, tc, w.Code, w.Body.String())
			}
		}
	}
}

func TestCollectionEditsHTTPMapRPCFailuresWithoutDetails(t *testing.T) {
	for _, kind := range []string{"text", "assignee", "deadline"} {
		for _, tc := range []struct {
			code codes.Code
			want int
		}{{codes.Aborted, 409}, {codes.FailedPrecondition, 409}, {codes.PermissionDenied, 403}, {codes.NotFound, 404}, {codes.Unauthenticated, 401}, {codes.InvalidArgument, 400}, {codes.Unavailable, 503}, {codes.DeadlineExceeded, 504}, {codes.Internal, 502}} {
			w := httptest.NewRecorder()
			collectionEditHandler(kind, collectionResultClient(nil, status.Error(tc.code, "private backend details")))(w, collectionEditHTTPRequest("9007199254740999", "0", validCollectionEditBody(kind)))
			if w.Code != tc.want || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("%s %s: %d %s", kind, tc.code, w.Code, w.Body.String())
			}
		}
	}
}

func TestCollectionEditsHTTPRejectValidFrozenOrCreatedResults(t *testing.T) {
	for _, kind := range []string{"text", "assignee", "deadline"} {
		for _, state := range []string{"creating", "succeeded", "skipped"} {
			result := collectionEditResponse(1, 2)
			result.Item.Status = state
			if state == "succeeded" {
				result.Item.TaskId = 9007199254740997
			}
			if kind == "assignee" {
				result.Item.Draft.AssigneeId = 0
				result.Item.Draft.AssigneeResolution = "unassigned"
			}
			if kind == "deadline" {
				result.Item.Draft.Deadline.Resolution = "unset"
			}
			w := httptest.NewRecorder()
			collectionEditHandler(kind, collectionResultClient(result, nil))(w, collectionEditHTTPRequest("9007199254740999", "1", validCollectionEditBody(kind)))
			if w.Code != 502 {
				t.Fatalf("%s %s falsely edited: %d %s", kind, state, w.Code, w.Body.String())
			}
		}
	}
}
