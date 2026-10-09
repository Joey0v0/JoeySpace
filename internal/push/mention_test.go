package push

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/pkg/snowflake"
	"github.com/yjydist/go-im/internal/ws"
	"go.uber.org/zap"
)

type mentionEligibility struct {
	denied  int64
	checked []int64
}

func (s *mentionEligibility) CheckCurrentTeamMember(_ context.Context, team, user int64) (int64, bool, error) {
	s.checked = append(s.checked, user)
	return 7, user != s.denied, nil
}

type mentionValidatorStub struct {
	calls                           int
	group, team, sender, generation int64
	targets                         []MentionMemberGeneration
	err                             error
}

func (s *mentionValidatorStub) ValidateGroupMentionTargets(_ context.Context, group, team, sender, generation int64, targets []MentionMemberGeneration) error {
	s.calls++
	s.group, s.team, s.sender, s.generation = group, team, sender, generation
	s.targets = append([]MentionMemberGeneration(nil), targets...)
	return s.err
}

type mentionMessageStore struct {
	groupDeliveryMessageStub
	calls int
	ids   []int64
}

func (s *mentionMessageStore) CreateWithMentions(_ context.Context, msg *model.Message, _ int64, ids []int64) error {
	s.calls++
	s.created = msg
	s.ids = append([]int64(nil), ids...)
	return nil
}

func TestMentionValidationPrecedesPersistenceAndDelivery(t *testing.T) {
	if err := snowflake.Init(1); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		event       ws.KafkaChatMsg
		denied      int64
		imErr       error
		wantPersist bool
	}{
		{"valid", ws.KafkaChatMsg{MsgID: "m1", FromID: 1, ToID: 300, ChatType: 2, ContentType: 1, Content: "hello", MentionedUserIDs: []int64{2, 3}}, 0, nil, true},
		{"wrong chat", ws.KafkaChatMsg{MsgID: "m1", FromID: 1, ToID: 300, ChatType: 1, ContentType: 1, Content: "hello", MentionedUserIDs: []int64{2}}, 0, nil, false},
		{"duplicate", ws.KafkaChatMsg{MsgID: "m1", FromID: 1, ToID: 300, ChatType: 2, ContentType: 1, Content: "hello", MentionedUserIDs: []int64{2, 2}}, 0, nil, false},
		{"sender revoked", ws.KafkaChatMsg{MsgID: "m1", FromID: 1, ToID: 300, ChatType: 2, ContentType: 1, Content: "hello", MentionedUserIDs: []int64{2}}, 1, nil, false},
		{"target revoked", ws.KafkaChatMsg{MsgID: "m1", FromID: 1, ToID: 300, ChatType: 2, ContentType: 1, Content: "hello", MentionedUserIDs: []int64{2}}, 2, nil, false},
		{"IM rejects fence", ws.KafkaChatMsg{MsgID: "m1", FromID: 1, ToID: 300, ChatType: 2, ContentType: 1, Content: "hello", MentionedUserIDs: []int64{2}}, 0, errors.New("closed"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			team := int64(100)
			groups := &groupDeliveryGroupStub{teamID: &team}
			store := &mentionMessageStore{}
			eligibility := &mentionEligibility{denied: tc.denied}
			validator := &mentionValidatorStub{err: tc.imErr}
			p := NewPusher(store, groups, &groupDeliveryRedisStub{}, zap.NewNop())
			p.SetTeamEligibility(eligibility)
			p.SetMentionValidator(validator)
			err := p.HandleMessage(context.Background(), &tc.event)
			if tc.wantPersist {
				if err != nil || store.calls != 1 || store.created == nil || !reflect.DeepEqual(store.created.MentionedUserIDs, tc.event.MentionedUserIDs) || !reflect.DeepEqual(store.ids, tc.event.MentionedUserIDs) || validator.calls != 1 || validator.team != team || validator.generation != 7 {
					t.Fatalf("valid mention not persisted: err=%v store=%+v validator=%+v", err, store, validator)
				}
			} else if err == nil || store.calls != 0 || store.created != nil || groups.calls != 0 {
				t.Fatalf("unauthorized mention reached persistence/delivery: err=%v store=%+v groupCalls=%d", err, store, groups.calls)
			}
		})
	}
}

func TestMentionClientRequiresIndependentCompleteConfiguration(t *testing.T) {
	if c, err := LoadMentionClientConfig(func(string) string { return "" }); err != nil || c.Addr != "" {
		t.Fatalf("empty optional config: %+v %v", c, err)
	}
	if _, err := LoadMentionClientConfig(func(k string) string {
		if k == "PUSH_IM_MENTION_RPC_ADDR" {
			return "im:9443"
		}
		return ""
	}); err == nil {
		t.Fatal("partial TLS config accepted")
	}
	if client, err := NewMentionClient(MentionClientConfig{}); client != nil || err != nil {
		t.Fatalf("absent optional client: %+v %v", client, err)
	}
	if err := (*MentionClient)(nil).ValidateGroupMentionTargets(context.Background(), 300, 100, 1, 7, []MentionMemberGeneration{{UserID: 2, Generation: 7}}); err == nil {
		t.Fatal("missing validator accepted")
	}
}
