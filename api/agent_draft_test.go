package main

import (
	"context"
	"encoding/json"
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

type draftPreparerFunc func(context.Context, *pb.PrepareTaskDraftRequest) (*pb.PrepareTaskDraftResponse, error)

func (f draftPreparerFunc) PrepareTaskDraft(ctx context.Context, req *pb.PrepareTaskDraftRequest, _ ...grpc.CallOption) (*pb.PrepareTaskDraftResponse, error) {
	return f(ctx, req)
}

type draftReaderFunc func(context.Context, *pb.GetTaskDraftRequest) (*pb.GetTaskDraftResponse, error)

func (f draftReaderFunc) GetTaskDraft(ctx context.Context, req *pb.GetTaskDraftRequest, _ ...grpc.CallOption) (*pb.GetTaskDraftResponse, error) {
	return f(ctx, req)
}

func prepareDraftHTTPRequest(teamID, groupID, body, key string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/teams/"+teamID+"/groups/"+groupID+"/task-drafts", strings.NewReader(body))
	r = pathvar.WithVars(r, map[string]string{"team_id": teamID, "group_id": groupID})
	r.Header.Set("Authorization", "Bearer user-token")
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	return r
}

func getDraftHTTPRequest(runID string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/agent/runs/"+runID+"/draft", nil)
	r = pathvar.WithVars(r, map[string]string{"run_id": runID})
	r.Header.Set("Authorization", "Bearer user-token")
	return r
}

func TestPrepareTaskDraftHTTPForwardsScopeAndKey(t *testing.T) {
	client := draftPreparerFunc(func(ctx context.Context, req *pb.PrepareTaskDraftRequest) (*pb.PrepareTaskDraftResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 9007199254740993 || req.GetGroupId() != 9007199254740995 || req.GetInstruction() != "提取待办" || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" || len(md.Get("idempotency-key")) != 1 || md.Get("idempotency-key")[0] != "draft-123" {
			t.Fatalf("wrong Agent request: %v, %v", req, md)
		}
		return &pb.PrepareTaskDraftResponse{RunId: 9007199254740997}, nil
	})
	w := httptest.NewRecorder()
	prepareTaskDraftHandler(client)(w, prepareDraftHTTPRequest("9007199254740993", "9007199254740995", `{"instruction":" 提取待办 "}`, "draft-123"))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"run_id":"9007199254740997"`) {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
}

func TestPrepareTaskDraftHTTPRejectsInvalidInput(t *testing.T) {
	client := draftPreparerFunc(func(context.Context, *pb.PrepareTaskDraftRequest) (*pb.PrepareTaskDraftResponse, error) {
		t.Fatal("invalid request reached Agent RPC")
		return nil, nil
	})
	for _, tc := range []struct {
		name, team, group, body, key string
		removeToken                  bool
		want                         int
	}{
		{"missing token", "2", "3", `{"instruction":"x"}`, "draft-1", true, 401},
		{"missing key", "2", "3", `{"instruction":"x"}`, "", false, 400},
		{"bad key", "2", "3", `{"instruction":"x"}`, "bad key", false, 400},
		{"bad team", "0", "3", `{"instruction":"x"}`, "draft-1", false, 400},
		{"bad group", "2", "bad", `{"instruction":"x"}`, "draft-1", false, 400},
		{"empty instruction", "2", "3", `{"instruction":" "}`, "draft-1", false, 400},
		{"unknown field", "2", "3", `{"instruction":"x","user_id":1}`, "draft-1", false, 400},
		{"two objects", "2", "3", `{"instruction":"x"}{"instruction":"y"}`, "draft-1", false, 400},
		{"too long", "2", "3", `{"instruction":"` + strings.Repeat("x", 2001) + `"}`, "draft-1", false, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := prepareDraftHTTPRequest(tc.team, tc.group, tc.body, tc.key)
			if tc.removeToken {
				r.Header.Del("Authorization")
			}
			w := httptest.NewRecorder()
			prepareTaskDraftHandler(client)(w, r)
			if w.Code != tc.want {
				t.Fatalf("response = %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestGetTaskDraftHTTPReturnsStringIDsAndForwardsToken(t *testing.T) {
	client := draftReaderFunc(func(ctx context.Context, req *pb.GetTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetRunId() != 9007199254740997 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" {
			t.Fatalf("wrong Agent request: %v, %v", req, md)
		}
		return &pb.GetTaskDraftResponse{
			RunId: req.GetRunId(), TeamId: 9007199254740993, GroupId: 9007199254740995, Status: "waiting_confirmation",
			Draft: &pb.TaskDraftItem{Revision: 1, Title: "更新文档", Description: "整理会议记录", SourceMessageId: 9007199254740999},
		}, nil
	})
	w := httptest.NewRecorder()
	getTaskDraftHandler(client)(w, getDraftHTTPRequest("9007199254740997"))
	if w.Code != http.StatusOK {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	data := result["data"].(map[string]any)
	draft := data["draft"].(map[string]any)
	if data["run_id"] != "9007199254740997" || data["team_id"] != "9007199254740993" || data["group_id"] != "9007199254740995" || data["status"] != "waiting_confirmation" || draft["source_message_id"] != "9007199254740999" || draft["title"] != "更新文档" {
		t.Fatalf("wrong draft data: %v", data)
	}
}

func TestGetTaskDraftHTTPRejectsInvalidInputAndDeniedAccess(t *testing.T) {
	called := false
	client := draftReaderFunc(func(context.Context, *pb.GetTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
		called = true
		return nil, status.Error(codes.PermissionDenied, "private detail")
	})
	for _, id := range []string{"0", "bad", "9223372036854775808"} {
		w := httptest.NewRecorder()
		getTaskDraftHandler(client)(w, getDraftHTTPRequest(id))
		if w.Code != 400 || called {
			t.Fatalf("invalid ID response = %d, called = %v", w.Code, called)
		}
	}
	r := getDraftHTTPRequest("1")
	r.Header.Del("Authorization")
	w := httptest.NewRecorder()
	getTaskDraftHandler(client)(w, r)
	if w.Code != 401 || called {
		t.Fatalf("missing token response = %d, called = %v", w.Code, called)
	}
	w = httptest.NewRecorder()
	getTaskDraftHandler(client)(w, getDraftHTTPRequest("1"))
	if w.Code != 403 || strings.Contains(w.Body.String(), "private detail") {
		t.Fatalf("permission response = %d %s", w.Code, w.Body.String())
	}
}

func TestPrepareTaskDraftHTTPConflict(t *testing.T) {
	client := draftPreparerFunc(func(context.Context, *pb.PrepareTaskDraftRequest) (*pb.PrepareTaskDraftResponse, error) {
		return nil, status.Error(codes.AlreadyExists, "internal fingerprint")
	})
	w := httptest.NewRecorder()
	prepareTaskDraftHandler(client)(w, prepareDraftHTTPRequest("2", "3", `{"instruction":"x"}`, "draft-1"))
	if w.Code != 409 || strings.Contains(w.Body.String(), "internal fingerprint") {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
}
