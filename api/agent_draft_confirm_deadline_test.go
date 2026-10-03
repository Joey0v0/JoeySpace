package main

import (
	"context"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/agent/pb"
)

func TestConfirmDeadlineHTTPPreservesMissingZeroAndMilliseconds(t *testing.T) {
	for _, deadline := range []*int64{nil, int64Pointer(0), int64Pointer(1791097200123), int64Pointer(maxDraftDeadlineUnixMs)} {
		client := draftConfirmerFunc(func(_ context.Context, req *pb.ConfirmTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
			if (deadline == nil) != (req.ExpectedDueAtUnixMs == nil) || (deadline != nil && req.GetExpectedDueAtUnixMs() != *deadline) {
				t.Fatalf("lost reviewed time: %v", req)
			}
			result := deadlineResponse(req.GetExpectedDueAtUnixMs(), 1)
			result.Status, result.TaskId = "succeeded", 99
			return result, nil
		})
		body := validDraftConfirmBody
		if deadline != nil {
			body = strings.TrimSuffix(body, "}") + `,"expected_due_at_unix_ms":` + strconv.FormatInt(*deadline, 10) + "}"
		}
		w := httptest.NewRecorder()
		confirmTaskDraftHandler(client)(w, confirmDraftHTTPRequest("1", body))
		if w.Code != 200 {
			t.Fatalf("deadline %v: %d %s", deadline, w.Code, w.Body.String())
		}
	}
}

func TestConfirmDeadlineHTTPRejectsInvalidReviewBeforeRPC(t *testing.T) {
	client := draftConfirmerFunc(func(context.Context, *pb.ConfirmTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
		t.Fatal("invalid time reached RPC")
		return nil, nil
	})
	for _, value := range []string{`null`, `"0"`, `0.5`, `1e3`, `-1`, `253402300800000`, `9223372036854775808`, `true`, `[]`, `{}`} {
		body := strings.TrimSuffix(validDraftConfirmBody, "}") + `,"expected_due_at_unix_ms":` + value + "}"
		w := httptest.NewRecorder()
		confirmTaskDraftHandler(client)(w, confirmDraftHTTPRequest("1", body))
		if w.Code != 400 {
			t.Fatalf("review %s: %d %s", value, w.Code, w.Body.String())
		}
	}
}

func TestConfirmDeadlineHTTPRejectsChangedTimeOrVersion(t *testing.T) {
	for _, change := range []func(*pb.GetTaskDraftResponse){func(r *pb.GetTaskDraftResponse) { r.Draft.DueAtUnixMs++ }, func(r *pb.GetTaskDraftResponse) { r.Draft.Revision++ }} {
		result := deadlineResponse(1791097200123, 1)
		result.Status, result.TaskId = "succeeded", 99
		change(result)
		client := draftConfirmerFunc(func(context.Context, *pb.ConfirmTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
			return result, nil
		})
		body := strings.TrimSuffix(validDraftConfirmBody, "}") + `,"expected_due_at_unix_ms":1791097200123}`
		w := httptest.NewRecorder()
		confirmTaskDraftHandler(client)(w, confirmDraftHTTPRequest("1", body))
		if w.Code != 502 {
			t.Fatalf("mismatched result %v: %d %s", result, w.Code, w.Body.String())
		}
	}
}
