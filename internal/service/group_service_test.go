package service

import (
	"context"
	"testing"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/internal/repository"
)

type joinGroupRepo struct {
	repository.GroupRepository
	group       *model.Group
	memberCalls int
}

func (r *joinGroupRepo) GetByID(context.Context, int64) (*model.Group, error) {
	return r.group, nil
}

func (r *joinGroupRepo) GetMember(context.Context, int64, int64) (*model.GroupMember, error) {
	r.memberCalls++
	return &model.GroupMember{}, nil
}

func TestJoinGroupRejectsTeamGroupBeforeMembershipWrite(t *testing.T) {
	teamID := int64(200)
	repo := &joinGroupRepo{group: &model.Group{ID: 100, TeamID: &teamID}}
	s := &groupService{groupRepo: repo}
	err := s.JoinGroup(context.Background(), 100, 42)
	code, ok := ParseBusinessError(err)
	if !ok || code != errcode.ErrForbidden || repo.memberCalls != 0 {
		t.Fatalf("team group join: code=%d, business=%t, member queries=%d, err=%v", code, ok, repo.memberCalls, err)
	}
}

func TestJoinGroupKeepsLegacyGroupPath(t *testing.T) {
	repo := &joinGroupRepo{group: &model.Group{ID: 100}}
	s := &groupService{groupRepo: repo}
	err := s.JoinGroup(context.Background(), 100, 42)
	code, ok := ParseBusinessError(err)
	if !ok || code != errcode.ErrGroupMember || repo.memberCalls != 1 {
		t.Fatalf("legacy group join: code=%d, business=%t, member queries=%d, err=%v", code, ok, repo.memberCalls, err)
	}
}
