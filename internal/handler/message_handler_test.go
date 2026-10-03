package handler

import (
	"context"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/yjydist/go-im/internal/middleware"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/internal/service"
	"go.uber.org/zap"
)

type historyService struct {
	service.MessageService
	err error
}

type offlineHandlerService struct {
	service.MessageService
	messages []model.Message
	ackUser  int64
	ackIDs   []int64
}

func (s *offlineHandlerService) GetOfflineMessages(context.Context, int64) ([]model.Message, error) {
	return s.messages, nil
}

func (s *offlineHandlerService) AckOfflineMessages(_ context.Context, userID int64, messageIDs []int64) error {
	s.ackUser = userID
	s.ackIDs = append(s.ackIDs, messageIDs...)
	return nil
}

func TestOfflineHandlerReturnsStringIDsAndAcknowledgesThem(t *testing.T) {
	const messageID int64 = 9007199254740993
	stub := &offlineHandlerService{messages: []model.Message{
		{ID: messageID, MsgID: "m1", FromID: messageID + 1},
		{ID: messageID + 3, MsgID: "bot", FromID: messageID + 1, SenderType: 2, InitiatorID: messageID + 2},
	}}
	h := &MessageHandler{msgService: stub, logger: zap.NewNop()}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/message/offline", nil)
	c.Set(middleware.ContextKeyUserID, int64(42))
	h.GetOfflineMessages(c)
	if !strings.Contains(w.Body.String(), `"id":"9007199254740993"`) || !strings.Contains(w.Body.String(), `"from_id":"9007199254740994"`) {
		t.Fatalf("unsafe offline IDs: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"sender_type":1`) || !strings.Contains(w.Body.String(), `"sender_type":2`) || !strings.Contains(w.Body.String(), `"initiator_id":"9007199254740995"`) {
		t.Fatalf("lost offline sender identity: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/api/v1/message/offline/ack", strings.NewReader(`{"message_ids":["9007199254740993"]}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(middleware.ContextKeyUserID, int64(42))
	h.AckOfflineMessages(c)
	if stub.ackUser != 42 || !reflect.DeepEqual(stub.ackIDs, []int64{messageID}) || !strings.Contains(w.Body.String(), `"code":0`) {
		t.Fatalf("ack user=%d IDs=%v response=%s", stub.ackUser, stub.ackIDs, w.Body.String())
	}
}

func TestOfflineAckRejectsInvalidIDsBeforeService(t *testing.T) {
	for _, body := range []string{`{"message_ids":[]}`, `{"message_ids":[9007199254740993]}`, `{"message_ids":["-1"]}`, `{"message_ids":["oops"]}`} {
		stub := &offlineHandlerService{}
		h := &MessageHandler{msgService: stub, logger: zap.NewNop()}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/api/v1/message/offline/ack", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set(middleware.ContextKeyUserID, int64(42))
		h.AckOfflineMessages(c)
		if len(stub.ackIDs) != 0 || !strings.Contains(w.Body.String(), fmt.Sprintf(`"code":%d`, errcode.ErrBadRequest)) {
			t.Fatalf("body=%s, IDs=%v, response=%s", body, stub.ackIDs, w.Body.String())
		}
	}
}

func (s historyService) GetHistory(context.Context, int64, int64, int8, int64, int) ([]model.Message, error) {
	return nil, s.err
}

func TestHistoryHandlerMapsMembershipDenial(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int
	}{
		{"non-member", fmt.Errorf("%w: %d", service.ErrBusiness, errcode.ErrGroupNotMember), errcode.ErrGroupNotMember},
		{"team group via legacy endpoint", fmt.Errorf("%w: %d", service.ErrBusiness, errcode.ErrForbidden), errcode.ErrForbidden},
		{"database failure", fmt.Errorf("database disconnected"), errcode.ErrInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("GET", "/api/v1/message/history?target_id=100&chat_type=2", nil)
			c.Set(middleware.ContextKeyUserID, int64(42))
			h := &MessageHandler{msgService: historyService{err: tc.err}, logger: zap.NewNop()}
			h.GetHistory(c)
			if w.Code != 200 || !strings.Contains(w.Body.String(), fmt.Sprintf(`"code":%d`, tc.code)) || strings.Contains(w.Body.String(), `"data"`) {
				t.Fatalf("unexpected history response: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
