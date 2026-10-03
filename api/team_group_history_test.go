package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/im/pb"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type teamGroupHistoryClient struct {
	pb.IMClient
	call func(context.Context, *pb.ListTeamGroupMessagesRequest) (*pb.ListTeamGroupMessagesResponse, error)
}

func (c teamGroupHistoryClient) ListTeamGroupMessages(ctx context.Context, req *pb.ListTeamGroupMessagesRequest, _ ...grpc.CallOption) (*pb.ListTeamGroupMessagesResponse, error) {
	return c.call(ctx, req)
}

func teamGroupHistoryRequest(teamID, groupID, query, token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/teams/"+teamID+"/groups/"+groupID+"/messages"+query, nil)
	r = pathvar.WithVars(r, map[string]string{"team_id": teamID, "group_id": groupID})
	if token != "" {
		r.Header.Set("Authorization", token)
	}
	return r
}

func TestTeamGroupHistoryHTTPRejectsInvalidRequestBeforeRPC(t *testing.T) {
	client := teamGroupHistoryClient{call: func(context.Context, *pb.ListTeamGroupMessagesRequest) (*pb.ListTeamGroupMessagesResponse, error) {
		t.Fatal("invalid request reached IM RPC")
		return nil, nil
	}}
	for _, tc := range []struct {
		team, group, query, token string
		want                      int
	}{
		{"200", "300", "", "", 401},
		{"200", "300", "", "Basic token", 401},
		{"0", "300", "", "Bearer token", 400},
		{"200", "bad", "", "Bearer token", 400},
		{"200", "300", "?before_message_id=-1", "Bearer token", 400},
		{"200", "300", "?limit=101", "Bearer token", 400},
		{"200", "300", "?limit=2&limit=3", "Bearer token", 400},
		{"200", "300", "?unknown=1", "Bearer token", 400},
	} {
		w := httptest.NewRecorder()
		listTeamGroupMessagesHandler(client)(w, teamGroupHistoryRequest(tc.team, tc.group, tc.query, tc.token))
		if w.Code != tc.want {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
}

func TestTeamGroupHistoryHTTPForwardsAndPreservesIDs(t *testing.T) {
	client := teamGroupHistoryClient{call: func(ctx context.Context, req *pb.ListTeamGroupMessagesRequest) (*pb.ListTeamGroupMessagesResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 9007199254740993 || req.GetGroupId() != 9007199254740995 || req.GetBeforeMessageId() != 9007199254740997 || req.GetLimit() != 2 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer test-token" {
			t.Fatalf("wrong RPC request: %v %v", req, md)
		}
		return &pb.ListTeamGroupMessagesResponse{Messages: []*pb.TeamGroupMessage{{Id: 9007199254740996, MsgId: "msg-1", FromId: 9007199254740998, ContentType: 1, Content: "hello", CreatedAtUnixMs: 1790683200000}}, NextBeforeMessageId: 9007199254740996}, nil
	}}
	w := httptest.NewRecorder()
	listTeamGroupMessagesHandler(client)(w, teamGroupHistoryRequest("9007199254740993", "9007199254740995", "?before_message_id=9007199254740997&limit=2", "Bearer test-token"))
	var result struct {
		Data struct {
			Messages []struct {
				ID     string `json:"id"`
				FromID string `json:"from_id"`
			} `json:"messages"`
			Next string `json:"next_before_message_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(result.Data.Messages) != 1 || result.Data.Messages[0].ID != "9007199254740996" || result.Data.Messages[0].FromID != "9007199254740998" || result.Data.Next != "9007199254740996" {
		t.Fatalf("response: %d %s", w.Code, w.Body.String())
	}
}

func TestTeamGroupHistoryHTTPMapsErrors(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{{codes.InvalidArgument, 400}, {codes.Unauthenticated, 401}, {codes.PermissionDenied, 403}, {codes.NotFound, 404}, {codes.Unavailable, 503}, {codes.DeadlineExceeded, 504}, {codes.Internal, 502}} {
		client := teamGroupHistoryClient{call: func(context.Context, *pb.ListTeamGroupMessagesRequest) (*pb.ListTeamGroupMessagesResponse, error) {
			return nil, status.Error(tc.code, "private detail")
		}}
		w := httptest.NewRecorder()
		listTeamGroupMessagesHandler(client)(w, teamGroupHistoryRequest("200", "300", "", "Bearer token"))
		if w.Code != tc.want || strings.Contains(w.Body.String(), "private detail") {
			t.Fatalf("RPC %v: %d %s", tc.code, w.Code, w.Body.String())
		}
	}
}

func TestTeamGroupHistoryHTTPPreservesBotAndLegacySenderMetadata(t *testing.T) {
	content, err := model.EncodeTaskCreatedCard(9007199254740993, "已确认标题")
	if err != nil {
		t.Fatal(err)
	}
	client := teamGroupHistoryClient{call: func(context.Context, *pb.ListTeamGroupMessagesRequest) (*pb.ListTeamGroupMessagesResponse, error) {
		return &pb.ListTeamGroupMessagesResponse{Messages: []*pb.TeamGroupMessage{
			{Id: 9, MsgId: "bot", FromId: 42, SenderType: 2, InitiatorId: 9007199254740995, ContentType: int32(model.MessageContentTaskCard), Content: content},
			{Id: 8, MsgId: "legacy", FromId: 42},
		}}, nil
	}}
	w := httptest.NewRecorder()
	listTeamGroupMessagesHandler(client)(w, teamGroupHistoryRequest("200", "300", "", "Bearer token"))
	var result struct {
		Data struct {
			Messages []struct {
				SenderType  int32  `json:"sender_type"`
				Initiator   string `json:"initiator_id"`
				From        string `json:"from_id"`
				ContentType int32  `json:"content_type"`
				Content     string `json:"content"`
			} `json:"messages"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || len(result.Data.Messages) != 2 {
		t.Fatalf("invalid history response: %s err=%v", w.Body, err)
	}
	bot, user := result.Data.Messages[0], result.Data.Messages[1]
	if bot.SenderType != 2 || bot.Initiator != "9007199254740995" || bot.From != "42" || user.SenderType != 1 || user.Initiator != "0" {
		t.Fatalf("sender metadata changed: %s", w.Body)
	}
	if bot.ContentType != int32(model.MessageContentTaskCard) || bot.Content != content {
		t.Fatalf("HTTP history changed task result: %s", w.Body)
	}
}
