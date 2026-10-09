package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	impb "github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type unreadIMClient struct {
	impb.IMClient
	list func(context.Context, *impb.ListMyUnreadConversationsRequest) (*impb.ListMyUnreadConversationsResponse, error)
}

func (c unreadIMClient) ListMyUnreadConversations(ctx context.Context, req *impb.ListMyUnreadConversationsRequest, _ ...grpc.CallOption) (*impb.ListMyUnreadConversationsResponse, error) {
	return c.list(ctx, req)
}

func TestUnreadOverviewForwardsBearerAndNamesOnlyProvenPeers(t *testing.T) {
	const snapshot = int64(9007199254740997)
	im := unreadIMClient{list: func(ctx context.Context, req *impb.ListMyUnreadConversationsRequest) (*impb.ListMyUnreadConversationsResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer test-token" {
			t.Fatalf("authorization: %v", got)
		}
		if req.SnapshotUpperMessageId != snapshot || req.BeforeLastMessageId != snapshot-1 || req.Limit != 2 || req.MentionsOnly {
			t.Fatalf("request: %v", req)
		}
		return &impb.ListMyUnreadConversationsResponse{SnapshotUpperMessageId: snapshot, NextBeforeLastMessageId: snapshot - 3,
			Conversations: []*impb.UnreadConversation{
				{ChatType: 2, TeamId: 42, GroupId: 43, GroupName: "团队讨论", LastMessageId: snapshot - 2, LastMessageTimeUnixMs: 1000, Preview: "群消息", UnreadCount: 7, MentionUnreadCount: 1},
				{ChatType: 1, PeerId: 9007199254740993, LastMessageId: snapshot - 3, LastMessageTimeUnixMs: 999, Preview: "私聊", UnreadCount: 9007199254740994},
			}}, nil
	}}
	users := f2UserClient{names: func(ctx context.Context, req *userpb.BatchGetConversationDisplayNamesRequest) (*userpb.BatchGetConversationDisplayNamesResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer test-token" {
			t.Fatalf("user authorization: %v", got)
		}
		if len(req.UserIds) != 1 || req.UserIds[0] != 9007199254740993 {
			t.Fatalf("unexpected name lookup: %v", req.UserIds)
		}
		return &userpb.BatchGetConversationDisplayNamesResponse{Users: []*userpb.ConversationDisplayName{{UserId: req.UserIds[0], DisplayName: "阿青"}}}, nil
	}}
	w := httptest.NewRecorder()
	listMyUnreadConversationsHandler(im, users)(w, f2Request("/api/v1/messages/unread-conversations?snapshot_upper_message_id=9007199254740997&before_last_message_id=9007199254740996&limit=2"))
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `"peer_id":"9007199254740993"`) ||
		!strings.Contains(body, `"display_name":"阿青"`) || !strings.Contains(body, `"unread_count":"9007199254740994"`) ||
		!strings.Contains(body, `"snapshot_upper_message_id":"9007199254740997"`) ||
		!strings.Contains(body, `"last_message_time_unix_ms":1000`) {
		t.Fatalf("response: %d %s", w.Code, body)
	}
}

func TestUnreadOverviewMentionsOnlyAndEmptyPage(t *testing.T) {
	im := unreadIMClient{list: func(_ context.Context, req *impb.ListMyUnreadConversationsRequest) (*impb.ListMyUnreadConversationsResponse, error) {
		if !req.MentionsOnly || req.Limit != 0 {
			t.Fatalf("request: %v", req)
		}
		return &impb.ListMyUnreadConversationsResponse{}, nil
	}}
	w := httptest.NewRecorder()
	listMyUnreadConversationsHandler(im, nil)(w, f2Request("/api/v1/messages/unread-conversations?mentions_only=1"))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"conversations":[]`) {
		t.Fatalf("response: %d %s", w.Code, w.Body.String())
	}
}

func TestUnreadOverviewRejectsBadInputBeforeRPC(t *testing.T) {
	im := unreadIMClient{list: func(context.Context, *impb.ListMyUnreadConversationsRequest) (*impb.ListMyUnreadConversationsResponse, error) {
		t.Fatal("invalid query reached IM")
		return nil, nil
	}}
	for _, path := range []string{
		"?user_id=4", "?limit=0", "?limit=51", "?limit=-1", "?limit=2&limit=3",
		"?mentions_only=true", "?mentions_only=2", "?snapshot_upper_message_id=4",
		"?before_last_message_id=4", "?snapshot_upper_message_id=4&before_last_message_id=5",
		"?snapshot_upper_message_id=9223372036854775808&before_last_message_id=1",
	} {
		w := httptest.NewRecorder()
		listMyUnreadConversationsHandler(im, nil)(w, f2Request("/api/v1/messages/unread-conversations"+path))
		if w.Code != 400 || strings.Contains(w.Body.String(), `"conversations"`) {
			t.Fatalf("query %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	r := f2Request("/api/v1/messages/unread-conversations")
	r.Header.Del("Authorization")
	listMyUnreadConversationsHandler(im, nil)(w, r)
	if w.Code != 401 {
		t.Fatalf("missing auth: %d %s", w.Code, w.Body.String())
	}
}

func TestUnreadOverviewDoesNotMaskIMOrUserFailure(t *testing.T) {
	im := unreadIMClient{list: func(context.Context, *impb.ListMyUnreadConversationsRequest) (*impb.ListMyUnreadConversationsResponse, error) {
		return nil, status.Error(codes.PermissionDenied, "private IM detail")
	}}
	w := httptest.NewRecorder()
	listMyUnreadConversationsHandler(im, nil)(w, f2Request("/api/v1/messages/unread-conversations"))
	if w.Code != 403 || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), `"conversations"`) {
		t.Fatalf("IM error: %d %s", w.Code, w.Body.String())
	}
	im.list = func(context.Context, *impb.ListMyUnreadConversationsRequest) (*impb.ListMyUnreadConversationsResponse, error) {
		return &impb.ListMyUnreadConversationsResponse{SnapshotUpperMessageId: 10, Conversations: []*impb.UnreadConversation{{ChatType: 1, PeerId: 7, LastMessageId: 9, UnreadCount: 1}}}, nil
	}
	users := f2UserClient{names: func(context.Context, *userpb.BatchGetConversationDisplayNamesRequest) (*userpb.BatchGetConversationDisplayNamesResponse, error) {
		return nil, status.Error(codes.Unavailable, "private User detail")
	}}
	w = httptest.NewRecorder()
	listMyUnreadConversationsHandler(im, users)(w, f2Request("/api/v1/messages/unread-conversations"))
	if w.Code != 503 || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), `"conversations"`) {
		t.Fatalf("User error: %d %s", w.Code, w.Body.String())
	}
}

func TestUnreadOverviewRejectsInvalidIMRowsBeforeNameLookup(t *testing.T) {
	users := f2UserClient{names: func(context.Context, *userpb.BatchGetConversationDisplayNamesRequest) (*userpb.BatchGetConversationDisplayNamesResponse, error) {
		t.Fatal("invalid IM page reached User")
		return nil, nil
	}}
	for _, rows := range [][]*impb.UnreadConversation{
		{{ChatType: 1, PeerId: 7, LastMessageId: 10, UnreadCount: 1}, {ChatType: 2, TeamId: 2, GroupId: 3, GroupName: "群", LastMessageId: 10, UnreadCount: 1}},
		{{ChatType: 2, TeamId: 2, GroupId: 3, GroupName: "群", LastMessageId: 9, UnreadCount: 1, MentionUnreadCount: 2}},
		{{ChatType: 1, PeerId: 7, GroupId: 2, LastMessageId: 9, UnreadCount: 1}},
		{{ChatType: 3, PeerId: 7, LastMessageId: 9, UnreadCount: 1}},
	} {
		im := unreadIMClient{list: func(context.Context, *impb.ListMyUnreadConversationsRequest) (*impb.ListMyUnreadConversationsResponse, error) {
			return &impb.ListMyUnreadConversationsResponse{SnapshotUpperMessageId: 10, Conversations: rows}, nil
		}}
		w := httptest.NewRecorder()
		listMyUnreadConversationsHandler(im, users)(w, f2Request("/api/v1/messages/unread-conversations"))
		if w.Code != 502 || strings.Contains(w.Body.String(), `"conversations"`) {
			t.Fatalf("invalid rows: %d %s", w.Code, w.Body.String())
		}
	}
}
