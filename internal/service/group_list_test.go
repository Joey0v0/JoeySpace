package service

import (
	"context"
	"errors"
	"testing"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/internal/repository"
	"gorm.io/gorm"
)

type groupListRepo struct {
	repository.GroupRepository
	group       *model.Group
	memberErr   error
	memberCalls int
	listCalls   int
}

func (r *groupListRepo) GetByID(context.Context, int64) (*model.Group, error) {
	return r.group, nil
}

func (r *groupListRepo) GetMember(context.Context, int64, int64) (*model.GroupMember, error) {
	r.memberCalls++
	return &model.GroupMember{}, r.memberErr
}

func (r *groupListRepo) ListMembers(context.Context, int64) ([]model.GroupMember, error) {
	r.listCalls++
	return []model.GroupMember{{GroupID: 100, UserID: 42}}, nil
}

func TestListMembersRejectsTeamGroupBeforeMemberQuery(t *testing.T) {
	teamID := int64(200)
	repo := &groupListRepo{group: &model.Group{ID: 100, TeamID: &teamID}}
	members, err := (&groupService{groupRepo: repo}).ListMembers(context.Background(), 100, 42)
	code, ok := ParseBusinessError(err)
	if members != nil || !ok || code != errcode.ErrForbidden || repo.memberCalls != 0 || repo.listCalls != 0 {
		t.Fatalf("team group list: code=%d, business=%t, member queries=%d, lists=%d, err=%v", code, ok, repo.memberCalls, repo.listCalls, err)
	}
}

func TestListMembersRequiresLegacyGroupMembership(t *testing.T) {
	for _, tc := range []struct {
		name       string
		memberErr  error
		wantCode   int
		wantLists  int
		wantResult bool
	}{
		{"member", nil, 0, 1, true},
		{"non-member", gorm.ErrRecordNotFound, errcode.ErrGroupNotMember, 0, false},
		{"database error", errors.New("database unavailable"), 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &groupListRepo{group: &model.Group{ID: 100}, memberErr: tc.memberErr}
			members, err := (&groupService{groupRepo: repo}).ListMembers(context.Background(), 100, 42)
			code, isBiz := ParseBusinessError(err)
			if (members != nil) != tc.wantResult || repo.memberCalls != 1 || repo.listCalls != tc.wantLists {
				t.Fatalf("list flow: members=%v, member queries=%d, lists=%d, err=%v", members, repo.memberCalls, repo.listCalls, err)
			}
			if tc.wantCode != 0 && (!isBiz || code != tc.wantCode) {
				t.Fatalf("business error: code=%d, err=%v", code, err)
			}
			if tc.memberErr != nil && tc.wantCode == 0 && (err == nil || isBiz) {
				t.Fatalf("database error must not become business error: %v", err)
			}
		})
	}
}
