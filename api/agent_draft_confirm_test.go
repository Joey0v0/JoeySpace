package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/agent/pb"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type draftConfirmerFunc func(context.Context, *pb.ConfirmTaskDraftRequest) (*pb.GetTaskDraftResponse, error)

func (f draftConfirmerFunc) ConfirmTaskDraft(ctx context.Context, req *pb.ConfirmTaskDraftRequest, _ ...grpc.CallOption) (*pb.GetTaskDraftResponse, error) {
	return f(ctx, req)
}

const validDraftConfirmBody = `{"expected_revision":"1","expected_title":"Task","expected_description":""}`

func confirmDraftHTTPRequest(id, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/agent/runs/"+id+"/confirm", strings.NewReader(body))
	r = pathvar.WithVars(r, map[string]string{"run_id": id})
	r.Header.Set("Authorization", "Bearer user-token")
	return r
}

func TestConfirmTaskDraftHTTPForwardsSavedTextAndReturnsExactTaskID(t *testing.T) {
	client := draftConfirmerFunc(func(ctx context.Context, req *pb.ConfirmTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetRunId() != 9007199254740997 || req.GetExpectedTitle() != " Saved title " || req.GetExpectedDescription() != "" ||
			len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" || len(md.Get("idempotency-key")) != 0 {
			t.Fatalf("confirmation = %v, %v", req, md)
		}
		return &pb.GetTaskDraftResponse{RunId: req.GetRunId(), TeamId: 2, GroupId: 3, Status: "succeeded",
			TaskId: 9223372036854775806, Draft: &pb.TaskDraftItem{Revision: 1, Title: "Saved title"}}, nil
	})
	r := confirmDraftHTTPRequest("9007199254740997", `{"expected_revision":"1","expected_title":" Saved title ","expected_description":""}`)
	r.Header.Set("Idempotency-Key", "untrusted-browser-key")
	w := httptest.NewRecorder()
	confirmTaskDraftHandler(client)(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"task_id":"9223372036854775806"`) ||
		!strings.Contains(w.Body.String(), `"run_id":"9007199254740997"`) {
		t.Fatalf("confirmation result = %d %s", w.Code, w.Body.String())
	}
}

func TestConfirmTaskDraftHTTPRejectsInvalidInputBeforeRPC(t *testing.T) {
	client := draftConfirmerFunc(func(context.Context, *pb.ConfirmTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
		t.Fatal("invalid confirmation reached Agent")
		return nil, nil
	})
	for _, tc := range []struct{ name, id, body string }{
		{"invalid ID", "0", validDraftConfirmBody},
		{"overflow ID", "9223372036854775808", validDraftConfirmBody},
		{"missing description", "1", `{"expected_revision":"1","expected_title":"Task"}`},
		{"null description", "1", `{"expected_revision":"1","expected_title":"Task","expected_description":null}`},
		{"client task key", "1", `{"expected_revision":"1","expected_title":"Task","expected_description":"","request_key":"fake"}`},
		{"client scope", "1", `{"expected_revision":"1","expected_title":"Task","expected_description":"","team_id":"4"}`},
		{"empty title", "1", `{"expected_revision":"1","expected_title":" ","expected_description":""}`},
		{"long title", "1", `{"expected_revision":"1","expected_title":"` + strings.Repeat("字", 201) + `","expected_description":""}`},
		{"long description", "1", `{"expected_revision":"1","expected_title":"Task","expected_description":"` + strings.Repeat("字", 2001) + `"}`},
		{"two objects", "1", validDraftConfirmBody + `{}`},
		{"oversized", "1", validDraftConfirmBody + strings.Repeat(" ", 32768)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			confirmTaskDraftHandler(client)(w, confirmDraftHTTPRequest(tc.id, tc.body))
			if w.Code != 400 {
				t.Fatalf("response = %d %s", w.Code, w.Body.String())
			}
		})
	}
	for _, auth := range []string{"", "Basic token", "Bearer token other"} {
		r := confirmDraftHTTPRequest("1", validDraftConfirmBody)
		r.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		confirmTaskDraftHandler(client)(w, r)
		if w.Code != 401 {
			t.Fatalf("invalid token = %d", w.Code)
		}
	}
	r := confirmDraftHTTPRequest("1", validDraftConfirmBody)
	r.Header.Add("Authorization", "Bearer other")
	w := httptest.NewRecorder()
	confirmTaskDraftHandler(client)(w, r)
	if w.Code != 401 {
		t.Fatalf("duplicate token = %d", w.Code)
	}
}

func TestConfirmTaskDraftHTTPMapsFailuresWithoutReportingTaskSuccess(t *testing.T) {
	for _, tc := range []struct {
		code                     codes.Code
		httpStatus, businessCode int
	}{
		{codes.Aborted, 409, errcode.ErrAgentDraftConflict}, {codes.FailedPrecondition, 409, errcode.ErrAgentDraftConflict},
		{codes.AlreadyExists, 409, errcode.ErrTaskRequestConflict}, {codes.PermissionDenied, 403, errcode.ErrForbidden},
		{codes.Unauthenticated, 401, errcode.ErrUnAuth}, {codes.NotFound, 404, errcode.ErrNotFound},
		{codes.InvalidArgument, 400, errcode.ErrBadRequest}, {codes.Unavailable, 503, errcode.ErrInternal},
		{codes.DeadlineExceeded, 504, errcode.ErrInternal}, {codes.Internal, 502, errcode.ErrInternal},
	} {
		t.Run(tc.code.String(), func(t *testing.T) {
			client := draftConfirmerFunc(func(context.Context, *pb.ConfirmTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
				return nil, status.Error(tc.code, "private detail")
			})
			w := httptest.NewRecorder()
			confirmTaskDraftHandler(client)(w, confirmDraftHTTPRequest("1", validDraftConfirmBody))
			var resp agentDraftResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.httpStatus || resp.Code != tc.businessCode || resp.Data != nil || strings.Contains(w.Body.String(), "private detail") {
				t.Fatalf("error = %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestConfirmTaskDraftHTTPRejectsUnsavedOrMismatchedResult(t *testing.T) {
	for _, resp := range []*pb.GetTaskDraftResponse{
		nil, {RunId: 1, TeamId: 2, GroupId: 3, Status: "creating", Draft: &pb.TaskDraftItem{Revision: 1, Title: "Task"}},
		{RunId: 1, TeamId: 2, GroupId: 3, Status: "succeeded", Draft: &pb.TaskDraftItem{Revision: 1, Title: "Task"}},
		{RunId: 2, TeamId: 2, GroupId: 3, Status: "succeeded", TaskId: 99, Draft: &pb.TaskDraftItem{Revision: 1, Title: "Task"}},
		{RunId: 1, TeamId: 2, GroupId: 3, Status: "succeeded", TaskId: 99},
	} {
		client := draftConfirmerFunc(func(context.Context, *pb.ConfirmTaskDraftRequest) (*pb.GetTaskDraftResponse, error) { return resp, nil })
		w := httptest.NewRecorder()
		confirmTaskDraftHandler(client)(w, confirmDraftHTTPRequest("1", validDraftConfirmBody))
		if w.Code != 502 || strings.Contains(w.Body.String(), `"code":0`) {
			t.Fatalf("invalid result = %d %s", w.Code, w.Body.String())
		}
	}
}

func TestDraftReadHTTPReturnsPersistedResultAndRejectsInconsistentStates(t *testing.T) {
	for _, tc := range []struct {
		state  string
		taskID int64
		want   int
	}{
		{"waiting_confirmation", 0, 200}, {"creating", 0, 200}, {"succeeded", 9223372036854775806, 200},
		{"succeeded", 0, 502}, {"creating", 42, 502}, {"waiting_confirmation", -1, 502},
	} {
		client := draftReaderFunc(func(context.Context, *pb.GetTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
			return &pb.GetTaskDraftResponse{RunId: 1, TeamId: 2, GroupId: 3, Status: tc.state, TaskId: tc.taskID,
				Draft: &pb.TaskDraftItem{Revision: 1, Title: "Task"}}, nil
		})
		w := httptest.NewRecorder()
		getTaskDraftHandler(client)(w, getDraftHTTPRequest("1"))
		if w.Code != tc.want {
			t.Fatalf("read %s = %d %s", tc.state, w.Code, w.Body.String())
		}
		if tc.want == 200 && !strings.Contains(w.Body.String(), `"task_id":"`) {
			t.Fatalf("non-string result = %s", w.Body.String())
		}
	}
}

func TestConfirmTaskDraftHTTPAcceptsEscapedUnicodeAtLimits(t *testing.T) {
	client := draftConfirmerFunc(func(_ context.Context, req *pb.ConfirmTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
		if len([]rune(req.GetExpectedTitle())) != 200 || len([]rune(req.GetExpectedDescription())) != 2000 {
			t.Fatal("wrong text lengths")
		}
		return &pb.GetTaskDraftResponse{RunId: 1, TeamId: 2, GroupId: 3, Status: "succeeded", TaskId: 99,
			Draft: &pb.TaskDraftItem{Revision: 1, Title: req.GetExpectedTitle(), Description: req.GetExpectedDescription()}}, nil
	})
	body := `{"expected_revision":"1","expected_title":"` + strings.Repeat(`\ud83d\ude00`, 200) + `","expected_description":"` + strings.Repeat(`\ud83d\ude00`, 2000) + `"}`
	w := httptest.NewRecorder()
	confirmTaskDraftHandler(client)(w, confirmDraftHTTPRequest("1", body))
	if w.Code != 200 {
		t.Fatalf("escaped text = %d %s", w.Code, w.Body.String())
	}
}
