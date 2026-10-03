package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/agent/pb"
)

func TestConfirmDraftHTTPForwardsReviewedAssigneeIncludingExplicitZero(t *testing.T) {
	for _, tc := range []struct {
		field      string
		id         *int64
		resolution string
	}{
		{"", nil, ""}, {`,"expected_assignee_id":"0"`, int64Pointer(0), "unassigned"},
		{`,"expected_assignee_id":"9007199254740993"`, int64Pointer(9007199254740993), "selected"},
	} {
		client := draftConfirmerFunc(func(_ context.Context, req *pb.ConfirmTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
			if (tc.id == nil) != (req.ExpectedAssigneeId == nil) || (tc.id != nil && req.GetExpectedAssigneeId() != *tc.id) {
				t.Fatalf("reviewed ID lost or rounded: %v", req)
			}
			result := selectedDraftResponse(req.GetExpectedAssigneeId(), tc.resolution)
			result.Status, result.TaskId, result.Draft.Revision = "succeeded", 99, 1
			result.Draft.AssigneeName = ""
			return result, nil
		})
		body := strings.TrimSuffix(validDraftConfirmBody, "}") + tc.field + "}"
		w := httptest.NewRecorder()
		confirmTaskDraftHandler(client)(w, confirmDraftHTTPRequest("1", body))
		if w.Code != 200 {
			t.Fatalf("reviewed field %s: %d %s", tc.field, w.Code, w.Body.String())
		}
	}
}

func int64Pointer(value int64) *int64 { return &value }

func TestConfirmDraftHTTPRejectsInvalidReviewedIDBeforeRPC(t *testing.T) {
	client := draftConfirmerFunc(func(context.Context, *pb.ConfirmTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
		t.Fatal("invalid reviewed ID reached RPC")
		return nil, nil
	})
	for _, field := range []string{`null`, `0`, `1`, `""`, `"-1"`, `"00"`, `"+1"`, `" 1"`, `"9223372036854775808"`, `[]`, `{}`} {
		body := strings.TrimSuffix(validDraftConfirmBody, "}") + `,"expected_assignee_id":` + field + "}"
		w := httptest.NewRecorder()
		confirmTaskDraftHandler(client)(w, confirmDraftHTTPRequest("1", body))
		if w.Code != 400 {
			t.Fatalf("reviewed ID %s: %d %s", field, w.Code, w.Body.String())
		}
	}
	for _, runID := range []string{"01", "+1"} {
		w := httptest.NewRecorder()
		confirmTaskDraftHandler(client)(w, confirmDraftHTTPRequest(runID, validDraftConfirmBody))
		if w.Code != 400 {
			t.Fatalf("run ID %s: %d", runID, w.Code)
		}
	}
}

func TestConfirmDraftHTTPRejectsInconsistentReviewedResult(t *testing.T) {
	for _, change := range []func(*pb.GetTaskDraftResponse){
		func(r *pb.GetTaskDraftResponse) { r.Draft.AssigneeId = 5 },
		func(r *pb.GetTaskDraftResponse) { r.Draft.Revision = 2 },
		func(r *pb.GetTaskDraftResponse) { r.Draft.AssigneeResolution = "unknown" },
	} {
		result := selectedDraftResponse(4, "selected")
		result.Status, result.TaskId, result.Draft.Revision = "succeeded", 99, 1
		change(result)
		client := draftConfirmerFunc(func(context.Context, *pb.ConfirmTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
			return result, nil
		})
		body := strings.TrimSuffix(validDraftConfirmBody, "}") + `,"expected_assignee_id":"4"}`
		w := httptest.NewRecorder()
		confirmTaskDraftHandler(client)(w, confirmDraftHTTPRequest("1", body))
		if w.Code != 502 || strings.Contains(w.Body.String(), `"code":0`) {
			t.Fatalf("result %v: %d %s", result, w.Code, w.Body.String())
		}
	}
}
