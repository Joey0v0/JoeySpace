package main

import (
	"context"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/agent/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestPrepareDraftHTTPPreservesOptionalReferenceAndOriginalContext(t *testing.T) {
	for _, reference := range []*int64{nil, int64Pointer(1), int64Pointer(1791097200123), int64Pointer(maxDraftDeadlineUnixMs)} {
		client := draftPreparerFunc(func(ctx context.Context, req *pb.PrepareTaskDraftRequest) (*pb.PrepareTaskDraftResponse, error) {
			md, _ := metadata.FromOutgoingContext(ctx)
			if (req.InstructionReferenceUnixMs == nil) != (reference == nil) ||
				(reference != nil && req.GetInstructionReferenceUnixMs() != *reference) {
				t.Fatalf("reference missing, defaulted, or rounded: %v", req)
			}
			if req.GetTeamId() != 9007199254740993 || req.GetGroupId() != 9007199254740995 || req.GetInstruction() != "明天 15:00 更新文档" ||
				len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" ||
				len(md.Get("idempotency-key")) != 1 || md.Get("idempotency-key")[0] != "fixed-reference-key" {
				t.Fatalf("original context changed: %v %v", req, md)
			}
			return &pb.PrepareTaskDraftResponse{RunId: 9007199254740997}, nil
		})
		body := `{"instruction":" 明天 15:00 更新文档 "}`
		if reference != nil {
			body = strings.TrimSuffix(body, "}") + `,"instruction_reference_unix_ms":` + strconv.FormatInt(*reference, 10) + "}"
		}
		w := httptest.NewRecorder()
		prepareTaskDraftHandler(client)(w, prepareDraftHTTPRequest("9007199254740993", "9007199254740995", body, "fixed-reference-key"))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"run_id":"9007199254740997"`) || strings.Contains(w.Body.String(), "reference") {
			t.Fatalf("response changed: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestPrepareDraftHTTPRejectsInvalidReferenceBeforeRPC(t *testing.T) {
	client := draftPreparerFunc(func(context.Context, *pb.PrepareTaskDraftRequest) (*pb.PrepareTaskDraftResponse, error) {
		t.Fatal("invalid reference reached RPC")
		return nil, nil
	})
	for _, value := range []string{`null`, `0`, `-1`, `"1"`, `1.5`, `1.0`, `1e3`, `253402300800000`, `9223372036854775808`, `true`, `[]`, `{}`} {
		w := httptest.NewRecorder()
		body := `{"instruction":"x","instruction_reference_unix_ms":` + value + `}`
		prepareTaskDraftHandler(client)(w, prepareDraftHTTPRequest("2", "3", body, "fixed-reference-key"))
		if w.Code != 400 {
			t.Fatalf("reference %s: %d %s", value, w.Code, w.Body.String())
		}
	}
}

func TestPrepareDraftHTTPRetriesIdenticalReferenceAndMapsConflict(t *testing.T) {
	const reference int64 = 1791097200123
	const originalBody = `{"instruction":"明天 15:00 更新文档","instruction_reference_unix_ms":1791097200123}`
	requests := 0
	client := draftPreparerFunc(func(_ context.Context, req *pb.PrepareTaskDraftRequest) (*pb.PrepareTaskDraftResponse, error) {
		requests++
		if req.InstructionReferenceUnixMs == nil {
			t.Fatal("reference lost on retry")
		}
		if requests == 1 {
			if req.GetInstructionReferenceUnixMs() != reference {
				t.Fatal(req)
			}
			return nil, status.Error(codes.Unavailable, "private dependency error")
		}
		if req.GetInstructionReferenceUnixMs() != reference {
			return nil, status.Error(codes.AlreadyExists, "private reference fingerprint")
		}
		return &pb.PrepareTaskDraftResponse{RunId: 9}, nil
	})
	for _, tc := range []struct {
		body string
		want int
	}{
		{originalBody, 503}, {originalBody, 200}, {strings.Replace(originalBody, "1791097200123", "1791097200124", 1), 409},
	} {
		w := httptest.NewRecorder()
		prepareTaskDraftHandler(client)(w, prepareDraftHTTPRequest("2", "3", tc.body, "fixed-reference-key"))
		if w.Code != tc.want || strings.Contains(w.Body.String(), "private") {
			t.Fatalf("reference retry: %d %s", w.Code, w.Body.String())
		}
	}
}
