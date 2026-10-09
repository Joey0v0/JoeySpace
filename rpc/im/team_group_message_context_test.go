package main

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const contextSelect = "SELECT id, msg_id, from_id, sender_type, initiator_id, content_type, content, created_at FROM `messages` WHERE (to_id = ? AND chat_type = ?) AND id "

func contextRows(ids ...int64) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"id", "msg_id", "from_id", "sender_type", "initiator_id", "content_type", "content", "created_at"})
	for _, id := range ids {
		rows.AddRow(id, fmt.Sprintf("msg-%d", id), int64(9007199254740997), 0, 0, 1, "content", time.Date(2026, 10, 9, 1, 2, 3, 456000000, time.FixedZone("east", 28800)))
	}
	return rows
}
func contextQuery(mock sqlmock.Sqlmock, target int64, part string) *sqlmock.ExpectedQuery {
	suffix, limit := "= ? LIMIT ?", 1
	if part == "earlier" {
		suffix, limit = "< ? ORDER BY id DESC LIMIT ?", 20
	}
	if part == "later" {
		suffix, limit = "> ? ORDER BY id ASC LIMIT ?", 20
	}
	return mock.ExpectQuery("^"+regexp.QuoteMeta(contextSelect+suffix)+"$").WithArgs(int64(300), 2, target, limit)
}
func contextMentions(mock sqlmock.Sqlmock, ids []int64) *sqlmock.ExpectedQuery {
	args := make([]driver.Value, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return mock.ExpectQuery("SELECT message_id, mentioned_user_id FROM `im_group_message_mentions`").WithArgs(args...)
}
func contextServer(t *testing.T) (*imServer, sqlmock.Sqlmock) {
	s, m := testIMServer(t)
	s.teamClient = teamCheckFunc(func(ctx context.Context, req *userpb.CheckTeamMemberRequest) error {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 200 || len(md.Get("authorization")) != 1 || !strings.HasPrefix(md.Get("authorization")[0], "Bearer ") {
			t.Fatalf("authorization lost: %v %v", req, md)
		}
		return nil
	})
	return s, m
}
func contextRequest(id int64) *pb.GetTeamGroupMessageContextRequest {
	return &pb.GetTeamGroupMessageContextRequest{TeamId: 200, GroupId: 300, MessageId: id}
}
func TestMessageContextInvalidConfigAndAuth(t *testing.T) {
	s, _ := contextServer(t)
	for _, req := range []*pb.GetTeamGroupMessageContextRequest{nil, {}, {TeamId: -1, GroupId: 300, MessageId: 4}, {TeamId: 200, MessageId: 4}, contextRequest(0), contextRequest(-1)} {
		r, e := s.GetTeamGroupMessageContext(historyContext(t), req)
		if r != nil || status.Code(e) != codes.InvalidArgument {
			t.Fatalf("invalid: %v %v", r, e)
		}
	}
	for _, ctx := range []context.Context{context.Background(), metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Basic bad")), metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer bad")), metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer a", "authorization", "Bearer b"))} {
		r, e := s.GetTeamGroupMessageContext(ctx, contextRequest(4))
		if r != nil || status.Code(e) != codes.Unauthenticated {
			t.Fatalf("auth: %v %v", r, e)
		}
	}
	for _, change := range []func(*imServer){func(s *imServer) { s.db = nil }, func(s *imServer) { s.jwtSecret = "" }, func(s *imServer) { s.teamClient = nil }} {
		s, _ := contextServer(t)
		change(s)
		r, e := s.GetTeamGroupMessageContext(historyContext(t), contextRequest(4))
		if r != nil || status.Code(e) != codes.Unavailable {
			t.Fatalf("config: %v %v", r, e)
		}
	}
}
func TestMessageContextWindow(t *testing.T) {
	for _, n := range []int{0, 2, 20} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s, m := contextServer(t)
			target := int64(9007199254741100)
			unreadAccess(m, 42)
			contextQuery(m, target, "target").WillReturnRows(sqlmock.NewRows([]string{"id", "msg_id", "from_id", "sender_type", "initiator_id", "content_type", "content", "created_at"}).AddRow(target, "bot", int64(99), 2, int64(9007199254740993), 100, "card", time.UnixMilli(1791500000000)))
			earlier, later, ids := []int64{}, []int64{}, []int64{}
			for i := 1; i <= n; i++ {
				earlier = append(earlier, target-int64(i))
				later = append(later, target+int64(i))
			}
			for i := n; i >= 1; i-- {
				ids = append(ids, target-int64(i))
			}
			ids = append(ids, target)
			ids = append(ids, later...)
			contextQuery(m, target, "earlier").WillReturnRows(contextRows(earlier...))
			contextQuery(m, target, "later").WillReturnRows(contextRows(later...))
			contextMentions(m, ids).WillReturnRows(sqlmock.NewRows([]string{"message_id", "mentioned_user_id"}).AddRow(target, int64(9007199254740995)))
			unreadAccess(m, 42)
			var r *pb.GetTeamGroupMessageContextResponse
			var e error
			if n == 2 {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				server := grpc.NewServer()
				pb.RegisterIMServer(server, s)
				go server.Serve(listener)
				defer server.Stop()
				conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+validIMToken(t)))
				r, e = pb.NewIMClient(conn).GetTeamGroupMessageContext(ctx, contextRequest(target))
			} else {
				r, e = s.GetTeamGroupMessageContext(historyContext(t), contextRequest(target))
			}
			if e != nil || r.GetTargetMessageId() != target || len(r.GetMessages()) != 2*n+1 {
				t.Fatalf("window: %v %v", r, e)
			}
			for i, msg := range r.Messages {
				if msg.Id != ids[i] || msg.MsgId == "" || msg.FromId <= 0 {
					t.Fatalf("message %d: %v", i, msg)
				}
			}
			bot := r.Messages[n]
			if bot.SenderType != 2 || bot.InitiatorId != 9007199254740993 || bot.ContentType != 100 || bot.Content != "card" || bot.CreatedAtUnixMs != 1791500000000 || len(bot.MentionedUserIds) != 1 || bot.MentionedUserIds[0] != 9007199254740995 {
				t.Fatalf("bot: %v", bot)
			}
			if n > 0 && (r.Messages[0].SenderType != 1 || r.Messages[0].InitiatorId != 0 || r.Messages[0].CreatedAtUnixMs != time.Date(2026, 10, 9, 1, 2, 3, 456000000, time.FixedZone("east", 28800)).UnixMilli()) {
				t.Fatalf("legacy: %v", r.Messages[0])
			}
		})
	}
}
func TestMessageContextEarlyFailures(t *testing.T) {
	for _, stage := range []string{"group", "team", "wrong group", "target missing", "target", "earlier", "later", "mentions"} {
		t.Run(stage, func(t *testing.T) {
			s, m := contextServer(t)
			want := codes.Unavailable
			switch stage {
			case "group":
				m.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).WillReturnRows(sqlmock.NewRows([]string{"team_id"}))
				want = codes.PermissionDenied
			case "team":
				s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error {
					return status.Error(codes.PermissionDenied, "revoked")
				})
				m.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(200))
				want = codes.PermissionDenied
			case "wrong group":
				s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error { return nil })
				m.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(201))
				expectTeamGroupReadFence(m, 300, 42, 201, nil, true)
				m.ExpectQuery(regexp.QuoteMeta(historyGroupQuery)).WithArgs(int64(300), 1).WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(201))
				want = codes.NotFound
			default:
				unreadAccess(m, 42)
				if stage == "target missing" {
					contextQuery(m, 4, "target").WillReturnRows(contextRows())
					want = codes.NotFound
					break
				}
				if stage == "target" {
					contextQuery(m, 4, "target").WillReturnError(errors.New("private detail"))
					break
				}
				contextQuery(m, 4, "target").WillReturnRows(contextRows(4))
				if stage == "earlier" {
					contextQuery(m, 4, "earlier").WillReturnError(errors.New("private detail"))
					break
				}
				contextQuery(m, 4, "earlier").WillReturnRows(contextRows())
				if stage == "later" {
					contextQuery(m, 4, "later").WillReturnError(errors.New("private detail"))
					break
				}
				contextQuery(m, 4, "later").WillReturnRows(contextRows())
				contextMentions(m, []int64{4}).WillReturnError(errors.New("private detail"))
			}
			r, e := s.GetTeamGroupMessageContext(historyContext(t), contextRequest(4))
			if r != nil || status.Code(e) != want || strings.Contains(status.Convert(e).Message(), "private detail") {
				t.Fatalf("failure: %v %v", r, e)
			}
		})
	}
}
func TestMessageContextFinalRecheckDiscardsContent(t *testing.T) {
	for _, stage := range []string{"group", "team", "user failure", "generation", "fence", "ownership"} {
		t.Run(stage, func(t *testing.T) {
			s, m := contextServer(t)
			calls := 0
			s.teamClient = readMembershipResponseFunc{check: func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
				calls++
				if calls == 2 {
					switch stage {
					case "team":
						return nil, status.Error(codes.PermissionDenied, "left")
					case "user failure":
						return nil, status.Error(codes.Unavailable, "private detail")
					case "generation":
						return &userpb.CheckTeamMemberResponse{UserId: 42, Generation: 0}, nil
					}
				}
				return &userpb.CheckTeamMemberResponse{UserId: 42, Generation: 1}, nil
			}}
			unreadAccess(m, 42)
			contextQuery(m, 4, "target").WillReturnRows(contextRows(4))
			contextQuery(m, 4, "earlier").WillReturnRows(contextRows())
			contextQuery(m, 4, "later").WillReturnRows(contextRows())
			contextMentions(m, []int64{4}).WillReturnRows(sqlmock.NewRows([]string{"message_id", "mentioned_user_id"}))
			rows := sqlmock.NewRows([]string{"team_id"})
			if stage != "group" {
				rows.AddRow(200)
			}
			m.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).WillReturnRows(rows)
			want := codes.PermissionDenied
			if stage == "user failure" || stage == "generation" {
				want = codes.Unavailable
			}
			if stage == "fence" {
				expectTeamGroupReadFence(m, 300, 42, 200, int64(1), true)
			}
			if stage == "ownership" {
				expectTeamGroupReadFence(m, 300, 42, 200, nil, true)
				m.ExpectQuery(regexp.QuoteMeta(historyGroupQuery)).WithArgs(int64(300), 1).WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(201))
				want = codes.NotFound
			}
			r, e := s.GetTeamGroupMessageContext(historyContext(t), contextRequest(4))
			if r != nil || status.Code(e) != want {
				t.Fatalf("final: %v %v", r, e)
			}
		})
	}
}
func TestMessageContextCanceledDatabase(t *testing.T) {
	s, _ := contextServer(t)
	ctx, cancel := context.WithCancel(historyContext(t))
	cancel()
	r, e := s.GetTeamGroupMessageContext(ctx, contextRequest(4))
	if r != nil || status.Code(e) != codes.Canceled {
		t.Fatalf("canceled: %v %v", r, e)
	}
}

func TestMessageContextActiveGenerationChangeDiscardsContent(t *testing.T) {
	s, m := contextServer(t)
	calls := int64(0)
	s.teamClient = readMembershipResponseFunc{check: func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
		calls++
		return &userpb.CheckTeamMemberResponse{UserId: 42, Generation: calls}, nil
	}}
	unreadAccess(m, 42)
	contextQuery(m, 4, "target").WillReturnRows(contextRows(4))
	contextQuery(m, 4, "earlier").WillReturnRows(contextRows())
	contextQuery(m, 4, "later").WillReturnRows(contextRows())
	contextMentions(m, []int64{4}).WillReturnRows(sqlmock.NewRows([]string{"message_id", "mentioned_user_id"}))
	unreadAccess(m, 42)
	r, e := s.GetTeamGroupMessageContext(historyContext(t), contextRequest(4))
	if r != nil || status.Code(e) != codes.PermissionDenied {
		t.Fatalf("generation change leaked: %v %v", r, e)
	}
}

func TestMessageContextNormalizesUserAuthorization(t *testing.T) {
	s, m := contextServer(t)
	token := validIMToken(t)
	s.teamClient = teamCheckFunc(func(ctx context.Context, _ *userpb.CheckTeamMemberRequest) error {
		md, _ := metadata.FromOutgoingContext(ctx)
		if len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer "+token {
			t.Fatalf("not normalized: %v", md)
		}
		return status.Error(codes.PermissionDenied, "stop before content")
	})
	m.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(200))
	r, e := s.GetTeamGroupMessageContext(metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "bEaReR   "+token)), contextRequest(4))
	if r != nil || status.Code(e) != codes.PermissionDenied {
		t.Fatalf("authorization: %v %v", r, e)
	}
}
func TestMessageContextExpiredDeadline(t *testing.T) {
	s, _ := contextServer(t)
	ctx, cancel := context.WithDeadline(historyContext(t), time.Now().Add(-time.Second))
	defer cancel()
	r, e := s.GetTeamGroupMessageContext(ctx, contextRequest(4))
	if r != nil || status.Code(e) != codes.DeadlineExceeded {
		t.Fatalf("deadline: %v %v", r, e)
	}
}
