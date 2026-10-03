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
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type draftDeadlineEditorFunc func(context.Context, *pb.EditTaskDraftDeadlineRequest) (*pb.GetTaskDraftResponse, error)

func (f draftDeadlineEditorFunc) EditTaskDraftDeadline(ctx context.Context, req *pb.EditTaskDraftDeadlineRequest, _ ...grpc.CallOption) (*pb.GetTaskDraftResponse, error) {
	return f(ctx, req)
}

func deadlineHTTPRequest(id, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPut, "/api/v1/agent/runs/"+id+"/draft/deadline", strings.NewReader(body))
	r = pathvar.WithVars(r, map[string]string{"run_id": id})
	r.Header.Set("Authorization", "Bearer user-token")
	return r
}

func deadlineResponse(deadline, revision int64) *pb.GetTaskDraftResponse {
	return &pb.GetTaskDraftResponse{RunId: 1, TeamId: 2, GroupId: 3, Status: "waiting_confirmation", Draft: &pb.TaskDraftItem{
		Title: "Task", Revision: revision, DueAtUnixMs: deadline,
	}}
}

func TestEditDeadlineHTTPForwardsExplicitTimeAndToken(t *testing.T) {
	for _, deadline := range []int64{0, 1791097200123, maxDraftDeadlineUnixMs} {
		client := draftDeadlineEditorFunc(func(ctx context.Context, req *pb.EditTaskDraftDeadlineRequest) (*pb.GetTaskDraftResponse, error) {
			md, _ := metadata.FromOutgoingContext(ctx)
			if req.GetRunId() != 1 || req.DueAtUnixMs == nil || req.GetDueAtUnixMs() != deadline || req.GetExpectedRevision() != 9007199254740993 ||
				len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" || len(md.Get("idempotency-key")) != 0 {
				t.Fatalf("request %v metadata %v", req, md)
			}
			return deadlineResponse(deadline, 9007199254740994), nil
		})
		text := strconv.FormatInt(deadline, 10)
		r := deadlineHTTPRequest("1", `{"due_at_unix_ms":`+text+`,"expected_revision":"9007199254740993"}`)
		r.Header.Set("Idempotency-Key", "ignored-browser-key")
		w := httptest.NewRecorder()
		editTaskDraftDeadlineHandler(client)(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"due_at_unix_ms":`+text) || !strings.Contains(w.Body.String(), `"revision":"9007199254740994"`) {
			t.Fatalf("deadline %d: %d %s", deadline, w.Code, w.Body.String())
		}
	}
}

func TestEditDeadlineHTTPRejectsInvalidInputBeforeRPC(t *testing.T) {
	client := draftDeadlineEditorFunc(func(context.Context, *pb.EditTaskDraftDeadlineRequest) (*pb.GetTaskDraftResponse, error) {
		t.Fatal("invalid request reached RPC")
		return nil, nil
	})
	valid := `{"due_at_unix_ms":0,"expected_revision":"1"}`
	for _, body := range []string{
		`{"expected_revision":"1"}`, `{"due_at_unix_ms":null,"expected_revision":"1"}`, `{"due_at_unix_ms":"0","expected_revision":"1"}`,
		`{"due_at_unix_ms":0.5,"expected_revision":"1"}`, `{"due_at_unix_ms":1e3,"expected_revision":"1"}`, `{"due_at_unix_ms":-1,"expected_revision":"1"}`,
		`{"due_at_unix_ms":253402300800000,"expected_revision":"1"}`, `{"due_at_unix_ms":9223372036854775808,"expected_revision":"1"}`,
		`{"due_at_unix_ms":0}`, `{"due_at_unix_ms":0,"expected_revision":null}`, `{"due_at_unix_ms":0,"expected_revision":1}`,
		`{"due_at_unix_ms":0,"expected_revision":"0"}`, `{"due_at_unix_ms":0,"expected_revision":"01"}`, `{"due_at_unix_ms":0,"expected_revision":"+1"}`,
		`{"due_at_unix_ms":0,"expected_revision":"9223372036854775808"}`, `{"due_at_unix_ms":0,"expected_revision":"1","team_id":"2"}`,
		valid + `{}`, valid + strings.Repeat(" ", 1024),
	} {
		w := httptest.NewRecorder()
		editTaskDraftDeadlineHandler(client)(w, deadlineHTTPRequest("1", body))
		if w.Code != 400 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	for _, id := range []string{"0", "01", "+1", "-1", "9223372036854775808"} {
		w := httptest.NewRecorder()
		editTaskDraftDeadlineHandler(client)(w, deadlineHTTPRequest(id, valid))
		if w.Code != 400 {
			t.Fatalf("ID %s: %d", id, w.Code)
		}
	}
	r := deadlineHTTPRequest("1", valid)
	r.Header.Del("Authorization")
	w := httptest.NewRecorder()
	editTaskDraftDeadlineHandler(client)(w, r)
	if w.Code != 401 {
		t.Fatalf("missing token: %d", w.Code)
	}
}

func TestEditDeadlineHTTPRejectsInvalidResults(t *testing.T) {
	for _, mutate := range []func(*pb.GetTaskDraftResponse){
		func(r *pb.GetTaskDraftResponse) { r.RunId = 2 }, func(r *pb.GetTaskDraftResponse) { r.TeamId = 0 }, func(r *pb.GetTaskDraftResponse) { r.GroupId = 0 },
		func(r *pb.GetTaskDraftResponse) { r.Status = "creating" }, func(r *pb.GetTaskDraftResponse) { r.TaskId = 4 }, func(r *pb.GetTaskDraftResponse) { r.Draft = nil },
		func(r *pb.GetTaskDraftResponse) { r.Draft.DueAtUnixMs = 1 }, func(r *pb.GetTaskDraftResponse) { r.Draft.Revision = 0 },
		func(r *pb.GetTaskDraftResponse) { r.Draft.Revision = 3 }, func(r *pb.GetTaskDraftResponse) { r.Draft.AssigneeResolution = "unknown" },
		func(r *pb.GetTaskDraftResponse) { r.ReplyStatus = "accepted" },
	} {
		result := deadlineResponse(0, 2)
		mutate(result)
		client := draftDeadlineEditorFunc(func(context.Context, *pb.EditTaskDraftDeadlineRequest) (*pb.GetTaskDraftResponse, error) {
			return result, nil
		})
		w := httptest.NewRecorder()
		editTaskDraftDeadlineHandler(client)(w, deadlineHTTPRequest("1", `{"due_at_unix_ms":0,"expected_revision":"1"}`))
		if w.Code != 502 {
			t.Fatalf("result %v: %d %s", result, w.Code, w.Body.String())
		}
	}
}

func TestEditDeadlineHTTPKeepsNoOpAndMaxRevisionExact(t *testing.T) {
	for _, tc := range []struct {
		expected, returned int64
		want               int
	}{{2, 1, 502}, {2, 2, 200}, {2, 3, 200}, {2, 4, 502}, {math.MaxInt64, math.MaxInt64, 200}, {math.MaxInt64, math.MinInt64, 502}} {
		client := draftDeadlineEditorFunc(func(context.Context, *pb.EditTaskDraftDeadlineRequest) (*pb.GetTaskDraftResponse, error) {
			return deadlineResponse(0, tc.returned), nil
		})
		w := httptest.NewRecorder()
		editTaskDraftDeadlineHandler(client)(w, deadlineHTTPRequest("1", `{"due_at_unix_ms":0,"expected_revision":"`+strconv.FormatInt(tc.expected, 10)+`"}`))
		if w.Code != tc.want {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
}

func TestEditDeadlineHTTPMapsRPCFailures(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{{codes.Aborted, 409}, {codes.FailedPrecondition, 409}, {codes.Unauthenticated, 401}, {codes.PermissionDenied, 403}, {codes.NotFound, 404}, {codes.InvalidArgument, 400}, {codes.Unavailable, 503}, {codes.DeadlineExceeded, 504}, {codes.Internal, 502}} {
		client := draftDeadlineEditorFunc(func(context.Context, *pb.EditTaskDraftDeadlineRequest) (*pb.GetTaskDraftResponse, error) {
			return nil, status.Error(tc.code, "private detail")
		})
		w := httptest.NewRecorder()
		editTaskDraftDeadlineHandler(client)(w, deadlineHTTPRequest("1", `{"due_at_unix_ms":0,"expected_revision":"1"}`))
		if w.Code != tc.want || strings.Contains(w.Body.String(), "private detail") {
			t.Fatalf("%s: %d %s", tc.code, w.Code, w.Body.String())
		}
	}
}

func TestSharedDraftHTTPRejectsOutOfRangeDeadlines(t *testing.T) {
	for _, tc := range []struct {
		deadline int64
		want     int
	}{{-1, 502}, {0, 200}, {maxDraftDeadlineUnixMs, 200}, {maxDraftDeadlineUnixMs + 1, 502}} {
		w := httptest.NewRecorder()
		writeTaskDraftResult(w, 1, deadlineResponse(tc.deadline, 1))
		if w.Code != tc.want {
			t.Fatalf("deadline %d: %d %s", tc.deadline, w.Code, w.Body.String())
		}
	}
}
