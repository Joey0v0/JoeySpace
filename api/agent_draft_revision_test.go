package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/agent/pb"
)

func TestDraftHTTPRevisionRequiresCanonicalStringBeforeRPC(t *testing.T) {
	client := draftEditorFunc(func(context.Context, *pb.EditTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
		t.Fatal("invalid revision reached RPC")
		return nil, nil
	})
	for _, value := range []string{"null", "0", "1", "\"0\"", "\"01\"", "\"+1\"", "\"9223372036854775808\""} {
		body := strings.Replace(validDraftEditBody, `"expected_revision":"1"`, `"expected_revision":`+value, 1)
		w := httptest.NewRecorder()
		editTaskDraftHandler(client)(w, editDraftHTTPRequest("1", body))
		if w.Code != 400 {
			t.Fatalf("revision %s: %d %s", value, w.Code, w.Body.String())
		}
	}
	body := strings.Replace(validDraftEditBody, `"expected_revision":"1",`, "", 1)
	w := httptest.NewRecorder()
	editTaskDraftHandler(client)(w, editDraftHTTPRequest("1", body))
	if w.Code != 400 {
		t.Fatalf("missing revision: %d", w.Code)
	}
}

func TestDraftHTTPKeepsLargeRevisionExact(t *testing.T) {
	const revision int64 = 9007199254740993
	client := draftEditorFunc(func(_ context.Context, req *pb.EditTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
		if req.GetExpectedRevision() != revision {
			t.Fatalf("rounded revision: %v", req)
		}
		return &pb.GetTaskDraftResponse{RunId: 1, TeamId: 2, GroupId: 3, Status: "waiting_confirmation", Draft: &pb.TaskDraftItem{Title: req.GetTitle(), Revision: revision + 1}}, nil
	})
	body := strings.Replace(validDraftEditBody, `"expected_revision":"1"`, `"expected_revision":"9007199254740993"`, 1)
	w := httptest.NewRecorder()
	editTaskDraftHandler(client)(w, editDraftHTTPRequest("1", body))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"revision":"9007199254740994"`) {
		t.Fatalf("exact response: %d %s", w.Code, w.Body.String())
	}
}
