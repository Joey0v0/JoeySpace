package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
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

type triggerStatusReaderFunc func(context.Context, *pb.GetTaskTriggerStatusRequest) (*pb.GetTaskTriggerStatusResponse, error)

func (f triggerStatusReaderFunc) GetTaskTriggerStatus(ctx context.Context, req *pb.GetTaskTriggerStatusRequest, _ ...grpc.CallOption) (*pb.GetTaskTriggerStatusResponse, error) {
	return f(ctx, req)
}

func triggerStatusHTTPRequest(team, group, message string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/teams/2/groups/3/agent-triggers/4?actor_id=99&run_id=88&team_id=77", nil)
	r = pathvar.WithVars(r, map[string]string{"team_id": team, "group_id": group, "message_id": message})
	r.Header.Set("Authorization", "Bearer user-token")
	return r
}

func TestTriggerStatusHTTPFourStatesExposeOnlyFiveStringIDFields(t *testing.T) {
	for _, execution := range []string{"queued", "running", "exhausted", "completed"} {
		for _, id := range []int64{1, 9007199254740993, math.MaxInt64} {
			t.Run(execution+"/"+strconv.FormatInt(id, 10), func(t *testing.T) {
				runID := int64(0)
				if execution == "completed" {
					runID = id
				}
				calls := 0
				client := triggerStatusReaderFunc(func(ctx context.Context, req *pb.GetTaskTriggerStatusRequest) (*pb.GetTaskTriggerStatusResponse, error) {
					calls++
					md, ok := metadata.FromOutgoingContext(ctx)
					if req.GetMessageId() != id || !ok || len(md) != 1 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" {
						t.Fatalf("request=%v metadata=%v", req, md)
					}
					return &pb.GetTaskTriggerStatusResponse{MessageId: id, TeamId: id, GroupId: id, Status: execution, RunId: runID}, nil
				})
				value := strconv.FormatInt(id, 10)
				request := triggerStatusHTTPRequest(value, value, value)
				request = request.WithContext(metadata.NewOutgoingContext(request.Context(), metadata.Pairs("authorization", "Bearer forged", "actor-id", "forged", "service", "agent", "idempotency-key", "not-used", "x-extra", "private")))
				request.Header.Set("X-Actor-Id", "forged")
				request.Header.Set("Idempotency-Key", "unused-key")
				w := httptest.NewRecorder()
				getTaskTriggerStatusHandler(client)(w, request)
				var body struct {
					Code int            `json:"code"`
					Msg  string         `json:"msg"`
					Data map[string]any `json:"data"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if w.Code != http.StatusOK || body.Code != errcode.Success || body.Msg != "success" || len(body.Data) != 5 || calls != 1 {
					t.Fatalf("HTTP=%d body=%s calls=%d", w.Code, w.Body.String(), calls)
				}
				for _, field := range []string{"message_id", "team_id", "group_id"} {
					if actual, ok := body.Data[field].(string); !ok || actual != value {
						t.Fatalf("field %s lost precision: %v", field, body.Data[field])
					}
				}
				if actual, ok := body.Data["run_id"].(string); !ok || actual != strconv.FormatInt(runID, 10) || body.Data["status"] != execution {
					t.Fatalf("wrong state/run shape: %+v", body.Data)
				}
			})
		}
	}
}

func TestTriggerStatusHTTPRequiresSingleBearerBeforeAnyRPC(t *testing.T) {
	client := triggerStatusReaderFunc(func(context.Context, *pb.GetTaskTriggerStatusRequest) (*pb.GetTaskTriggerStatusResponse, error) {
		t.Fatal("missing or ambiguous Token reached RPC")
		return nil, nil
	})
	for _, headers := range [][]string{nil, {""}, {"Basic private"}, {"Bearer"}, {"Bearer token extra"}, {"private-token"}, {"Bearer one", "Bearer two"}} {
		r := triggerStatusHTTPRequest("2", "3", "4")
		r.Header.Del("Authorization")
		for _, header := range headers {
			r.Header.Add("Authorization", header)
		}
		w := httptest.NewRecorder()
		getTaskTriggerStatusHandler(client)(w, r)
		var body agentTriggerStatusResponse
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusUnauthorized || body.Code != errcode.ErrUnAuth || body.Data != nil {
			t.Fatalf("headers=%v HTTP=%d body=%s", headers, w.Code, w.Body.String())
		}
	}
}

func TestTriggerStatusHTTPRejectsNoncanonicalPathIDsWithoutRPC(t *testing.T) {
	client := triggerStatusReaderFunc(func(context.Context, *pb.GetTaskTriggerStatusRequest) (*pb.GetTaskTriggerStatusResponse, error) {
		t.Fatal("invalid path reached RPC")
		return nil, nil
	})
	for _, field := range []string{"team_id", "group_id", "message_id"} {
		for _, bad := range []string{"", "0", "-1", "+1", "01", "1.0", "1e3", "9223372036854775808", " 1", "1 ", "一", "\xff"} {
			r := triggerStatusHTTPRequest("2", "3", "4")
			vars := pathvar.Vars(r)
			vars[field] = bad
			r = pathvar.WithVars(r, vars)
			w := httptest.NewRecorder()
			getTaskTriggerStatusHandler(client)(w, r)
			var body agentTriggerStatusResponse
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusBadRequest || body.Code != errcode.ErrBadRequest || body.Data != nil {
				t.Fatalf("field=%s bad=%q HTTP=%d body=%s", field, bad, w.Code, w.Body.String())
			}
		}
	}
}

func TestTriggerStatusHTTPRejectsWrongScopeInvalidStateAndRunShape(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*pb.GetTaskTriggerStatusResponse)
		nilResult bool
	}{
		{name: "nil", nilResult: true},
		{name: "wrong message", mutate: func(r *pb.GetTaskTriggerStatusResponse) { r.MessageId = 5 }},
		{name: "wrong team", mutate: func(r *pb.GetTaskTriggerStatusResponse) { r.TeamId = 5 }},
		{name: "wrong group", mutate: func(r *pb.GetTaskTriggerStatusResponse) { r.GroupId = 5 }},
		{name: "zero message", mutate: func(r *pb.GetTaskTriggerStatusResponse) { r.MessageId = 0 }},
		{name: "negative team", mutate: func(r *pb.GetTaskTriggerStatusResponse) { r.TeamId = -1 }},
		{name: "zero group", mutate: func(r *pb.GetTaskTriggerStatusResponse) { r.GroupId = 0 }},
		{name: "unknown status", mutate: func(r *pb.GetTaskTriggerStatusResponse) { r.Status = "failed" }},
		{name: "empty status", mutate: func(r *pb.GetTaskTriggerStatusResponse) { r.Status = "" }},
		{name: "noncanonical status", mutate: func(r *pb.GetTaskTriggerStatusResponse) { r.Status = " completed" }},
		{name: "queued with run", mutate: func(r *pb.GetTaskTriggerStatusResponse) { r.Status = "queued"; r.RunId = 1 }},
		{name: "running with run", mutate: func(r *pb.GetTaskTriggerStatusResponse) { r.Status = "running"; r.RunId = 1 }},
		{name: "exhausted with run", mutate: func(r *pb.GetTaskTriggerStatusResponse) { r.Status = "exhausted"; r.RunId = 1 }},
		{name: "queued negative run", mutate: func(r *pb.GetTaskTriggerStatusResponse) { r.Status = "queued"; r.RunId = -1 }},
		{name: "completed missing run", mutate: func(r *pb.GetTaskTriggerStatusResponse) { r.RunId = 0 }},
		{name: "completed negative run", mutate: func(r *pb.GetTaskTriggerStatusResponse) { r.RunId = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := triggerStatusReaderFunc(func(context.Context, *pb.GetTaskTriggerStatusRequest) (*pb.GetTaskTriggerStatusResponse, error) {
				calls++
				if tc.nilResult {
					return nil, nil
				}
				result := &pb.GetTaskTriggerStatusResponse{MessageId: 4, TeamId: 2, GroupId: 3, Status: "completed", RunId: 9001}
				tc.mutate(result)
				return result, nil
			})
			w := httptest.NewRecorder()
			getTaskTriggerStatusHandler(client)(w, triggerStatusHTTPRequest("2", "3", "4"))
			var body agentTriggerStatusResponse
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusBadGateway || body.Code != errcode.ErrInternal || body.Data != nil || calls != 1 {
				t.Fatalf("HTTP=%d body=%s calls=%d", w.Code, w.Body.String(), calls)
			}
		})
	}
}

func TestTriggerStatusHTTPMapsRPCErrorsWithoutPrivateDetailsOrState(t *testing.T) {
	for _, tc := range []struct {
		rpc        codes.Code
		http, code int
	}{
		{codes.InvalidArgument, 400, errcode.ErrBadRequest}, {codes.Unauthenticated, 401, errcode.ErrUnAuth}, {codes.PermissionDenied, 403, errcode.ErrForbidden},
		{codes.NotFound, 404, errcode.ErrNotFound}, {codes.Unavailable, 503, errcode.ErrInternal}, {codes.DeadlineExceeded, 504, errcode.ErrInternal},
		{codes.Internal, 502, errcode.ErrInternal}, {codes.Unimplemented, 502, errcode.ErrInternal}, {codes.FailedPrecondition, 502, errcode.ErrInternal}, {codes.Canceled, 502, errcode.ErrInternal},
	} {
		t.Run(tc.rpc.String(), func(t *testing.T) {
			calls := 0
			client := triggerStatusReaderFunc(func(context.Context, *pb.GetTaskTriggerStatusRequest) (*pb.GetTaskTriggerStatusResponse, error) {
				calls++
				return &pb.GetTaskTriggerStatusResponse{MessageId: 4, TeamId: 2, GroupId: 3, Status: "completed", RunId: 9001}, status.Error(tc.rpc, "private database/token/model contents")
			})
			w := httptest.NewRecorder()
			getTaskTriggerStatusHandler(client)(w, triggerStatusHTTPRequest("2", "3", "4"))
			var body agentTriggerStatusResponse
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.http || body.Code != tc.code || body.Data != nil || calls != 1 || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "9001") {
				t.Fatalf("HTTP=%d body=%s calls=%d", w.Code, w.Body.String(), calls)
			}
		})
	}
	client := triggerStatusReaderFunc(func(context.Context, *pb.GetTaskTriggerStatusRequest) (*pb.GetTaskTriggerStatusResponse, error) {
		return nil, errors.New("private transport details")
	})
	w := httptest.NewRecorder()
	getTaskTriggerStatusHandler(client)(w, triggerStatusHTTPRequest("2", "3", "4"))
	if w.Code != 502 || strings.Contains(w.Body.String(), "private") {
		t.Fatalf("HTTP=%d body=%s", w.Code, w.Body.String())
	}
}
