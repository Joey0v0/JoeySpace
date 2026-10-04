package push

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/pkg/snowflake"
	"github.com/yjydist/go-im/internal/repository"
	"github.com/yjydist/go-im/internal/ws"
	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type triggerFlowID struct {
	id        int64
	source    *sqlmock.Rows
	reference time.Time
	added     bool
}

func (c *triggerFlowID) Match(v driver.Value) bool {
	id, ok := v.(int64)
	if !ok || id <= 0 {
		return false
	}
	c.id = id
	if !c.added {
		c.source.AddRow(id, "flow-client", int64(1), int64(1), int64(0), int64(100), int64(2), int64(1), "@AI 整理任务 明天修复缓存", c.reference)
		c.added = true
	}
	return true
}

type triggerFlowWriter struct {
	fail bool
	sent []kafka.Message
}

func (w *triggerFlowWriter) WriteMessages(_ context.Context, m ...kafka.Message) error {
	w.sent = append(w.sent, m...)
	if w.fail {
		return errors.New("broker unavailable")
	}
	return nil
}
func (w *triggerFlowWriter) Close() error { return nil }

func TestAgentTriggerMessageOutboxAndPublisherFlow(t *testing.T) {
	if err := snowflake.Init(1); err != nil {
		t.Fatal(err)
	}
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "chat delivered despite broker failure then stable retry", true: "outbox failure rolls back message before delivery"}[rollback], func(t *testing.T) {
			sqlDB, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer sqlDB.Close()
			db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Error(err)
				}
			}()
			ref := time.Date(2026, 10, 4, 3, 59, 59, 0, time.UTC)
			source := sqlmock.NewRows([]string{"id", "msg_id", "from_id", "sender_type", "initiator_id", "to_id", "chat_type", "content_type", "content", "created_at"})
			capture := &triggerFlowID{source: source, reference: ref}
			mock.ExpectBegin()
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `messages`")).WithArgs("flow-client", int64(1), int8(1), int64(0), int64(100), int8(2), int8(1), "@AI 整理任务 明天修复缓存", sqlmock.AnyArg(), capture).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery("SELECT .* FROM `messages` WHERE id = ").WillReturnRows(source)
			mock.ExpectQuery("SELECT .* FROM `groups` WHERE id = ").WillReturnRows(sqlmock.NewRows([]string{"id", "team_id"}).AddRow(100, 200))
			if rollback {
				mock.ExpectExec("INSERT INTO im_agent_trigger_outbox").WillReturnError(errors.New("outbox unavailable"))
				mock.ExpectRollback()
			} else {
				mock.ExpectExec("INSERT INTO im_agent_trigger_outbox").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
			var delivered *model.Message
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var p ws.PushMsg
				var msg model.Message
				if e := json.NewDecoder(r.Body).Decode(&p); e != nil {
					t.Error(e)
				}
				if e := json.Unmarshal(p.Data, &msg); e != nil {
					t.Error(e)
				}
				delivered = &msg
			}))
			defer srv.Close()
			groups := &groupDeliveryGroupStub{members: []int64{1, 3}}
			redis := &groupDeliveryRedisStub{onlineAddr: strings.TrimPrefix(srv.URL, "http://")}
			pusher := NewPusher(repository.NewAgentTriggerMessageRepository(db), groups, redis, zap.NewNop())
			pusher.httpClient = srv.Client()
			err = pusher.HandleMessage(context.Background(), &ws.KafkaChatMsg{MsgID: "flow-client", FromID: 1, ToID: 100, ChatType: 2, ContentType: 1, Content: "@AI 整理任务 明天修复缓存"})
			if rollback {
				if err == nil || groups.calls != 0 || delivered != nil {
					t.Fatalf("rolled back chat delivered: %v", err)
				}
				return
			}
			if err != nil || delivered == nil || delivered.ID != capture.id || !delivered.CreatedAt.Equal(ref) {
				t.Fatalf("saved chat delivery: %+v error %v", delivered, err)
			}
			row := model.AgentTriggerOutbox{MessageID: capture.id, Action: model.AgentTriggerAction, EventVersion: 1, MsgID: "flow-client", ActorID: 1, TeamID: 200, GroupID: 100, Instruction: "明天修复缓存", ReferenceTimeMS: ref.UnixMilli()}
			rows := func() *sqlmock.Rows {
				return sqlmock.NewRows([]string{"message_id", "action", "event_version", "msg_id", "actor_id", "team_id", "group_id", "instruction", "reference_time_ms", "published"}).AddRow(row.MessageID, row.Action, row.EventVersion, row.MsgID, row.ActorID, row.TeamID, row.GroupID, row.Instruction, row.ReferenceTimeMS, false)
			}
			writer := &triggerFlowWriter{fail: true}
			publisher := NewAgentTriggerPublisher(repository.NewAgentTriggerStore(db), writer, zap.NewNop())
			mock.ExpectQuery("SELECT .* FROM im_agent_trigger_outbox WHERE published = ").WillReturnRows(rows())
			if err := publisher.RunOnce(context.Background()); err == nil {
				t.Fatal("missing broker failure")
			}
			writer.fail = false
			mock.ExpectQuery("SELECT .* FROM im_agent_trigger_outbox WHERE published = ").WillReturnRows(rows())
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT .* FROM im_agent_trigger_outbox WHERE message_id = ").WillReturnRows(rows())
			mock.ExpectExec("UPDATE im_agent_trigger_outbox SET published = ").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			if err := publisher.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(writer.sent) != 2 || !reflect.DeepEqual(writer.sent[0].Key, writer.sent[1].Key) || !reflect.DeepEqual(writer.sent[0].Value, writer.sent[1].Value) || string(writer.sent[0].Key) != row.RequestKey() {
				t.Fatalf("unstable retry %+v", writer.sent)
			}
			want, _ := json.Marshal(row.Event())
			if string(writer.sent[0].Value) != string(want) {
				t.Fatalf("unexpected event %s", writer.sent[0].Value)
			}
		})
	}
}
