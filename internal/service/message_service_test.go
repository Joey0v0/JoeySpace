package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type offlineMessageRepoStub struct {
	repository.MessageRepository
	messages     []model.Message
	deleted      []int64
	deleteUserID int64
}

func (r *offlineMessageRepoStub) ListOffline(context.Context, int64) ([]model.Message, error) {
	return r.messages, nil
}

func (r *offlineMessageRepoStub) DeleteOffline(_ context.Context, userID int64, messageIDs []int64) error {
	r.deleteUserID = userID
	r.deleted = append(r.deleted, messageIDs...)
	return nil
}

func TestOfflinePullKeepsMessagesUntilAcknowledged(t *testing.T) {
	repo := &offlineMessageRepoStub{messages: []model.Message{{ID: 7}, {ID: 8}}}
	s := &messageService{msgRepo: repo, logger: zap.NewNop()}
	messages, err := s.GetOfflineMessages(context.Background(), 42)
	if err != nil || !reflect.DeepEqual(messages, repo.messages) || len(repo.deleted) != 0 {
		t.Fatalf("messages=%v, deleted=%v, err=%v", messages, repo.deleted, err)
	}
	if again, err := s.GetOfflineMessages(context.Background(), 42); err != nil || !reflect.DeepEqual(again, messages) {
		t.Fatalf("retry pull: messages=%v, err=%v", again, err)
	}
}

func TestOfflineAckDeletesOnlyRequestedIDsForCurrentUser(t *testing.T) {
	repo := &offlineMessageRepoStub{}
	s := &messageService{msgRepo: repo}
	for _, ids := range [][]int64{nil, {0}, {-1}, make([]int64, 1001)} {
		if err := s.AckOfflineMessages(context.Background(), 42, ids); err == nil {
			t.Fatalf("accepted invalid IDs: %v", ids)
		}
	}
	if len(repo.deleted) != 0 {
		t.Fatalf("invalid acknowledgement deleted %v", repo.deleted)
	}
	if err := s.AckOfflineMessages(context.Background(), 42, []int64{7, 8}); err != nil {
		t.Fatal(err)
	}
	if repo.deleteUserID != 42 || !reflect.DeepEqual(repo.deleted, []int64{7, 8}) {
		t.Fatalf("user=%d, IDs=%v", repo.deleteUserID, repo.deleted)
	}
}

type historyGroupRepo struct {
	repository.GroupRepository
	getMember func(groupID, userID int64) (*model.GroupMember, error)
	getByID   func(groupID int64) (*model.Group, error)
}

func (r historyGroupRepo) GetMember(_ context.Context, groupID, userID int64) (*model.GroupMember, error) {
	return r.getMember(groupID, userID)
}

func (r historyGroupRepo) GetByID(_ context.Context, groupID int64) (*model.Group, error) {
	return r.getByID(groupID)
}

type historyMessageRepo struct {
	repository.MessageRepository
	getHistory func(userID, targetID int64, chatType int8) ([]model.Message, error)
}

func (r historyMessageRepo) GetHistory(_ context.Context, userID, targetID int64, chatType int8, _ int64, _ int) ([]model.Message, error) {
	return r.getHistory(userID, targetID, chatType)
}

func TestGroupHistoryChecksMembershipBeforeReading(t *testing.T) {
	for _, tc := range []struct {
		name      string
		memberErr error
		wantCode  int
	}{
		{"member", nil, errcode.Success},
		{"non-member", gorm.ErrRecordNotFound, errcode.ErrGroupNotMember},
		{"database failure", errors.New("database disconnected"), errcode.ErrInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readCount := 0
			s := &messageService{
				groupRepo: historyGroupRepo{getMember: func(groupID, userID int64) (*model.GroupMember, error) {
					if groupID != 100 || userID != 42 {
						t.Fatalf("wrong membership lookup: %d, %d", groupID, userID)
					}
					if tc.memberErr != nil {
						return nil, tc.memberErr
					}
					return &model.GroupMember{GroupID: 100, UserID: 42}, nil
				}, getByID: func(groupID int64) (*model.Group, error) {
					if groupID != 100 {
						t.Fatalf("wrong group lookup: %d", groupID)
					}
					return &model.Group{ID: groupID}, nil
				}},
				msgRepo: historyMessageRepo{getHistory: func(userID, targetID int64, chatType int8) ([]model.Message, error) {
					readCount++
					if userID != 42 || targetID != 100 || chatType != 2 {
						t.Fatalf("wrong history lookup: %d, %d, %d", userID, targetID, chatType)
					}
					return []model.Message{{ID: 7}}, nil
				}},
			}
			messages, err := s.GetHistory(context.Background(), 42, 100, 2, 0, 20)
			code, business := ParseBusinessError(err)
			if tc.wantCode == errcode.Success {
				if err != nil || len(messages) != 1 || readCount != 1 {
					t.Fatalf("member history: %v, %v, reads=%d", messages, err, readCount)
				}
			} else if messages != nil || readCount != 0 || code != tc.wantCode || business != (tc.wantCode == errcode.ErrGroupNotMember) {
				t.Fatalf("rejected history: %v, %v, reads=%d", messages, err, readCount)
			}
		})
	}
}

func TestDirectHistoryDoesNotQueryGroupMembership(t *testing.T) {
	s := &messageService{
		groupRepo: historyGroupRepo{getMember: func(int64, int64) (*model.GroupMember, error) {
			t.Fatal("direct chat must not query group membership")
			return nil, nil
		}},
		msgRepo: historyMessageRepo{getHistory: func(_, _ int64, chatType int8) ([]model.Message, error) {
			if chatType != 1 {
				t.Fatal("wrong chat type")
			}
			return []model.Message{{ID: 9}}, nil
		}},
	}
	messages, err := s.GetHistory(context.Background(), 42, 7, 1, 0, 20)
	if err != nil || len(messages) != 1 {
		t.Fatalf("direct history: %v, %v", messages, err)
	}
}

func TestLegacyHistoryRejectsTeamGroupBeforeReadingMessages(t *testing.T) {
	teamID := int64(200)
	reads := 0
	s := &messageService{
		groupRepo: historyGroupRepo{
			getMember: func(int64, int64) (*model.GroupMember, error) {
				return &model.GroupMember{GroupID: 100, UserID: 42}, nil
			},
			getByID: func(int64) (*model.Group, error) {
				return &model.Group{ID: 100, TeamID: &teamID}, nil
			},
		},
		msgRepo: historyMessageRepo{getHistory: func(int64, int64, int8) ([]model.Message, error) {
			reads++
			return nil, nil
		}},
	}
	messages, err := s.GetHistory(context.Background(), 42, 100, 2, 0, 20)
	code, business := ParseBusinessError(err)
	if messages != nil || !business || code != errcode.ErrForbidden || reads != 0 {
		t.Fatalf("team history must be refused: messages=%v, code=%d, reads=%d, err=%v", messages, code, reads, err)
	}
}

func TestLegacyHistoryGroupLookupFailureDoesNotReadMessages(t *testing.T) {
	reads := 0
	s := &messageService{
		groupRepo: historyGroupRepo{
			getMember: func(int64, int64) (*model.GroupMember, error) {
				return &model.GroupMember{GroupID: 100, UserID: 42}, nil
			},
			getByID: func(int64) (*model.Group, error) {
				return nil, errors.New("database unavailable")
			},
		},
		msgRepo: historyMessageRepo{getHistory: func(int64, int64, int8) ([]model.Message, error) {
			reads++
			return nil, nil
		}},
	}
	messages, err := s.GetHistory(context.Background(), 42, 100, 2, 0, 20)
	if messages != nil || err == nil || reads != 0 {
		t.Fatalf("group lookup failure must refuse history: messages=%v, reads=%d, err=%v", messages, reads, err)
	}
}
