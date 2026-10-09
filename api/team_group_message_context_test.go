package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/im/pb"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"github.com/zeromicro/go-zero/rest/router"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type messageContextClient struct {
	pb.IMClient
	call func(context.Context, *pb.GetTeamGroupMessageContextRequest) (*pb.GetTeamGroupMessageContextResponse, error)
}

func (c messageContextClient) GetTeamGroupMessageContext(ctx context.Context, r *pb.GetTeamGroupMessageContextRequest, _ ...grpc.CallOption) (*pb.GetTeamGroupMessageContextResponse, error) {
	return c.call(ctx, r)
}
func messageContextRequest(team, group, message, query, auth string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/teams/"+team+"/groups/"+group+"/messages/"+message+"/context"+query, nil)
	r = pathvar.WithVars(r, map[string]string{"team_id": team, "group_id": group, "message_id": message})
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	return r
}
func validMessageContext() *pb.GetTeamGroupMessageContextResponse {
	return &pb.GetTeamGroupMessageContextResponse{TargetMessageId: 4, Messages: []*pb.TeamGroupMessage{{Id: 4, MsgId: "msg", FromId: 42, ContentType: 1, Content: "private content", CreatedAtUnixMs: 1}}}
}
func TestMessageContextHTTPInvalidInputs(t *testing.T) {
	client := messageContextClient{call: func(context.Context, *pb.GetTeamGroupMessageContextRequest) (*pb.GetTeamGroupMessageContextResponse, error) {
		t.Fatal("invalid request reached RPC")
		return nil, nil
	}}
	for _, tc := range []struct {
		team, group, message, query, auth string
		want                              int
	}{
		{"200", "300", "4", "", "", 401}, {"200", "300", "4", "", "Basic token", 401}, {"200", "300", "4", "", "Bearer", 401}, {"200", "300", "4", "", "Bearer a b", 401},
		{"0", "300", "4", "", "Bearer token", 400}, {"200", "-1", "4", "", "Bearer token", 400}, {"200", "300", "0", "", "Bearer token", 400}, {"200", "300", "x", "", "Bearer token", 400}, {"200", "300", "9223372036854775808", "", "Bearer token", 400}, {"+200", "300", "4", "", "Bearer token", 400},
		{"200", "300", "4", "?limit=20", "Bearer token", 400}, {"200", "300", "4", "?x=1&x=2", "Bearer token", 400}, {"200", "300", "4", "?x=%zz", "Bearer token", 400}, {"200", "300", "4", "?x=1;y=2", "Bearer token", 400},
	} {
		w := httptest.NewRecorder()
		getTeamGroupMessageContextHandler(client)(w, messageContextRequest(tc.team, tc.group, tc.message, tc.query, tc.auth))
		if w.Code != tc.want {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body)
		}
	}
	r := messageContextRequest("200", "300", "4", "", "Bearer a")
	r.Header.Add("Authorization", "Bearer b")
	w := httptest.NewRecorder()
	getTeamGroupMessageContextHandler(client)(w, r)
	if w.Code != 401 {
		t.Fatalf("duplicate auth: %d", w.Code)
	}
}
func TestMessageContextHTTPFieldsAndMetadata(t *testing.T) {
	client := messageContextClient{call: func(ctx context.Context, req *pb.GetTeamGroupMessageContextRequest) (*pb.GetTeamGroupMessageContextResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.TeamId != 9007199254740993 || req.GroupId != 9007199254740995 || req.MessageId != 9007199254740997 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer token" {
			t.Fatalf("request: %v %v", req, md)
		}
		return &pb.GetTeamGroupMessageContextResponse{TargetMessageId: req.MessageId, Messages: []*pb.TeamGroupMessage{
			{Id: req.MessageId - 1, MsgId: "user", FromId: 9007199254740999, ContentType: 1, Content: "hello", CreatedAtUnixMs: 9007199254741001},
			{Id: req.MessageId, MsgId: "bot", FromId: 99, SenderType: 2, InitiatorId: 9007199254741003, ContentType: 100, Content: "card", CreatedAtUnixMs: 9007199254741005, MentionedUserIds: []int64{9007199254741007}},
		}}, nil
	}}
	r := messageContextRequest("9007199254740993", "9007199254740995", "9007199254740997", "", "bEaReR   token")
	r = r.WithContext(metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer old", "authorization", "Bearer other")))
	w := httptest.NewRecorder()
	getTeamGroupMessageContextHandler(client)(w, r)
	var result struct {
		Data struct {
			Target   string `json:"target_message_id"`
			Messages []struct {
				ID        string   `json:"id"`
				From      string   `json:"from_id"`
				Sender    int32    `json:"sender_type"`
				Initiator string   `json:"initiator_id"`
				Created   string   `json:"created_at_unix_ms"`
				Content   string   `json:"content"`
				Mentions  []string `json:"mentioned_user_ids"`
			} `json:"messages"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 {
		t.Fatalf("response %d %s %v", w.Code, w.Body, err)
	}
	if result.Data.Target != "9007199254740997" || len(result.Data.Messages) != 2 {
		t.Fatalf("response %s", w.Body)
	}
	a, b := result.Data.Messages[0], result.Data.Messages[1]
	if a.ID != "9007199254740996" || a.From != "9007199254740999" || a.Sender != 1 || a.Initiator != "0" || a.Created != "9007199254741001" || b.Sender != 2 || b.Initiator != "9007199254741003" || b.Created != "9007199254741005" || b.Content != "card" || len(b.Mentions) != 1 || b.Mentions[0] != "9007199254741007" {
		t.Fatalf("fields: %s", w.Body)
	}
}
func TestMessageContextHTTPErrorMappings(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{{codes.InvalidArgument, 400}, {codes.Unauthenticated, 401}, {codes.PermissionDenied, 403}, {codes.NotFound, 404}, {codes.Unavailable, 503}, {codes.DeadlineExceeded, 504}, {codes.Canceled, 502}, {codes.Internal, 502}} {
		client := messageContextClient{call: func(context.Context, *pb.GetTeamGroupMessageContextRequest) (*pb.GetTeamGroupMessageContextResponse, error) {
			return nil, status.Error(tc.code, "private detail")
		}}
		w := httptest.NewRecorder()
		getTeamGroupMessageContextHandler(client)(w, messageContextRequest("200", "300", "4", "", "Bearer token"))
		if w.Code != tc.want || strings.Contains(w.Body.String(), "private detail") || strings.Contains(w.Body.String(), "data") {
			t.Fatalf("error: %d %s", w.Code, w.Body)
		}
	}
}
func TestMessageContextHTTPRejectsInvalidResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*pb.GetTeamGroupMessageContextResponse)
	}{
		{"empty", func(r *pb.GetTeamGroupMessageContextResponse) { r.Messages = nil }}, {"nil message", func(r *pb.GetTeamGroupMessageContextResponse) { r.Messages[0] = nil }},
		{"target zero", func(r *pb.GetTeamGroupMessageContextResponse) { r.TargetMessageId = 0 }}, {"target wrong", func(r *pb.GetTeamGroupMessageContextResponse) { r.TargetMessageId = 5 }},
		{"id zero", func(r *pb.GetTeamGroupMessageContextResponse) { r.Messages[0].Id = 0 }}, {"missing target", func(r *pb.GetTeamGroupMessageContextResponse) { r.Messages[0].Id = 3 }},
		{"duplicate target", func(r *pb.GetTeamGroupMessageContextResponse) { r.Messages = append(r.Messages, r.Messages[0]) }}, {"unsorted", func(r *pb.GetTeamGroupMessageContextResponse) {
			r.Messages = append(r.Messages, &pb.TeamGroupMessage{Id: 3, MsgId: "a", FromId: 1})
		}},
		{"over41", func(r *pb.GetTeamGroupMessageContextResponse) {
			for i := int64(5); i <= 45; i++ {
				r.Messages = append(r.Messages, &pb.TeamGroupMessage{Id: i, MsgId: "a", FromId: 1})
			}
		}},
		{"from", func(r *pb.GetTeamGroupMessageContextResponse) { r.Messages[0].FromId = 0 }}, {"sender", func(r *pb.GetTeamGroupMessageContextResponse) { r.Messages[0].SenderType = 3 }}, {"sender negative", func(r *pb.GetTeamGroupMessageContextResponse) { r.Messages[0].SenderType = -1 }},
		{"bot initiator", func(r *pb.GetTeamGroupMessageContextResponse) { r.Messages[0].SenderType = 2 }}, {"user initiator", func(r *pb.GetTeamGroupMessageContextResponse) {
			r.Messages[0].SenderType = 1
			r.Messages[0].InitiatorId = 1
		}}, {"legacy initiator", func(r *pb.GetTeamGroupMessageContextResponse) { r.Messages[0].InitiatorId = 1 }},
		{"content type", func(r *pb.GetTeamGroupMessageContextResponse) { r.Messages[0].ContentType = -1 }}, {"time", func(r *pb.GetTeamGroupMessageContextResponse) { r.Messages[0].CreatedAtUnixMs = -1 }}, {"msg_id", func(r *pb.GetTeamGroupMessageContextResponse) { r.Messages[0].MsgId = "" }},
		{"mention zero", func(r *pb.GetTeamGroupMessageContextResponse) { r.Messages[0].MentionedUserIds = []int64{0} }}, {"mention duplicate", func(r *pb.GetTeamGroupMessageContextResponse) { r.Messages[0].MentionedUserIds = []int64{1, 1} }}, {"mention max", func(r *pb.GetTeamGroupMessageContextResponse) {
			r.Messages[0].MentionedUserIds = []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := validMessageContext()
			tc.change(r)
			client := messageContextClient{call: func(context.Context, *pb.GetTeamGroupMessageContextRequest) (*pb.GetTeamGroupMessageContextResponse, error) {
				return r, nil
			}}
			w := httptest.NewRecorder()
			getTeamGroupMessageContextHandler(client)(w, messageContextRequest("200", "300", "4", "", "Bearer token"))
			if w.Code != 502 || strings.Contains(w.Body.String(), "private content") || strings.Contains(w.Body.String(), "data") {
				t.Fatalf("shape: %d %s", w.Code, w.Body)
			}
		})
	}
	client := messageContextClient{call: func(context.Context, *pb.GetTeamGroupMessageContextRequest) (*pb.GetTeamGroupMessageContextResponse, error) {
		return nil, nil
	}}
	w := httptest.NewRecorder()
	getTeamGroupMessageContextHandler(client)(w, messageContextRequest("200", "300", "4", "", "Bearer token"))
	if w.Code != 502 {
		t.Fatalf("nil: %d", w.Code)
	}
}
func TestMessageContextHTTPRouteRegistered(t *testing.T) {
	b, e := os.ReadFile("main.go")
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(b), `Path:    "/api/v1/teams/:team_id/groups/:group_id/messages/:message_id/context"`) || !strings.Contains(string(b), "Handler: getTeamGroupMessageContextHandler(") {
		t.Fatal("context GET route missing")
	}
}

func TestMessageContextHTTPRealRouterPath(t *testing.T) {
	client := messageContextClient{call: func(_ context.Context, req *pb.GetTeamGroupMessageContextRequest) (*pb.GetTeamGroupMessageContextResponse, error) {
		if req.TeamId != 200 || req.GroupId != 300 || req.MessageId != 4 {
			t.Fatalf("routed request: %v", req)
		}
		return validMessageContext(), nil
	}}
	routes := router.NewRouter()
	if err := routes.Handle(http.MethodGet, "/api/v1/teams/:team_id/groups/:group_id/messages/:message_id/context", getTeamGroupMessageContextHandler(client)); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/teams/200/groups/300/messages/4/context", nil)
	r.Header.Set("Authorization", "Bearer token")
	w := httptest.NewRecorder()
	routes.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("route: %d %s", w.Code, w.Body)
	}
}
