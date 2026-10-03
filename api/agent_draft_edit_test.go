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

type draftEditorFunc func(context.Context, *pb.EditTaskDraftRequest) (*pb.GetTaskDraftResponse, error)

func (f draftEditorFunc) EditTaskDraft(ctx context.Context, req *pb.EditTaskDraftRequest, _ ...grpc.CallOption) (*pb.GetTaskDraftResponse, error) {
	return f(ctx, req)
}

const validDraftEditBody = `{"expected_revision":"1","title":"新标题","description":"","expected_title":"旧标题","expected_description":""}`

func editDraftHTTPRequest(runID, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPut, "/api/v1/agent/runs/"+runID+"/draft", strings.NewReader(body))
	r = pathvar.WithVars(r, map[string]string{"run_id": runID})
	r.Header.Set("Authorization", "Bearer user-token")
	return r
}

func TestEditTaskDraftHTTPForwardsOriginalComparisonAndToken(t *testing.T) {
	client := draftEditorFunc(func(ctx context.Context, req *pb.EditTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetRunId() != 9007199254740997 || req.GetTitle() != "新标题" || req.GetDescription() != "新说明" ||
			req.GetExpectedTitle() != " 旧标题 " || req.GetExpectedDescription() != " 旧说明 " ||
			len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" {
			t.Fatalf("RPC request = %v, metadata = %v", req, md)
		}
		return &pb.GetTaskDraftResponse{
			RunId: req.GetRunId(), TeamId: 9007199254740993, GroupId: 9007199254740995, Status: "waiting_confirmation",
			Draft: &pb.TaskDraftItem{Revision: 1, Title: req.GetTitle(), Description: req.GetDescription(), AssigneeId: 9007199254740999, SourceMessageId: 9007199254741001, DueAtUnixMs: 1790874000000},
		}, nil
	})
	w := httptest.NewRecorder()
	editTaskDraftHandler(client)(w, editDraftHTTPRequest("9007199254740997", `{"expected_revision":"1","title":" 新标题 ","description":" 新说明 ","expected_title":" 旧标题 ","expected_description":" 旧说明 "}`))
	var result agentDraftResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || result.Code != 0 || result.Data == nil || result.Data.Draft == nil ||
		result.Data.RunID != 9007199254740997 || result.Data.Draft.Title != "新标题" ||
		result.Data.Draft.AssigneeID != 9007199254740999 || result.Data.Draft.SourceMessageID != 9007199254741001 ||
		result.Data.Draft.DueAtUnixMs != 1790874000000 || !strings.Contains(w.Body.String(), `"run_id":"9007199254740997"`) {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
}

func TestEditTaskDraftHTTPAcceptsExplicitEmptyDescriptions(t *testing.T) {
	client := draftEditorFunc(func(_ context.Context, req *pb.EditTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
		if req.GetDescription() != "" || req.GetExpectedDescription() != "" || req.GetExpectedTitle() != "旧标题" {
			t.Fatalf("empty description request = %v", req)
		}
		return &pb.GetTaskDraftResponse{RunId: 1, TeamId: 2, GroupId: 3, Status: "waiting_confirmation", Draft: &pb.TaskDraftItem{Revision: 1, Title: req.GetTitle()}}, nil
	})
	w := httptest.NewRecorder()
	editTaskDraftHandler(client)(w, editDraftHTTPRequest("1", validDraftEditBody))
	if w.Code != 200 {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
}

func TestEditTaskDraftHTTPRejectsInvalidRequestBeforeRPC(t *testing.T) {
	client := draftEditorFunc(func(context.Context, *pb.EditTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
		t.Fatal("invalid request reached Agent")
		return nil, nil
	})
	for _, tc := range []struct {
		name, id, body, auth string
		want                 int
	}{
		{"missing token", "1", validDraftEditBody, "", 401},
		{"wrong token scheme", "1", validDraftEditBody, "Basic token", 401},
		{"bad ID", "0", validDraftEditBody, "Bearer token", 400},
		{"overflow ID", "9223372036854775808", validDraftEditBody, "Bearer token", 400},
		{"missing old description", "1", `{"expected_revision":"1","title":"new","description":"","expected_title":"old"}`, "Bearer token", 400},
		{"null field", "1", `{"expected_revision":"1","title":"new","description":null,"expected_title":"old","expected_description":""}`, "Bearer token", 400},
		{"unknown scope", "1", `{"expected_revision":"1","title":"new","description":"","expected_title":"old","expected_description":"","team_id":"9"}`, "Bearer token", 400},
		{"empty title", "1", `{"expected_revision":"1","title":" ","description":"","expected_title":"old","expected_description":""}`, "Bearer token", 400},
		{"long old title", "1", `{"expected_revision":"1","title":"new","description":"","expected_title":"` + strings.Repeat("a", 201) + `","expected_description":""}`, "Bearer token", 400},
		{"long description", "1", `{"title":"new","description":"` + strings.Repeat("a", 2001) + `","expected_title":"old","expected_description":""}`, "Bearer token", 400},
		{"trailing JSON", "1", validDraftEditBody + `{}`, "Bearer token", 400},
		{"oversized body", "1", validDraftEditBody + strings.Repeat(" ", 32768), "Bearer token", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := editDraftHTTPRequest(tc.id, tc.body)
			r.Header.Del("Authorization")
			if tc.auth != "" {
				r.Header.Set("Authorization", tc.auth)
			}
			w := httptest.NewRecorder()
			editTaskDraftHandler(client)(w, r)
			if w.Code != tc.want {
				t.Fatalf("response = %d %s", w.Code, w.Body.String())
			}
		})
	}
	r := editDraftHTTPRequest("1", validDraftEditBody)
	r.Header.Add("Authorization", "Bearer other")
	w := httptest.NewRecorder()
	editTaskDraftHandler(client)(w, r)
	if w.Code != 401 {
		t.Fatalf("duplicate token = %d", w.Code)
	}
}

func TestEditTaskDraftHTTPMapsConflictAndBackendFailures(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{
		{codes.Aborted, 409}, {codes.FailedPrecondition, 409}, {codes.PermissionDenied, 403},
		{codes.Unauthenticated, 401}, {codes.NotFound, 404}, {codes.InvalidArgument, 400},
		{codes.Unavailable, 503}, {codes.DeadlineExceeded, 504}, {codes.Internal, 502},
	} {
		t.Run(tc.code.String(), func(t *testing.T) {
			client := draftEditorFunc(func(context.Context, *pb.EditTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
				return nil, status.Error(tc.code, "private database detail")
			})
			w := httptest.NewRecorder()
			editTaskDraftHandler(client)(w, editDraftHTTPRequest("1", validDraftEditBody))
			var result agentDraftResponse
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.want || result.Code == 0 || result.Data != nil || strings.Contains(w.Body.String(), "private database detail") {
				t.Fatalf("response = %d %s", w.Code, w.Body.String())
			}
			if tc.want == 409 && result.Code != errcode.ErrAgentDraftConflict {
				t.Fatalf("conflict code = %d", result.Code)
			}
		})
	}
}

func TestEditTaskDraftHTTPRejectsInvalidRPCResponse(t *testing.T) {
	for _, resp := range []*pb.GetTaskDraftResponse{
		nil, {RunId: 2}, {RunId: 1, TeamId: 2, GroupId: 3},
		{RunId: 1, TeamId: 0, GroupId: 3, Draft: &pb.TaskDraftItem{Revision: 1, Title: "new"}},
	} {
		client := draftEditorFunc(func(context.Context, *pb.EditTaskDraftRequest) (*pb.GetTaskDraftResponse, error) { return resp, nil })
		w := httptest.NewRecorder()
		editTaskDraftHandler(client)(w, editDraftHTTPRequest("1", validDraftEditBody))
		if w.Code != 502 {
			t.Fatalf("response = %d %s", w.Code, w.Body.String())
		}
	}
}

func TestEditTaskDraftHTTPSupportsEscapedUnicodeAtFieldLimits(t *testing.T) {
	client := draftEditorFunc(func(_ context.Context, req *pb.EditTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
		if len([]rune(req.GetTitle())) != 200 || len([]rune(req.GetDescription())) != 2000 || len([]rune(req.GetExpectedDescription())) != 2000 {
			t.Fatalf("wrong decoded text lengths")
		}
		return &pb.GetTaskDraftResponse{RunId: 1, TeamId: 2, GroupId: 3, Status: "waiting_confirmation", Draft: &pb.TaskDraftItem{Revision: 1, Title: req.GetTitle(), Description: req.GetDescription()}}, nil
	})
	body := `{"expected_revision":"1","title":"` + strings.Repeat(`\u4efb`, 200) + `","description":"` + strings.Repeat(`\u4efb`, 2000) + `","expected_title":"` + strings.Repeat(`\u4efb`, 200) + `","expected_description":"` + strings.Repeat(`\u4efb`, 2000) + `"}`
	w := httptest.NewRecorder()
	editTaskDraftHandler(client)(w, editDraftHTTPRequest("1", body))
	if w.Code != 200 {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
}
