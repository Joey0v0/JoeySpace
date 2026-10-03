package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type assigneeClientFunc func(context.Context, *userpb.ResolveTeamMemberRequest) (*userpb.ResolveTeamMemberResponse, error)

func (f assigneeClientFunc) ResolveTeamMember(ctx context.Context, req *userpb.ResolveTeamMemberRequest, _ ...grpc.CallOption) (*userpb.ResolveTeamMemberResponse, error) {
	return f(ctx, req)
}

func matchingCandidates(count int) []*userpb.TeamMember {
	items := make([]*userpb.TeamMember, 0, count)
	for i := 1; i <= count; i++ {
		items = append(items, &userpb.TeamMember{UserId: int64(i), Username: fmt.Sprintf("zhang-%d", i), Nickname: "张三"})
	}
	return items
}

func TestDraftAssigneeResolvesWithOriginalIdentityAndExplicitOutcomes(t *testing.T) {
	for _, tc := range []struct {
		count     int
		truncated bool
		state     draftAssigneeResolution
		id        int64
	}{
		{0, false, assigneeNotFound, 0}, {1, false, assigneeMatched, 1},
		{2, false, assigneeAmbiguous, 0}, {20, false, assigneeAmbiguous, 0}, {20, true, assigneeTruncated, 0},
	} {
		t.Run(string(tc.state)+fmt.Sprint(tc.count), func(t *testing.T) {
			r := &draftAssigneeResolver{users: assigneeClientFunc(func(ctx context.Context, req *userpb.ResolveTeamMemberRequest) (*userpb.ResolveTeamMemberResponse, error) {
				md, _ := metadata.FromOutgoingContext(ctx)
				if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer user-token" || len(md.Get("idempotency-key")) != 0 || req.GetTeamId() != 200 || req.GetName() != "张三" {
					t.Errorf("identity or scope changed: %v, %v", md, req)
				}
				return &userpb.ResolveTeamMemberResponse{Candidates: matchingCandidates(tc.count), Truncated: tc.truncated}, nil
			})}
			draft, err := r.resolve(context.Background(), "user-token", 200, taskDraft{Title: "整理文档", AssigneeName: " 张三 "})
			if err != nil || draft.AssigneeName != "张三" || draft.AssigneeResolution != tc.state || draft.AssigneeID != tc.id {
				t.Fatalf("result: %+v, %v", draft, err)
			}
		})
	}
}

func TestDraftAssigneeDoesNotGuessOnInvalidResponsesOrFailures(t *testing.T) {
	for _, result := range []*userpb.ResolveTeamMemberResponse{
		nil, {Candidates: []*userpb.TeamMember{nil}}, {Candidates: []*userpb.TeamMember{{UserId: 0, Nickname: "张三"}}},
		{Candidates: []*userpb.TeamMember{{UserId: 1, Nickname: "张四"}}},
		{Candidates: []*userpb.TeamMember{{UserId: 2, Nickname: "张三"}, {UserId: 1, Nickname: "张三"}}},
		{Candidates: []*userpb.TeamMember{{UserId: 1, Nickname: "张三"}, {UserId: 1, Nickname: "张三"}}},
		{Candidates: matchingCandidates(21)}, {Candidates: matchingCandidates(1), Truncated: true},
	} {
		r := &draftAssigneeResolver{users: assigneeClientFunc(func(context.Context, *userpb.ResolveTeamMemberRequest) (*userpb.ResolveTeamMemberResponse, error) {
			return result, nil
		})}
		if d, err := r.resolve(context.Background(), "user-token", 200, taskDraft{AssigneeName: "张三"}); status.Code(err) != codes.Unavailable || d.AssigneeID != 0 {
			t.Fatalf("invalid response: %v, %+v, %v", result, d, err)
		}
	}
	for _, code := range []codes.Code{codes.PermissionDenied, codes.Unauthenticated, codes.NotFound, codes.Unavailable, codes.Unimplemented} {
		r := &draftAssigneeResolver{users: assigneeClientFunc(func(context.Context, *userpb.ResolveTeamMemberRequest) (*userpb.ResolveTeamMemberResponse, error) {
			return nil, status.Error(code, "private detail")
		})}
		want := code
		if code == codes.Unimplemented {
			want = codes.Unavailable
		}
		d, err := r.resolve(context.Background(), "user-token", 200, taskDraft{AssigneeName: "张三"})
		if status.Code(err) != want || d.AssigneeResolution != "" || strings.Contains(err.Error(), "private detail") {
			t.Fatalf("lookup failure: %+v, %v", d, err)
		}
	}
	r := &draftAssigneeResolver{users: assigneeClientFunc(func(ctx context.Context, _ *userpb.ResolveTeamMemberRequest) (*userpb.ResolveTeamMemberResponse, error) {
		return nil, errors.New("private transport")
	})}
	if _, err := r.resolve(context.Background(), "user-token", 200, taskDraft{AssigneeName: "张三"}); status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
}

func TestDraftAssigneeNoMentionSkipsLookupAndRejectsModelIDs(t *testing.T) {
	var r *draftAssigneeResolver
	d, err := r.resolve(context.Background(), "user-token", 200, taskDraft{Title: "任务"})
	if err != nil || d.AssigneeResolution != assigneeNone || d.AssigneeID != 0 {
		t.Fatalf("no mention: %+v, %v", d, err)
	}
	for _, bad := range []taskDraft{{AssigneeID: 3}, {AssigneeName: "张三", AssigneeResolution: assigneeMatched}, {AssigneeName: "\xff"}, {AssigneeName: strings.Repeat("张", 65)}} {
		if _, err := r.resolve(context.Background(), "user-token", 200, bad); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("model supplied fields: %+v, %v", bad, err)
		}
	}
	if _, err := r.resolve(context.Background(), "user-token", 200, taskDraft{AssigneeName: "张三"}); status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
}

func TestDraftAssigneeStateShapeAndOldConfirmation(t *testing.T) {
	for _, draft := range []taskDraft{
		{AssigneeID: 7}, {AssigneeResolution: assigneeNone},
		{AssigneeName: "张三", AssigneeResolution: assigneeMatched, AssigneeID: 7},
		{AssigneeName: "张三", AssigneeResolution: assigneeNotFound},
		{AssigneeName: "张三", AssigneeResolution: assigneeAmbiguous},
		{AssigneeName: "张三", AssigneeResolution: assigneeTruncated},
	} {
		if !draft.validAssigneeResolution() {
			t.Fatalf("valid shape: %+v", draft)
		}
		err := draft.requireAssigneeReview(nil)
		if draft.AssigneeName == "" && err != nil || draft.AssigneeName != "" && status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("review gate: %+v, %v", draft, err)
		}
	}
	for _, draft := range []taskDraft{{AssigneeName: "张三"}, {AssigneeResolution: assigneeMatched}, {AssigneeResolution: assigneeNone, AssigneeID: 7}, {AssigneeName: "张三", AssigneeResolution: assigneeAmbiguous, AssigneeID: 7}, {AssigneeResolution: "unknown"}, {AssigneeName: " 张三", AssigneeResolution: assigneeMatched, AssigneeID: 7}} {
		if draft.validAssigneeResolution() {
			t.Fatalf("invalid shape accepted: %+v", draft)
		}
	}
}
