package push

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/pkg/snowflake"
	"github.com/yjydist/go-im/internal/repository"
	"github.com/yjydist/go-im/internal/ws"
	"go.uber.org/zap"
)

type groupDeliveryRedisStub struct {
	repository.RedisRepository
	getOnlineError error
	onlineAddr     string
	visited        []int64
}

func (r *groupDeliveryRedisStub) GetOnline(_ context.Context, userID int64) (string, error) {
	r.visited = append(r.visited, userID)
	if userID == 2 {
		return "", r.getOnlineError
	}
	return r.onlineAddr, nil
}

type groupDeliveryGroupStub struct {
	repository.GroupRepository
	members                                                   []int64
	err                                                       error
	calls                                                     int
	teamID                                                    *int64
	groupErr                                                  error
	allowed                                                   bool
	checkErr                                                  error
	checkCalls                                                int
	checkedGroup, checkedTeam, checkedUser, checkedGeneration int64
}

func (r *groupDeliveryGroupStub) GetByID(_ context.Context, groupID int64) (*model.Group, error) {
	if r.groupErr != nil {
		return nil, r.groupErr
	}
	return &model.Group{ID: groupID, TeamID: r.teamID}, nil
}

func (r *groupDeliveryGroupStub) CheckTeamGroupMemberGeneration(_ context.Context, groupID, teamID, userID, generation int64) (bool, error) {
	r.checkCalls++
	r.checkedGroup, r.checkedTeam, r.checkedUser, r.checkedGeneration = groupID, teamID, userID, generation
	return r.allowed, r.checkErr
}

func (r *groupDeliveryGroupStub) ListMemberIDs(context.Context, int64) ([]int64, error) {
	r.calls++
	return r.members, r.err
}

type groupDeliveryMessageStub struct {
	repository.MessageRepository
	offlineUsers []int64
	created      *model.Message
}

func (r *groupDeliveryMessageStub) Create(_ context.Context, msg *model.Message) error {
	r.created = msg
	return nil
}

func (r *groupDeliveryMessageStub) CreateOffline(_ context.Context, offline *model.OfflineMessage) error {
	r.offlineUsers = append(r.offlineUsers, offline.UserID)
	return nil
}

func TestPushToGroupReturnsMemberErrorAfterTryingOthers(t *testing.T) {
	failure := errors.New("redis unavailable for member")
	for _, tc := range []struct {
		name      string
		memberErr error
	}{
		{"all members succeed", nil},
		{"one member fails", failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			redis := &groupDeliveryRedisStub{getOnlineError: tc.memberErr}
			messages := &groupDeliveryMessageStub{}
			groups := &groupDeliveryGroupStub{members: []int64{1, 2, 3}}
			pusher := &Pusher{groupRepo: groups, redisRepo: redis, messageRepo: messages, logger: zap.NewNop()}
			err := pusher.pushToGroup(context.Background(), 100, 1, &model.Message{ID: 7, MsgID: "m1"})
			if !reflect.DeepEqual(redis.visited, []int64{2, 3}) || groups.calls != 1 {
				t.Fatalf("visited members = %v, DB calls = %d", redis.visited, groups.calls)
			}
			if tc.memberErr == nil {
				if err != nil || !reflect.DeepEqual(messages.offlineUsers, []int64{2, 3}) {
					t.Fatalf("error = %v, offline users = %v", err, messages.offlineUsers)
				}
			} else if !errors.Is(err, failure) || !reflect.DeepEqual(messages.offlineUsers, []int64{3}) {
				t.Fatalf("error = %v, offline users = %v", err, messages.offlineUsers)
			}
		})
	}
}

func TestKafkaGroupMessagePreservesIdentityForOnlineAndOfflineMembers(t *testing.T) {
	if err := snowflake.Init(1); err != nil {
		t.Fatal(err)
	}
	for _, senderType := range []int8{0, model.MessageSenderBot} {
		t.Run(map[int8]string{0: "legacy user", 2: "bot with user ID collision"}[senderType], func(t *testing.T) {
			var onlineUsers []int64
			var initiator int64
			contentType, content := 1, "ordinary message"
			if senderType == model.MessageSenderBot {
				initiator = 3
				contentType = int(model.MessageContentTaskCard)
				var err error
				content, err = model.EncodeTaskCreatedCard(9007199254740993, "已确认标题")
				if err != nil {
					t.Fatal(err)
				}
			}
			wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload ws.PushMsg
				var msg model.Message
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if err := json.Unmarshal(payload.Data, &msg); err != nil || msg.InitiatorID != initiator ||
					msg.FromID != 1 || msg.SenderType != max(senderType, model.MessageSenderUser) ||
					int(msg.ContentType) != contentType || msg.Content != content {
					t.Errorf("push lost sender identity: %+v err=%v", msg, err)
				}
				onlineUsers = append(onlineUsers, payload.UserID)
			}))
			defer wsServer.Close()
			redis := &groupDeliveryRedisStub{onlineAddr: strings.TrimPrefix(wsServer.URL, "http://")}
			messages := &groupDeliveryMessageStub{}
			groups := &groupDeliveryGroupStub{members: []int64{1, 2, 3}}
			pusher := NewPusher(messages, groups, redis, zap.NewNop())
			pusher.httpClient = wsServer.Client()
			encoded, err := json.Marshal(ws.KafkaChatMsg{MsgID: "result", FromID: 1, ToID: 100,
				ChatType: 2, ContentType: contentType, Content: content, SenderType: senderType, InitiatorID: initiator})
			if err != nil {
				t.Fatal(err)
			}
			var event ws.KafkaChatMsg
			if err := json.Unmarshal(encoded, &event); err != nil {
				t.Fatal(err)
			}
			if err := pusher.HandleMessage(context.Background(), &event); err != nil {
				t.Fatal(err)
			}
			wantOnline, wantVisited := []int64{3}, []int64{2, 3}
			if senderType == model.MessageSenderBot {
				wantOnline, wantVisited = []int64{1, 3}, []int64{1, 2, 3}
			}
			if messages.created == nil || messages.created.SenderType != max(senderType, model.MessageSenderUser) ||
				int(messages.created.ContentType) != contentType || messages.created.Content != content ||
				messages.created.InitiatorID != initiator || !reflect.DeepEqual(redis.visited, wantVisited) ||
				!reflect.DeepEqual(onlineUsers, wantOnline) || !reflect.DeepEqual(messages.offlineUsers, []int64{2}) {
				t.Fatalf("identity or recipients changed: saved=%+v visited=%v online=%v offline=%v",
					messages.created, redis.visited, onlineUsers, messages.offlineUsers)
			}
		})
	}
}

func TestPushToGroupUsesCurrentMembers(t *testing.T) {
	redis := &groupDeliveryRedisStub{}
	messages := &groupDeliveryMessageStub{}
	groups := &groupDeliveryGroupStub{members: []int64{1, 2}}
	pusher := &Pusher{groupRepo: groups, redisRepo: redis, messageRepo: messages, logger: zap.NewNop()}
	msg := &model.Message{ID: 7, MsgID: "m1"}
	if err := pusher.pushToGroup(context.Background(), 100, 1, msg); err != nil {
		t.Fatal(err)
	}
	groups.members = []int64{1, 3}
	if err := pusher.pushToGroup(context.Background(), 100, 1, msg); err != nil {
		t.Fatal(err)
	}
	if groups.calls != 2 || !reflect.DeepEqual(messages.offlineUsers, []int64{2, 3}) {
		t.Fatalf("DB calls = %d, recipients = %v", groups.calls, messages.offlineUsers)
	}
}

func TestPushToGroupDoesNotDeliverWhenMembershipQueryFails(t *testing.T) {
	failure := errors.New("MySQL unavailable")
	redis := &groupDeliveryRedisStub{}
	groups := &groupDeliveryGroupStub{err: failure}
	pusher := &Pusher{groupRepo: groups, redisRepo: redis, logger: zap.NewNop()}
	err := pusher.pushToGroup(context.Background(), 100, 1, &model.Message{ID: 7, MsgID: "m1"})
	if !errors.Is(err, failure) || len(redis.visited) != 0 {
		t.Fatalf("error = %v, visited = %v", err, redis.visited)
	}
}

func TestPushToUserSavesOfflineWhenWSRejectsQueue(t *testing.T) {
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer wsServer.Close()
	redis := &groupDeliveryRedisStub{onlineAddr: strings.TrimPrefix(wsServer.URL, "http://")}
	messages := &groupDeliveryMessageStub{}
	pusher := &Pusher{redisRepo: redis, messageRepo: messages, httpClient: wsServer.Client(), logger: zap.NewNop()}
	if err := pusher.pushToUser(context.Background(), 42, 1, &model.Message{ID: 7, MsgID: "m1"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(messages.offlineUsers, []int64{42}) {
		t.Fatalf("offline users = %v", messages.offlineUsers)
	}
}

func TestPushToUserKeepsMessageIDAcrossRetries(t *testing.T) {
	var received []string
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			UserID int64 `json:"user_id"`
			Data   struct {
				MsgID string `json:"msg_id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.UserID != 42 {
			t.Errorf("invalid push payload: %+v, %v", payload, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received = append(received, payload.Data.MsgID)
	}))
	defer wsServer.Close()
	redis := &groupDeliveryRedisStub{onlineAddr: strings.TrimPrefix(wsServer.URL, "http://")}
	messages := &groupDeliveryMessageStub{}
	pusher := &Pusher{redisRepo: redis, messageRepo: messages, httpClient: wsServer.Client(), logger: zap.NewNop()}
	msg := &model.Message{ID: 7, MsgID: "stable-uuid"}
	for i := 0; i < 2; i++ {
		if err := pusher.pushToUser(context.Background(), 42, 1, msg); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(received, []string{"stable-uuid", "stable-uuid"}) || len(messages.offlineUsers) != 0 {
		t.Fatalf("received message IDs = %v, offline users = %v", received, messages.offlineUsers)
	}
}
