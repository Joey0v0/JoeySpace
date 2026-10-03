package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/agent/pb"
	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type agentAskerFunc func(context.Context, *pb.AskRequest) (*pb.AskResponse, error)

func (f agentAskerFunc) Ask(ctx context.Context, req *pb.AskRequest, _ ...grpc.CallOption) (*pb.AskResponse, error) {
	return f(ctx, req)
}

func askHTTPRequest(teamID, groupID, body string, headers ...string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/teams/"+teamID+"/groups/"+groupID+"/ask", strings.NewReader(body))
	r = pathvar.WithVars(r, map[string]string{"team_id": teamID, "group_id": groupID})
	for _, header := range headers {
		r.Header.Add("Authorization", header)
	}
	return r
}

func TestGatewayConfigLoadsAgentRPC(t *testing.T) {
	for _, tc := range []struct{ file, endpoint string }{
		{"etc/api.yaml", "127.0.0.1:9004"},
		{"../deploy/api-gateway.yaml", "agent-rpc:9004"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			var c config
			if err := conf.Load(tc.file, &c); err != nil {
				t.Fatal(err)
			}
			if len(c.AgentRPC.Endpoints) != 1 || c.AgentRPC.Endpoints[0] != tc.endpoint || c.AgentRPC.Timeout != 21000 || c.Timeout != 3000 || !c.AgentRPC.NonBlock {
				t.Fatalf("Agent/Gateway config = %+v", c)
			}
		})
	}
}

func TestAskAgentHTTPForwardsAuthorizedQuestion(t *testing.T) {
	client := agentAskerFunc(func(ctx context.Context, req *pb.AskRequest) (*pb.AskResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 9007199254740993 || req.GetGroupId() != 9007199254740995 || req.GetQuestion() != "总结待办" || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" {
			t.Fatalf("wrong Agent RPC request: %v, %v", req, md)
		}
		return &pb.AskResponse{Answer: "需要复核缓存任务。"}, nil
	})
	w := httptest.NewRecorder()
	askAgentHandler(client)(w, askHTTPRequest("9007199254740993", "9007199254740995", `{"question":" 总结待办 "}`, "Bearer user-token"))
	var result askAgentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || result.Code != 0 || result.Data == nil || result.Data.Answer != "需要复核缓存任务。" {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
}

func TestAskAgentHTTPRejectsInvalidInputBeforeRPC(t *testing.T) {
	client := agentAskerFunc(func(context.Context, *pb.AskRequest) (*pb.AskResponse, error) {
		t.Fatal("invalid HTTP input reached Agent RPC")
		return nil, nil
	})
	for _, tc := range []struct {
		name, team, group, body string
		headers                 []string
		want                    int
	}{
		{"no token", "200", "300", `{"question":"hi"}`, nil, 401},
		{"wrong scheme", "200", "300", `{"question":"hi"}`, []string{"Basic token"}, 401},
		{"duplicate token", "200", "300", `{"question":"hi"}`, []string{"Bearer token", "Bearer other"}, 401},
		{"bad team", "0", "300", `{"question":"hi"}`, []string{"Bearer token"}, 400},
		{"bad group", "200", "bad", `{"question":"hi"}`, []string{"Bearer token"}, 400},
		{"empty question", "200", "300", `{"question":"  "}`, []string{"Bearer token"}, 400},
		{"long question", "200", "300", `{"question":"` + strings.Repeat("a", 2001) + `"}`, []string{"Bearer token"}, 400},
		{"unknown field", "200", "300", `{"question":"hi","team_id":"999"}`, []string{"Bearer token"}, 400},
		{"two objects", "200", "300", `{"question":"hi"}{"question":"again"}`, []string{"Bearer token"}, 400},
		{"broken JSON", "200", "300", `{`, []string{"Bearer token"}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			askAgentHandler(client)(w, askHTTPRequest(tc.team, tc.group, tc.body, tc.headers...))
			if w.Code != tc.want {
				t.Fatalf("response = %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestAskAgentHTTPMapsRPCFailuresWithoutLeakingDetails(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{
		{codes.InvalidArgument, 400}, {codes.Unauthenticated, 401},
		{codes.PermissionDenied, 403}, {codes.NotFound, 404},
		{codes.Unavailable, 503}, {codes.FailedPrecondition, 503},
		{codes.DeadlineExceeded, 504}, {codes.Internal, 502},
	} {
		t.Run(tc.code.String(), func(t *testing.T) {
			client := agentAskerFunc(func(context.Context, *pb.AskRequest) (*pb.AskResponse, error) {
				return nil, status.Error(tc.code, "private model or RPC detail")
			})
			w := httptest.NewRecorder()
			askAgentHandler(client)(w, askHTTPRequest("200", "300", `{"question":"hi"}`, "Bearer token"))
			if w.Code != tc.want || strings.Contains(w.Body.String(), "private model or RPC detail") || strings.Contains(w.Body.String(), `"data"`) {
				t.Fatalf("RPC %v response = %d %s", tc.code, w.Code, w.Body.String())
			}
		})
	}
}

func TestAskAgentHTTPRejectsEmptyRPCAnswer(t *testing.T) {
	client := agentAskerFunc(func(context.Context, *pb.AskRequest) (*pb.AskResponse, error) {
		return &pb.AskResponse{}, nil
	})
	w := httptest.NewRecorder()
	askAgentHandler(client)(w, askHTTPRequest("200", "300", `{"question":"hi"}`, "Bearer token"))
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), `"data"`) {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
}
