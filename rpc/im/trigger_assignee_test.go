package main

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type triggerAssigneeTestTeams struct {
	check   func(context.Context, int64, int64) error
	resolve func(context.Context, int64, int64, string) (*userpb.ResolveTriggerTeamMemberResponse, error)
}

func (s *triggerAssigneeTestTeams) Check(ctx context.Context, actor, team int64) error {
	if s.check != nil {
		return s.check(ctx, actor, team)
	}
	return nil
}

func (s *triggerAssigneeTestTeams) Resolve(ctx context.Context, actor, team int64, name string) (*userpb.ResolveTriggerTeamMemberResponse, error) {
	return s.resolve(ctx, actor, team, name)
}

func triggerAssigneeTestSource() (model.AgentTriggerOutbox, model.Message) {
	r, source := triggerContextFixture()
	r.Instruction = "请张三整理上线清单"
	source.Content = "@AI 整理任务 " + r.Instruction
	return r, source
}

func triggerAssigneeTestResponse(actor, team int64, name string, count int) *userpb.ResolveTriggerTeamMemberResponse {
	r := &userpb.ResolveTriggerTeamMemberResponse{ActorId: actor, TeamId: team, Name: name}
	for i := 0; i < count; i++ {
		r.Candidates = append(r.Candidates, &userpb.TriggerTeamMemberCandidate{UserId: 9007199254741000 + int64(i), Username: name})
	}
	return r
}

func expectTriggerAssigneeContext(mock sqlmock.Sqlmock, r model.AgentTriggerOutbox, source model.Message, messages ...model.Message) {
	expectTriggerContextSource(mock, r, source)
	expectTriggerContextMembership(mock, r)
	if len(messages) == 0 {
		messages = []model.Message{source}
	}
	expectTriggerContextHistory(mock, r, triggerContextMessageRows(messages...))
}

func TestTriggerAssigneeUsesStoredScopeAndReturnsOnlyBoundedCandidates(t *testing.T) {
	for _, count := range []int{0, 1, 2, 20} {
		im, mock := testIMServer(t)
		r, source := triggerAssigneeTestSource()
		expectTriggerAssigneeContext(mock, r, source)
		expectTriggerContextMembership(mock, r) // Final fence after User lookup.
		var checks, resolves int
		teams := &triggerAssigneeTestTeams{check: func(_ context.Context, actor, team int64) error {
			checks++
			if actor != r.ActorID || team != r.TeamID {
				t.Fatal("Check used untrusted scope")
			}
			return nil
		}, resolve: func(ctx context.Context, actor, team int64, name string) (*userpb.ResolveTriggerTeamMemberResponse, error) {
			resolves++
			if actor != r.ActorID || team != r.TeamID || name != "张三" {
				t.Fatal("Resolve used untrusted scope/name")
			}
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > triggerContextTimeout {
				t.Fatal("missing total five-second budget")
			}
			result := triggerAssigneeTestResponse(actor, team, name, count)
			result.Truncated = count == 20
			return result, nil
		}}
		s := &triggerContextServer{db: im.db, teams: teams, agentDNSName: "agent.go-im.internal"}
		ctx := metadata.NewIncomingContext(triggerContextAgent(context.Background()), metadata.Pairs("actor_id", "1", "team_id", "2", "group_id", "3", "name", "另一个人", "authorization", "Bearer fake"))
		response, err := s.ResolveTaskTriggerMember(ctx, &pb.ResolveTaskTriggerMemberRequest{MessageId: r.MessageID, Name: "张三"})
		if err != nil || response.GetMessageId() != r.MessageID || response.GetActorId() != r.ActorID || response.GetTeamId() != r.TeamID ||
			response.GetGroupId() != r.GroupID || response.GetRequestKey() != r.RequestKey() || response.GetName() != "张三" || len(response.Candidates) != count ||
			response.Truncated != (count == 20) || checks != 1 || resolves != 1 || proto.Size(response) > model.AgentTriggerMemberResponseLimit {
			t.Fatalf("response=%v err=%v checks=%d resolves=%d", response, err, checks, resolves)
		}
	}
}

func TestTriggerAssigneeRejectsServiceInputAndMissingCapabilitiesBeforeSQL(t *testing.T) {
	for _, name := range []string{"plain", "forged metadata", "wrong TLS role", "nil context", "nil server", "nil request", "bad ID", "blank name", "untrimmed name", "invalid UTF8", "long name", "missing database", "legacy Check", "typed nil resolver", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			im, _ := testIMServer(t)
			teams := &triggerAssigneeTestTeams{check: func(context.Context, int64, int64) error { t.Fatal("invalid request called Check"); return nil },
				resolve: func(context.Context, int64, int64, string) (*userpb.ResolveTriggerTeamMemberResponse, error) {
					t.Fatal("invalid request called Resolve")
					return nil, nil
				}}
			s := &triggerContextServer{db: im.db, teams: teams, agentDNSName: "agent.go-im.internal"}
			ctx := triggerContextAgent(context.Background())
			req := &pb.ResolveTaskTriggerMemberRequest{MessageId: 42, Name: "张三"}
			want := codes.InvalidArgument
			switch name {
			case "plain", "forged metadata":
				ctx = metadata.NewIncomingContext(context.Background(), metadata.Pairs("service", "agent.go-im.internal", "authorization", "Bearer fake"))
				want = codes.Unauthenticated
			case "nil context":
				ctx = nil
				want = codes.Unauthenticated
			case "wrong TLS role":
				s.agentDNSName = "task.go-im.internal"
				want = codes.Unauthenticated
			case "nil server":
				s = nil
				want = codes.Unauthenticated
			case "nil request":
				req = nil
			case "bad ID":
				req.MessageId = 0
			case "blank name":
				req.Name = ""
			case "untrimmed name":
				req.Name = " 张三 "
			case "invalid UTF8":
				req.Name = "\xff"
			case "long name":
				req.Name = strings.Repeat("张", 65)
			case "missing database":
				s.db = nil
				want = codes.Unavailable
			case "legacy Check":
				s.teams = triggerContextTeamFunc(func(context.Context, int64, int64) error { t.Fatal("legacy capability used"); return nil })
				want = codes.Unavailable
			case "typed nil resolver":
				s.teams = (*triggerAssigneeTestTeams)(nil)
				want = codes.Unavailable
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
				req = nil
				want = codes.Canceled
			}
			response, err := s.ResolveTaskTriggerMember(ctx, req)
			if response != nil || status.Code(err) != want {
				t.Fatalf("response=%v err=%v want=%s", response, err, want)
			}
		})
	}
}

func TestTriggerAssigneeRequiresNameInAuthorizedTextNotCardsOrImages(t *testing.T) {
	for _, contentType := range []int8{1, 2, 3, 4} {
		im, mock := testIMServer(t)
		r, source := triggerContextFixture() // No 张三 in instruction/source.
		earlier := source
		earlier.ID--
		earlier.MsgID = "earlier-assignee"
		earlier.Content = "交给张三处理"
		earlier.ContentType = contentType
		expectTriggerAssigneeContext(mock, r, source, source, earlier)
		calls := 0
		teams := &triggerAssigneeTestTeams{resolve: func(_ context.Context, actor, team int64, name string) (*userpb.ResolveTriggerTeamMemberResponse, error) {
			calls++
			return triggerAssigneeTestResponse(actor, team, name, 1), nil
		}}
		s := &triggerContextServer{db: im.db, teams: teams, agentDNSName: "agent.go-im.internal"}
		if contentType == 1 {
			expectTriggerContextMembership(mock, r)
		}
		response, err := s.ResolveTaskTriggerMember(triggerContextAgent(context.Background()), &pb.ResolveTaskTriggerMemberRequest{MessageId: r.MessageID, Name: "张三"})
		if contentType == 1 {
			if err != nil || response == nil || calls != 1 {
				t.Fatalf("text evidence: %v %v calls=%d", response, err, calls)
			}
		} else if response != nil || status.Code(err) != codes.InvalidArgument || calls != 0 {
			t.Fatalf("non-text evidence: %v %v calls=%d", response, err, calls)
		}
	}
}

func TestTriggerAssigneeMissingSourceOrCurrentPermissionNeverResolves(t *testing.T) {
	for _, stage := range []string{"outbox", "source", "group", "team", "history"} {
		t.Run(stage, func(t *testing.T) {
			im, mock := testIMServer(t)
			r, source := triggerAssigneeTestSource()
			want := codes.PermissionDenied
			teams := &triggerAssigneeTestTeams{resolve: func(context.Context, int64, int64, string) (*userpb.ResolveTriggerTeamMemberResponse, error) {
				t.Fatal("unauthorized source reached lookup")
				return nil, nil
			}}
			if stage == "outbox" {
				mock.ExpectQuery(regexp.QuoteMeta(triggerOutboxQuery)).WithArgs(r.MessageID).WillReturnRows(triggerContextOutboxRows())
				want = codes.NotFound
			} else {
				mock.ExpectQuery(regexp.QuoteMeta(triggerOutboxQuery)).WithArgs(r.MessageID).WillReturnRows(triggerContextOutboxRows(r))
				if stage == "source" {
					mock.ExpectQuery(regexp.QuoteMeta(triggerSourceQuery)).WithArgs(r.MessageID).WillReturnRows(triggerContextMessageRows())
					want = codes.NotFound
				} else {
					mock.ExpectQuery(regexp.QuoteMeta(triggerSourceQuery)).WithArgs(r.MessageID).WillReturnRows(triggerContextMessageRows(source))
					if stage == "group" {
						mock.ExpectQuery(regexp.QuoteMeta(triggerMembershipQuery)).WithArgs(r.GroupID, r.TeamID, r.ActorID).WillReturnRows(sqlmock.NewRows([]string{"id", "team_id", "user_id"}))
					} else {
						expectTriggerContextMembership(mock, r)
						if stage == "team" {
							teams.check = func(context.Context, int64, int64) error {
								return status.Error(codes.PermissionDenied, "private departure")
							}
						} else {
							expectTriggerContextHistory(mock, r, triggerContextMessageRows())
						}
					}
				}
			}
			s := &triggerContextServer{db: im.db, teams: teams, agentDNSName: "agent.go-im.internal"}
			response, err := s.ResolveTaskTriggerMember(triggerContextAgent(context.Background()), &pb.ResolveTaskTriggerMemberRequest{MessageId: r.MessageID, Name: "张三"})
			if response != nil || status.Code(err) != want || strings.Contains(err.Error(), "private") {
				t.Fatalf("response=%v err=%v", response, err)
			}
		})
	}
}

func TestTriggerAssigneeRejectsGroupChangeAfterMemberLookup(t *testing.T) {
	im, mock := testIMServer(t)
	r, source := triggerAssigneeTestSource()
	expectTriggerAssigneeContext(mock, r, source)
	mock.ExpectQuery(regexp.QuoteMeta(triggerMembershipQuery)).WithArgs(r.GroupID, r.TeamID, r.ActorID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "team_id", "user_id"}))
	s := &triggerContextServer{db: im.db, agentDNSName: "agent.go-im.internal", teams: &triggerAssigneeTestTeams{
		resolve: func(_ context.Context, actor, team int64, name string) (*userpb.ResolveTriggerTeamMemberResponse, error) {
			return triggerAssigneeTestResponse(actor, team, name, 1), nil
		},
	}}
	response, err := s.ResolveTaskTriggerMember(triggerContextAgent(context.Background()), &pb.ResolveTaskTriggerMemberRequest{MessageId: r.MessageID, Name: "张三"})
	if response != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("stale group disclosed candidates: %v %v", response, err)
	}
}

func TestTriggerAssigneeRejectsInvalidUserSuccessAndSafeErrors(t *testing.T) {
	for _, code := range []codes.Code{codes.PermissionDenied, codes.Unauthenticated, codes.InvalidArgument, codes.Canceled, codes.DeadlineExceeded, codes.Unimplemented, codes.Internal} {
		im, mock := testIMServer(t)
		r, source := triggerAssigneeTestSource()
		expectTriggerAssigneeContext(mock, r, source)
		calls := 0
		s := &triggerContextServer{db: im.db, agentDNSName: "agent.go-im.internal", teams: &triggerAssigneeTestTeams{
			resolve: func(context.Context, int64, int64, string) (*userpb.ResolveTriggerTeamMemberResponse, error) {
				calls++
				return nil, status.Error(code, "private DB/name/password")
			},
		}}
		response, err := s.ResolveTaskTriggerMember(triggerContextAgent(context.Background()), &pb.ResolveTaskTriggerMemberRequest{MessageId: r.MessageID, Name: "张三"})
		want := code
		if code == codes.Unimplemented || code == codes.Internal {
			want = codes.Unavailable
		}
		if response != nil || status.Code(err) != want || calls != 1 || strings.Contains(err.Error(), "private") {
			t.Fatalf("code=%s response=%v err=%v calls=%d", code, response, err, calls)
		}
	}
	for _, mutate := range []func(*userpb.ResolveTriggerTeamMemberResponse){
		func(r *userpb.ResolveTriggerTeamMemberResponse) { r.ActorId++ }, func(r *userpb.ResolveTriggerTeamMemberResponse) { r.TeamId++ },
		func(r *userpb.ResolveTriggerTeamMemberResponse) { r.Name = "李四" }, func(r *userpb.ResolveTriggerTeamMemberResponse) { r.Truncated = true },
		func(r *userpb.ResolveTriggerTeamMemberResponse) { r.Candidates = append(r.Candidates, r.Candidates[0]) },
		func(r *userpb.ResolveTriggerTeamMemberResponse) { r.Candidates[0].Username = "李四" },
	} {
		im, mock := testIMServer(t)
		r, source := triggerAssigneeTestSource()
		expectTriggerAssigneeContext(mock, r, source)
		s := &triggerContextServer{db: im.db, agentDNSName: "agent.go-im.internal", teams: &triggerAssigneeTestTeams{
			resolve: func(_ context.Context, actor, team int64, name string) (*userpb.ResolveTriggerTeamMemberResponse, error) {
				result := triggerAssigneeTestResponse(actor, team, name, 1)
				mutate(result)
				return result, nil
			},
		}}
		response, err := s.ResolveTaskTriggerMember(triggerContextAgent(context.Background()), &pb.ResolveTaskTriggerMemberRequest{MessageId: r.MessageID, Name: "张三"})
		if response != nil || status.Code(err) != codes.Unavailable {
			t.Fatalf("bad User success: %v %v", response, err)
		}
	}
}

func TestTriggerAssigneeCancellationAfterLookupWinsOverInvalidResult(t *testing.T) {
	im, mock := testIMServer(t)
	r, source := triggerAssigneeTestSource()
	expectTriggerAssigneeContext(mock, r, source)
	ctx, cancel := context.WithCancel(triggerContextAgent(context.Background()))
	defer cancel()
	s := &triggerContextServer{db: im.db, agentDNSName: "agent.go-im.internal", teams: &triggerAssigneeTestTeams{
		resolve: func(context.Context, int64, int64, string) (*userpb.ResolveTriggerTeamMemberResponse, error) {
			cancel()
			return nil, errors.New("private error")
		},
	}}
	response, err := s.ResolveTaskTriggerMember(ctx, &pb.ResolveTaskTriggerMemberRequest{MessageId: r.MessageID, Name: "张三"})
	if response != nil || status.Code(err) != codes.Canceled {
		t.Fatalf("cancel lost to lookup error: %v %v", response, err)
	}
}
