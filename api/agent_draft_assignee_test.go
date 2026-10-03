package main

import (
	"context"
	"encoding/json"
	"math"
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

type draftAssigneeSelectorFunc func(context.Context, *pb.SelectTaskDraftAssigneeRequest) (*pb.GetTaskDraftResponse, error)

func (f draftAssigneeSelectorFunc) SelectTaskDraftAssignee(ctx context.Context, req *pb.SelectTaskDraftAssigneeRequest, _ ...grpc.CallOption) (*pb.GetTaskDraftResponse, error) {
	return f(ctx, req)
}

func selectAssigneeHTTPRequest(id, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPut, "/api/v1/agent/runs/"+id+"/draft/assignee", strings.NewReader(body))
	r = pathvar.WithVars(r, map[string]string{"run_id": id})
	r.Header.Set("Authorization", "Bearer user-token")
	return r
}

func selectedDraftResponse(id int64, resolution string) *pb.GetTaskDraftResponse {
	return &pb.GetTaskDraftResponse{RunId: 1, TeamId: 2, GroupId: 3, Status: "waiting_confirmation",
		Draft: &pb.TaskDraftItem{Revision: 2, Title: "Task", AssigneeId: id, AssigneeName: "小李", AssigneeResolution: resolution}}
}

func TestSelectAssigneeHTTPForwardsExplicitIDRevisionAndToken(t *testing.T) {
	for _, id := range []int64{0, 9007199254740993} {
		client := draftAssigneeSelectorFunc(func(ctx context.Context, req *pb.SelectTaskDraftAssigneeRequest) (*pb.GetTaskDraftResponse, error) {
			md, _ := metadata.FromOutgoingContext(ctx)
			if req.RunId != 1 || req.AssigneeId == nil || req.GetAssigneeId() != id || req.ExpectedRevision != 9007199254740995 ||
				len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" || len(md.Get("idempotency-key")) != 0 {
				t.Fatalf("selection = %v, metadata = %v", req, md)
			}
			resolution := "selected"
			if id == 0 {
				resolution = "unassigned"
			}
			result := selectedDraftResponse(id, resolution)
			result.Draft.Revision = 9007199254740996
			return result, nil
		})
		idText := "9007199254740993"
		if id == 0 {
			idText = "0"
		}
		r := selectAssigneeHTTPRequest("1", `{"assignee_id":"`+idText+`","expected_revision":"9007199254740995"}`)
		r.Header.Set("Idempotency-Key", "browser-key-ignored")
		w := httptest.NewRecorder()
		selectTaskDraftAssigneeHandler(client)(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"assignee_id":"`+idText+`"`) ||
			!strings.Contains(w.Body.String(), `"revision":"9007199254740996"`) || !strings.Contains(w.Body.String(), `"assignee_name":"小李"`) {
			t.Fatalf("response = %d %s", w.Code, w.Body.String())
		}
	}
}

func TestSelectAssigneeHTTPRejectsInvalidInputBeforeRPC(t *testing.T) {
	client := draftAssigneeSelectorFunc(func(context.Context, *pb.SelectTaskDraftAssigneeRequest) (*pb.GetTaskDraftResponse, error) {
		t.Fatal("invalid selection reached RPC")
		return nil, nil
	})
	valid := `{"assignee_id":"0","expected_revision":"1"}`
	for _, body := range []string{
		`{"expected_revision":"1"}`, `{"assignee_id":null,"expected_revision":"1"}`, `{"assignee_id":0,"expected_revision":"1"}`,
		`{"assignee_id":"-1","expected_revision":"1"}`, `{"assignee_id":"00","expected_revision":"1"}`,
		`{"assignee_id":"+1","expected_revision":"1"}`, `{"assignee_id":"9223372036854775808","expected_revision":"1"}`,
		`{"assignee_id":"0"}`, `{"assignee_id":"0","expected_revision":null}`, `{"assignee_id":"0","expected_revision":1}`,
		`{"assignee_id":"0","expected_revision":"0"}`, `{"assignee_id":"0","expected_revision":"01"}`,
		`{"assignee_id":"0","expected_revision":"9223372036854775808"}`,
		`{"assignee_id":"0","expected_revision":"1","team_id":"2"}`, valid + `{}`, valid + strings.Repeat(" ", 1024),
	} {
		w := httptest.NewRecorder()
		selectTaskDraftAssigneeHandler(client)(w, selectAssigneeHTTPRequest("1", body))
		if w.Code != 400 {
			t.Fatalf("body %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	for _, id := range []string{"0", "01", "+1", "-1", "9223372036854775808"} {
		w := httptest.NewRecorder()
		selectTaskDraftAssigneeHandler(client)(w, selectAssigneeHTTPRequest(id, valid))
		if w.Code != 400 {
			t.Fatalf("ID %s: %d", id, w.Code)
		}
	}
	r := selectAssigneeHTTPRequest("1", valid)
	r.Header.Del("Authorization")
	w := httptest.NewRecorder()
	selectTaskDraftAssigneeHandler(client)(w, r)
	if w.Code != 401 {
		t.Fatalf("missing token: %d", w.Code)
	}
}

func TestSelectAssigneeHTTPRejectsMalformedSavedResults(t *testing.T) {
	for _, mutate := range []func(*pb.GetTaskDraftResponse){
		func(r *pb.GetTaskDraftResponse) { r.RunId = 4 }, func(r *pb.GetTaskDraftResponse) { r.TeamId = 0 },
		func(r *pb.GetTaskDraftResponse) { r.Status = "creating" }, func(r *pb.GetTaskDraftResponse) { r.TaskId = 9 },
		func(r *pb.GetTaskDraftResponse) { r.Draft = nil }, func(r *pb.GetTaskDraftResponse) { r.Draft.Revision = 0 },
		func(r *pb.GetTaskDraftResponse) { r.Draft.AssigneeId = 5 }, func(r *pb.GetTaskDraftResponse) { r.Draft.AssigneeResolution = "matched" },
		func(r *pb.GetTaskDraftResponse) { r.Draft.AssigneeName = " 李 " }, func(r *pb.GetTaskDraftResponse) { r.ReplyStatus = "accepted" },
	} {
		result := selectedDraftResponse(4, "selected")
		mutate(result)
		client := draftAssigneeSelectorFunc(func(context.Context, *pb.SelectTaskDraftAssigneeRequest) (*pb.GetTaskDraftResponse, error) {
			return result, nil
		})
		w := httptest.NewRecorder()
		selectTaskDraftAssigneeHandler(client)(w, selectAssigneeHTTPRequest("1", `{"assignee_id":"4","expected_revision":"1"}`))
		if w.Code != 502 {
			t.Fatalf("result %v: %d %s", result, w.Code, w.Body.String())
		}
	}
}

func TestSelectAssigneeHTTPMapsRPCFailures(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{
		{codes.Aborted, 409}, {codes.FailedPrecondition, 409}, {codes.Unauthenticated, 401}, {codes.PermissionDenied, 403},
		{codes.NotFound, 404}, {codes.InvalidArgument, 400}, {codes.Unavailable, 503}, {codes.DeadlineExceeded, 504}, {codes.Internal, 502},
	} {
		client := draftAssigneeSelectorFunc(func(context.Context, *pb.SelectTaskDraftAssigneeRequest) (*pb.GetTaskDraftResponse, error) {
			return nil, status.Error(tc.code, "private detail")
		})
		w := httptest.NewRecorder()
		selectTaskDraftAssigneeHandler(client)(w, selectAssigneeHTTPRequest("1", `{"assignee_id":"0","expected_revision":"1"}`))
		if w.Code != tc.want || strings.Contains(w.Body.String(), "private detail") {
			t.Fatalf("%s: %d %s", tc.code, w.Code, w.Body.String())
		}
	}
}

func TestGetDraftHTTPValidatesAssigneeMetadataWithLegacyCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, resolution string
		id               int64
		want             int
	}{
		{"", "", 7, 200}, {"", "none", 0, 200}, {"李四", "matched", 7, 200},
		{"李四", "not_found", 0, 200}, {"李四", "ambiguous", 0, 200}, {"李四", "truncated", 0, 200},
		{"", "selected", 7, 200}, {"李四", "selected", 7, 200}, {"", "unassigned", 0, 200}, {"李四", "unassigned", 0, 200},
		{"李四", "", 0, 502}, {"", "matched", 7, 502}, {"李四", "matched", 0, 502},
		{"李四", "none", 0, 502}, {"", "none", 7, 502}, {"李四", "ambiguous", 7, 502},
		{"", "selected", 0, 502}, {"", "unassigned", 7, 502}, {"", "unknown", 0, 502},
		{"", "", -1, 502}, {" 李四 ", "selected", 7, 502}, {strings.Repeat("李", 65), "selected", 7, 502},
	} {
		result := selectedDraftResponse(tc.id, tc.resolution)
		result.Draft.AssigneeName = tc.name
		client := draftReaderFunc(func(context.Context, *pb.GetTaskDraftRequest) (*pb.GetTaskDraftResponse, error) { return result, nil })
		w := httptest.NewRecorder()
		getTaskDraftHandler(client)(w, getDraftHTTPRequest("1"))
		if w.Code != tc.want {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
		if tc.want == 200 && tc.resolution != "" && !strings.Contains(w.Body.String(), `"assignee_resolution":"`+tc.resolution+`"`) {
			t.Fatal(w.Body.String())
		}
		if tc.want == 200 {
			var body struct {
				Data struct {
					Draft map[string]json.RawMessage `json:"draft"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Data.Draft["assignee_name"] == nil || body.Data.Draft["assignee_resolution"] == nil {
				t.Fatalf("metadata fields missing for %+v: %s", tc, w.Body.String())
			}
		}
	}
}

func TestSelectAssigneeHTTPRejectsStaleJumpingAndOverflowedVersions(t *testing.T) {
	for _, tc := range []struct {
		request, result string
		want            int
	}{
		{"2", "1", 502}, {"2", "2", 200}, {"2", "3", 200}, {"2", "4", 502},
		{"9223372036854775807", "9223372036854775807", 200}, {"9223372036854775807", "-9223372036854775808", 502},
	} {
		revision := int64(1)
		switch tc.result {
		case "2":
			revision = 2
		case "3":
			revision = 3
		case "4":
			revision = 4
		case "9223372036854775807":
			revision = math.MaxInt64
		case "-9223372036854775808":
			revision = math.MinInt64
		}
		result := selectedDraftResponse(4, "selected")
		result.Draft.Revision = revision
		client := draftAssigneeSelectorFunc(func(context.Context, *pb.SelectTaskDraftAssigneeRequest) (*pb.GetTaskDraftResponse, error) {
			return result, nil
		})
		w := httptest.NewRecorder()
		selectTaskDraftAssigneeHandler(client)(w, selectAssigneeHTTPRequest("1", `{"assignee_id":"4","expected_revision":"`+tc.request+`"}`))
		if w.Code != tc.want {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
}
