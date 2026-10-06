package main

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type triggerContextGenerationFunc func(context.Context, int64, int64) (int64, error)

func (f triggerContextGenerationFunc) Check(ctx context.Context, actor, team int64) error {
	_, err := f(ctx, actor, team)
	return err
}

func (f triggerContextGenerationFunc) CheckGeneration(ctx context.Context, actor, team int64) (int64, error) {
	return f(ctx, actor, team)
}

// SQL/User are contract substitutes. These sequential snapshots do not prove
// actual MySQL lock contention or atomic revocation across services.
func TestTriggerContextGenerationChecksBeforeHistoryAndBeforeReturningContent(t *testing.T) {
	for _, name := range []string{
		"legacy zero", "negative generation", "initial closed", "initial missing member", "initial storage failure",
		"generation advanced", "generation decreased", "final zero", "final revoked", "final unavailable", "final canceled",
		"final deadline", "final closed", "final missing member", "final storage failure", "large exact generation",
	} {
		t.Run(name, func(t *testing.T) {
			im, mock := testIMServer(t)
			r, source := triggerContextFixture()
			expectTriggerContextSource(mock, r, source)
			expectTriggerContextMembership(mock, r)
			initialGeneration, finalGeneration := int64(1), int64(1)
			want := codes.PermissionDenied
			wantCalls := 2
			var finalErr error
			switch name {
			case "legacy zero", "negative generation":
				initialGeneration = 0
				if name == "negative generation" {
					initialGeneration = -1
				}
				want, wantCalls = codes.Unavailable, 1
			case "initial closed", "initial missing member":
				wantCalls = 1
			case "initial storage failure":
				want, wantCalls = codes.Unavailable, 1
			case "generation advanced":
				finalGeneration = 2
			case "generation decreased":
				initialGeneration = 2
			case "final zero":
				finalGeneration, want = 0, codes.Unavailable
			case "final revoked":
				finalErr = status.Error(codes.PermissionDenied, "private upstream content")
			case "final unavailable":
				finalErr, want = status.Error(codes.Internal, "private upstream content"), codes.Unavailable
			case "final canceled":
				finalErr, want = status.Error(codes.Canceled, "private upstream content"), codes.Canceled
			case "final deadline":
				finalErr, want = status.Error(codes.DeadlineExceeded, "private upstream content"), codes.DeadlineExceeded
			case "final storage failure":
				want = codes.Unavailable
			case "large exact generation":
				initialGeneration, finalGeneration, want = math.MaxInt64, math.MaxInt64, codes.OK
			}
			if initialGeneration > 0 {
				switch name {
				case "initial closed":
					expectTeamGroupReadFence(mock, r.GroupID, r.ActorID, r.TeamID, int64(1), true)
				case "initial missing member":
					expectTeamGroupReadFence(mock, r.GroupID, r.ActorID, r.TeamID, nil, false)
				case "initial storage failure":
					mock.ExpectQuery("^"+regexp.QuoteMeta(readFenceTestSQL)+"$").WithArgs(r.GroupID, r.ActorID, r.TeamID).
						WillReturnError(errors.New("private SQL content"))
				default:
					closed := any(nil)
					if name == "large exact generation" {
						closed = int64(math.MaxInt64 - 1)
					}
					expectTeamGroupReadFence(mock, r.GroupID, r.ActorID, r.TeamID, closed, true)
					mock.ExpectQuery(regexp.QuoteMeta(triggerHistoryQuery)).WithArgs(r.TeamID, r.ActorID, r.GroupID, r.MessageID).
						WillReturnRows(triggerContextMessageRows(source))
					if finalGeneration == initialGeneration && finalErr == nil {
						switch name {
						case "final closed":
							expectTeamGroupReadFence(mock, r.GroupID, r.ActorID, r.TeamID, int64(1), true)
						case "final missing member":
							expectTeamGroupReadFence(mock, r.GroupID, r.ActorID, r.TeamID, nil, false)
						case "final storage failure":
							mock.ExpectQuery("^"+regexp.QuoteMeta(readFenceTestSQL)+"$").WithArgs(r.GroupID, r.ActorID, r.TeamID).
								WillReturnError(errors.New("private SQL content"))
						default:
							expectTeamGroupReadFence(mock, r.GroupID, r.ActorID, r.TeamID, closed, true)
						}
					}
				}
			}
			calls := 0
			s := &triggerContextServer{db: im.db, agentDNSName: "agent.go-im.internal", teams: triggerContextGenerationFunc(func(_ context.Context, actor, team int64) (int64, error) {
				calls++
				if actor != r.ActorID || team != r.TeamID {
					t.Fatal("generation check did not use persisted scope")
				}
				if calls == 1 {
					return initialGeneration, nil
				}
				return finalGeneration, finalErr
			})}
			response, err := s.ReadTaskTriggerContext(triggerContextAgent(context.Background()), &pb.ReadTaskTriggerContextRequest{MessageId: r.MessageID})
			if status.Code(err) != want || calls != wantCalls || err != nil && strings.Contains(err.Error(), "private") {
				t.Fatalf("generation checks=%d response=%v err=%v want=%v", calls, response, err, want)
			}
			if want == codes.OK {
				if response == nil || len(response.GetMessages()) != 1 || response.GetMessages()[0].GetContent() != source.Content {
					t.Fatal("large exact active generation lost its authorized history")
				}
			} else if response != nil {
				t.Fatal("failed generation/fence check disclosed partial context")
			}
		})
	}
}

func TestTriggerReadFlowActualTLSRejectsChangesAfterHistoryWithoutPartialContext(t *testing.T) {
	for _, name := range []string{"generation changed", "final User revocation", "final IM closure", "final group removal"} {
		t.Run(name, func(t *testing.T) {
			f := newTriggerReadFlow(t)
			f.expectSource(true)
			expectTriggerContextHistory(f.mock, f.outbox, triggerContextMessageRows(f.source))
			switch name {
			case "generation changed":
				f.userMode.Store(4)
			case "final User revocation":
				f.userMode.Store(5)
			case "final IM closure":
				expectTeamGroupReadFence(f.mock, f.outbox.GroupID, f.outbox.ActorID, f.outbox.TeamID, int64(1), true)
			case "final group removal":
				expectTeamGroupReadFence(f.mock, f.outbox.GroupID, f.outbox.ActorID, f.outbox.TeamID, nil, false)
			}
			result, err := f.client.Read(context.Background(), f.source.ID)
			if result != nil || status.Code(err) != codes.PermissionDenied || f.userCalls.Load() != 2 || strings.Contains(err.Error(), "private") {
				t.Fatalf("stale context returned: %v %v checks=%d", result, err, f.userCalls.Load())
			}
		})
	}
}
