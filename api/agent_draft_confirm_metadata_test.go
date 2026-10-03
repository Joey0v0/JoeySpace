package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/agent/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConfirmDeadlineMetadataHTTPForwardsReviewedResolution(t *testing.T) {
	for _, state := range []string{"", "none", "parsed", "selected", "unset"} {
		client := draftConfirmerFunc(func(_ context.Context, req *pb.ConfirmTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
			if req.GetExpectedDeadlineResolution() != state {
				t.Fatalf("state lost: %v", req)
			}
			result := deadlineResponse(0, 1)
			result.Status, result.TaskId = "succeeded", 99
			if state != "" {
				result.Draft = gatewayParsedDeadline()
				result.Draft.Deadline.Resolution = state
				if state == "none" {
					result.Draft.Deadline = &pb.TaskDraftDeadline{Source: "none", Timezone: "Asia/Shanghai", Resolution: "none"}
				}
				if state == "none" || state == "unset" {
					result.Draft.DueAtUnixMs = 0
				}
			}
			return result, nil
		})
		body := strings.TrimSuffix(validDraftConfirmBody, "}") + `,"expected_deadline_resolution":"` + state + `"}`
		if state != "" {
			deadline := "1791097200123"
			if state == "none" || state == "unset" {
				deadline = "0"
			}
			body = strings.TrimSuffix(body, "}") + `,"expected_due_at_unix_ms":` + deadline + "}"
		}
		w := httptest.NewRecorder()
		confirmTaskDraftHandler(client)(w, confirmDraftHTTPRequest("1", body))
		if w.Code != 200 {
			t.Fatalf("state %q: %d %s", state, w.Code, w.Body.String())
		}
	}
}

func TestConfirmDeadlineMetadataHTTPRejectsInvalidStateBeforeRPC(t *testing.T) {
	client := draftConfirmerFunc(func(context.Context, *pb.ConfirmTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
		t.Fatal("invalid state reached RPC")
		return nil, nil
	})
	for _, value := range []string{`null`, `1`, `true`, `[]`, `{}`, `"unknown"`, `" parsed"`, `"PARSED"`} {
		body := strings.TrimSuffix(validDraftConfirmBody, "}") + `,"expected_deadline_resolution":` + value + "}"
		w := httptest.NewRecorder()
		confirmTaskDraftHandler(client)(w, confirmDraftHTTPRequest("1", body))
		if w.Code != 400 {
			t.Fatalf("%s: %d %s", value, w.Code, w.Body.String())
		}
	}
}

func TestConfirmDeadlineMetadataHTTPForwardsNeedsInputToAgent(t *testing.T) {
	client := draftConfirmerFunc(func(_ context.Context, req *pb.ConfirmTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
		if req.GetExpectedDeadlineResolution() != "needs_input" {
			t.Fatal(req)
		}
		return nil, status.Error(codes.FailedPrecondition, "private clarification state")
	})
	body := strings.TrimSuffix(validDraftConfirmBody, "}") + `,"expected_deadline_resolution":"needs_input","expected_due_at_unix_ms":0}`
	w := httptest.NewRecorder()
	confirmTaskDraftHandler(client)(w, confirmDraftHTTPRequest("1", body))
	if w.Code != 409 || strings.Contains(w.Body.String(), "private") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestConfirmDeadlineMetadataHTTPRejectsDifferentOrMissingResultState(t *testing.T) {
	for _, tc := range []struct {
		requested   string
		resultState string
	}{
		{"", "parsed"}, {"selected", "parsed"}, {"parsed", ""},
	} {
		result := deadlineResponse(1791097200123, 1)
		result.Status, result.TaskId = "succeeded", 99
		if tc.resultState != "" {
			result.Draft = gatewayParsedDeadline()
			result.Draft.Deadline.Resolution = tc.resultState
		}
		client := draftConfirmerFunc(func(context.Context, *pb.ConfirmTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
			return result, nil
		})
		body := validDraftConfirmBody
		if tc.requested != "" {
			body = strings.TrimSuffix(body, "}") + `,"expected_deadline_resolution":"` + tc.requested + `"}`
		}
		w := httptest.NewRecorder()
		confirmTaskDraftHandler(client)(w, confirmDraftHTTPRequest("1", body))
		if w.Code != 502 {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
}

func TestEditDeadlineMetadataHTTPRequiresSavedManualState(t *testing.T) {
	for _, tc := range []struct {
		due   int64
		state string
		want  int
	}{
		{1791097200123, "selected", 200}, {0, "unset", 200}, {1791097200123, "parsed", 502}, {0, "none", 502}, {0, "needs_input", 502},
	} {
		result := deadlineResponse(tc.due, 2)
		result.Draft.Deadline = &pb.TaskDraftDeadline{Source: "none", Timezone: "Asia/Shanghai", Resolution: tc.state}
		if tc.state == "parsed" {
			result.Draft = gatewayParsedDeadline()
			result.Draft.Revision = 2
		}
		client := draftDeadlineEditorFunc(func(context.Context, *pb.EditTaskDraftDeadlineRequest) (*pb.GetTaskDraftResponse, error) {
			return result, nil
		})
		body := `{"due_at_unix_ms":0,"expected_revision":"1"}`
		if tc.due > 0 {
			body = `{"due_at_unix_ms":1791097200123,"expected_revision":"1"}`
		}
		w := httptest.NewRecorder()
		editTaskDraftDeadlineHandler(client)(w, deadlineHTTPRequest("1", body))
		if w.Code != tc.want {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
}
