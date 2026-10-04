package repository

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/internal/model"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var triggerTestColumns = []string{"message_id", "action", "event_version", "msg_id", "actor_id", "team_id", "group_id", "instruction", "reference_time_ms", "published"}

func triggerTestMessage() model.Message {
	return model.Message{MsgID: "trigger-client-id", FromID: 42, ToID: 300, ChatType: 2, ContentType: 1,
		Content: "@AI 整理任务 明天下午修复缓存", CreatedAt: time.Unix(1791043200, 987654321).UTC()}
}

func triggerTestIntent() model.AgentTriggerOutbox {
	return model.AgentTriggerOutbox{MessageID: 9007199254740993, Action: model.AgentTriggerAction, EventVersion: 1,
		MsgID: "trigger-client-id", ActorID: 42, TeamID: 200, GroupID: 300, Instruction: "明天下午修复缓存", ReferenceTimeMS: 1791043200000}
}

func triggerTestValues(r model.AgentTriggerOutbox) []driver.Value {
	return []driver.Value{r.MessageID, r.Action, int64(r.EventVersion), r.MsgID, r.ActorID, r.TeamID, r.GroupID, r.Instruction, r.ReferenceTimeMS, r.Published}
}

func triggerTestRows(records ...model.AgentTriggerOutbox) *sqlmock.Rows {
	rows := sqlmock.NewRows(triggerTestColumns)
	for _, r := range records {
		rows.AddRow(triggerTestValues(r)...)
	}
	return rows
}

func triggerSourceRows(msg model.Message) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "msg_id", "from_id", "sender_type", "initiator_id", "to_id", "chat_type", "content_type", "content", "created_at"}).
		AddRow(msg.ID, msg.MsgID, msg.FromID, msg.SenderType, msg.InitiatorID, msg.ToID, msg.ChatType, msg.ContentType, msg.Content, msg.CreatedAt)
}

func expectTriggerSource(mock sqlmock.Sqlmock, source model.Message) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `messages` WHERE id = ? LIMIT ?")).WithArgs(source.ID, 1).WillReturnRows(triggerSourceRows(source))
}

func expectTriggerGroup(mock sqlmock.Sqlmock, rows *sqlmock.Rows) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT `team_id` FROM `groups` WHERE id = ? LIMIT ?")).WithArgs(int64(300), 1).WillReturnRows(rows)
}

func persistedTriggerMessage() model.Message {
	msg := triggerTestMessage()
	msg.ID, msg.SenderType, msg.CreatedAt = triggerTestIntent().MessageID, model.MessageSenderUser, time.Unix(1791043200, 0).UTC()
	return msg
}

func expectTriggerInsertSource(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `messages`")).WillReturnResult(sqlmock.NewResult(triggerTestIntent().MessageID, 1))
	expectTriggerSource(mock, persistedTriggerMessage())
}

func TestAgentTriggerMessageCommitsSourceAndFixedSecondPrecisionIntent(t *testing.T) {
	repo, mock := testMessageRepo(t)
	expectTriggerInsertSource(mock)
	expectTriggerGroup(mock, sqlmock.NewRows([]string{"team_id"}).AddRow(200))
	mock.ExpectExec(regexp.QuoteMeta(insertAgentTrigger)).WithArgs(triggerTestValues(triggerTestIntent())...).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	msg := triggerTestMessage()
	if err := NewAgentTriggerMessageRepository(repo.db).Create(context.Background(), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.ID != triggerTestIntent().MessageID || msg.CreatedAt.UnixMilli() != 1791043200000 || msg.CreatedAt.Nanosecond() != 0 || msg.SenderType != model.MessageSenderUser {
		t.Fatalf("source identity or database time changed: %+v", msg)
	}
}

func TestAgentTriggerMessageUsesOneOuterTransactionWithDefaultGORMCreate(t *testing.T) {
	repo, mock := testMessageRepo(t)
	sqlDB, err := repo.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if db.Config.SkipDefaultTransaction {
		t.Fatal("test must retain default Create transactions")
	}
	expectTriggerInsertSource(mock)
	expectTriggerGroup(mock, sqlmock.NewRows([]string{"team_id"}).AddRow(200))
	mock.ExpectExec(regexp.QuoteMeta(insertAgentTrigger)).WithArgs(triggerTestValues(triggerTestIntent())...).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	msg := triggerTestMessage()
	if err := NewAgentTriggerMessageRepository(db).Create(context.Background(), &msg); err != nil || msg.CreatedAt.UnixMilli() != triggerTestIntent().ReferenceTimeMS {
		t.Fatalf("default transaction=%+v %v", msg, err)
	}
}

func TestAgentTriggerMessageFiltersOrdinaryMessagesToOriginalCreate(t *testing.T) {
	for _, name := range []string{"ordinary", "quoted", "private", "image", "bot", "bad command"} {
		t.Run(name, func(t *testing.T) {
			repo, mock := testMessageRepo(t)
			msg := triggerTestMessage()
			switch name {
			case "ordinary":
				msg.Content = "普通聊天"
			case "quoted":
				msg.Content = "引用 @AI 整理任务 修复"
			case "private":
				msg.ChatType = 1
			case "image":
				msg.ContentType = 2
			case "bot":
				msg.SenderType, msg.InitiatorID = model.MessageSenderBot, 42
			case "bad command":
				msg.Content = "@AI 整理任务"
			}
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `messages`")).WillReturnResult(sqlmock.NewResult(7, 1))
			if err := NewAgentTriggerMessageRepository(repo.db).Create(context.Background(), &msg); err != nil || msg.ID != 7 {
				t.Fatalf("ordinary create=%+v,%v", msg, err)
			}
		})
	}
}

func TestAgentTriggerMessageMissingOrLegacyGroupDoesNotCreateEvent(t *testing.T) {
	for _, name := range []string{"missing", "legacy", "zero team"} {
		t.Run(name, func(t *testing.T) {
			repo, mock := testMessageRepo(t)
			expectTriggerInsertSource(mock)
			rows := sqlmock.NewRows([]string{"team_id"})
			if name == "legacy" {
				rows.AddRow(nil)
			} else if name == "zero team" {
				rows.AddRow(0)
			}
			expectTriggerGroup(mock, rows)
			mock.ExpectCommit()
			msg := triggerTestMessage()
			if err := NewAgentTriggerMessageRepository(repo.db).Create(context.Background(), &msg); err != nil || msg.ID != triggerTestIntent().MessageID {
				t.Fatalf("non-team message=%+v,%v", msg, err)
			}
		})
	}
}

func TestAgentTriggerMessageRollsBackAtomicFailuresAndDoesNotPublishCallerIdentity(t *testing.T) {
	for _, name := range []string{"source insert", "source read", "source mismatch", "invalid source time", "group query", "outbox SQL", "zero rows", "two rows", "commit"} {
		t.Run(name, func(t *testing.T) {
			repo, mock := testMessageRepo(t)
			mock.ExpectBegin()
			insert := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `messages`"))
			if name == "source insert" {
				insert.WillReturnError(errors.New("private SQL"))
			} else {
				insert.WillReturnResult(sqlmock.NewResult(triggerTestIntent().MessageID, 1))
				source := persistedTriggerMessage()
				switch name {
				case "source read":
					mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `messages` WHERE id = ? LIMIT ?")).WillReturnError(errors.New("private SQL"))
				case "source mismatch":
					source.FromID++
					expectTriggerSource(mock, source)
				default:
					if name == "invalid source time" {
						source.CreatedAt = time.Time{}
					}
					expectTriggerSource(mock, source)
					if name == "group query" {
						mock.ExpectQuery(regexp.QuoteMeta("SELECT `team_id` FROM `groups` WHERE id = ? LIMIT ?")).WillReturnError(errors.New("private SQL"))
					} else {
						expectTriggerGroup(mock, sqlmock.NewRows([]string{"team_id"}).AddRow(200))
						if name != "invalid source time" {
							exec := mock.ExpectExec(regexp.QuoteMeta(insertAgentTrigger))
							if name == "outbox SQL" {
								exec.WillReturnError(errors.New("private SQL"))
							} else {
								rows := int64(1)
								if name == "zero rows" {
									rows = 0
								} else if name == "two rows" {
									rows = 2
								}
								exec.WillReturnResult(sqlmock.NewResult(0, rows))
							}
						}
					}
				}
			}
			if name == "commit" {
				mock.ExpectCommit().WillReturnError(errors.New("private commit"))
			} else {
				mock.ExpectRollback()
			}
			msg := triggerTestMessage()
			original := msg
			if err := NewAgentTriggerMessageRepository(repo.db).Create(context.Background(), &msg); err == nil || msg != original {
				t.Fatalf("failed transaction changed caller message: %+v %v", msg, err)
			}
		})
	}
}

func TestAgentTriggerReplayReusesOriginalMessageIDTimeAndCompleteIntent(t *testing.T) {
	for _, name := range []string{"same pending", "same published", "action", "version", "message", "actor", "team", "group", "instruction", "reference", "missing", "multiple"} {
		t.Run(name, func(t *testing.T) {
			repo, mock := testMessageRepo(t)
			mock.ExpectBegin()
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `messages`")).WillReturnError(&mysqldriver.MySQLError{Number: 1062})
			mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `messages` WHERE msg_id = ? LIMIT ?")).WithArgs("trigger-client-id", 1).WillReturnRows(triggerSourceRows(persistedTriggerMessage()))
			expectTriggerSource(mock, persistedTriggerMessage())
			expectTriggerGroup(mock, sqlmock.NewRows([]string{"team_id"}).AddRow(200))
			mock.ExpectExec(regexp.QuoteMeta(insertAgentTrigger)).WithArgs(triggerTestValues(triggerTestIntent())...).WillReturnError(&mysqldriver.MySQLError{Number: 1062})
			r := triggerTestIntent()
			switch name {
			case "same published":
				r.Published = true
			case "action":
				r.Action = "other"
			case "version":
				r.EventVersion = 2
			case "message":
				r.MsgID = "other"
			case "actor":
				r.ActorID++
			case "team":
				r.TeamID++
			case "group":
				r.GroupID++
			case "instruction":
				r.Instruction = "different"
			case "reference":
				r.ReferenceTimeMS += 1000
			}
			rows := triggerTestRows(r)
			if name == "missing" {
				rows = triggerTestRows()
			} else if name == "multiple" {
				rows = triggerTestRows(r, r)
			}
			mock.ExpectQuery(regexp.QuoteMeta(selectAgentTrigger + " FOR UPDATE")).WithArgs(r.MessageID).WillReturnRows(rows)
			ok := name == "same pending" || name == "same published"
			if ok {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			msg := triggerTestMessage()
			msg.CreatedAt = time.Unix(1791129600, 111).UTC() // A redelivery must not use this later time.
			err := NewAgentTriggerMessageRepository(repo.db).Create(context.Background(), &msg)
			if (err == nil) != ok || ok && (msg.ID != r.MessageID || msg.CreatedAt.UnixMilli() != triggerTestIntent().ReferenceTimeMS) {
				t.Fatalf("replay=%+v %v", msg, err)
			}
		})
	}
}

func TestAgentTriggerMessageRejectsReusedMessageIDWithDifferentOriginalFacts(t *testing.T) {
	for _, name := range []string{"content", "author", "bot identity"} {
		t.Run(name, func(t *testing.T) {
			repo, mock := testMessageRepo(t)
			source := persistedTriggerMessage()
			switch name {
			case "content":
				source.Content = "@AI 整理任务 原始不同指令"
			case "author":
				source.FromID++
			case "bot identity":
				source.SenderType, source.InitiatorID = model.MessageSenderBot, 42
			}
			mock.ExpectBegin()
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `messages`")).WillReturnError(&mysqldriver.MySQLError{Number: 1062})
			mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `messages` WHERE msg_id = ? LIMIT ?")).WithArgs("trigger-client-id", 1).WillReturnRows(triggerSourceRows(source))
			mock.ExpectRollback()
			msg := triggerTestMessage()
			original := msg
			if err := NewAgentTriggerMessageRepository(repo.db).Create(context.Background(), &msg); err == nil || msg != original {
				t.Fatalf("conflicting message changed intent: %+v %v", msg, err)
			}
		})
	}
}

func TestAgentTriggerPendingListValidatesBoundsAndEveryRecord(t *testing.T) {
	for _, limit := range []int{0, -1, 101} {
		repo, _ := testMessageRepo(t)
		if rows, err := NewAgentTriggerStore(repo.db).ListPending(context.Background(), limit); err == nil || rows != nil {
			t.Fatalf("invalid limit=%d", limit)
		}
	}
	for _, name := range []string{"valid", "empty", "published", "bad action", "bad time", "wrong order", "too many", "NULL", "SQL"} {
		t.Run(name, func(t *testing.T) {
			repo, mock := testMessageRepo(t)
			r, next := triggerTestIntent(), triggerTestIntent()
			next.MessageID++
			rows := triggerTestRows(r, next)
			switch name {
			case "empty":
				rows = triggerTestRows()
			case "published":
				r.Published = true
				rows = triggerTestRows(r)
			case "bad action":
				r.Action = "other"
				rows = triggerTestRows(r)
			case "bad time":
				r.ReferenceTimeMS = 0
				rows = triggerTestRows(r)
			case "wrong order":
				rows = triggerTestRows(next, r)
			case "too many":
				rows = triggerTestRows(r, next, next)
			case "NULL":
				values := triggerTestValues(r)
				values[9] = nil
				rows = sqlmock.NewRows(triggerTestColumns).AddRow(values...)
			}
			query := mock.ExpectQuery(regexp.QuoteMeta(selectPendingAgentTriggers)).WithArgs(2)
			if name == "SQL" {
				query.WillReturnError(errors.New("private SQL"))
			} else {
				query.WillReturnRows(rows)
			}
			got, err := NewAgentTriggerStore(repo.db).ListPending(context.Background(), 2)
			ok := name == "valid" || name == "empty"
			if (err == nil) != ok || !ok && got != nil || name == "valid" && (len(got) != 2 || got[0] != r || got[1] != next) {
				t.Fatalf("list=%+v %v", got, err)
			}
		})
	}
}

func TestAgentTriggerMarkPublishedLocksFullFactsAndSupportsSameFactReplay(t *testing.T) {
	for _, name := range []string{"success", "already published", "missing", "different actor", "different text", "different time", "multiple", "zero rows", "two rows", "SQL", "commit"} {
		t.Run(name, func(t *testing.T) {
			repo, mock := testMessageRepo(t)
			expected, saved := triggerTestIntent(), triggerTestIntent()
			mock.ExpectBegin()
			switch name {
			case "already published":
				saved.Published = true
			case "different actor":
				saved.ActorID++
			case "different text":
				saved.Instruction = "different"
			case "different time":
				saved.ReferenceTimeMS++
			}
			rows := triggerTestRows(saved)
			if name == "missing" {
				rows = triggerTestRows()
			} else if name == "multiple" {
				rows = triggerTestRows(saved, saved)
			}
			mock.ExpectQuery(regexp.QuoteMeta(selectAgentTrigger + " FOR UPDATE")).WithArgs(expected.MessageID).WillReturnRows(rows)
			if name == "success" || name == "zero rows" || name == "two rows" || name == "SQL" || name == "commit" {
				exec := mock.ExpectExec(regexp.QuoteMeta(publishAgentTrigger)).WithArgs(expected.MessageID)
				if name == "SQL" {
					exec.WillReturnError(errors.New("private SQL"))
				} else {
					count := int64(1)
					if name == "zero rows" {
						count = 0
					} else if name == "two rows" {
						count = 2
					}
					exec.WillReturnResult(sqlmock.NewResult(0, count))
				}
			}
			ok := name == "success" || name == "already published"
			if name == "commit" {
				mock.ExpectCommit().WillReturnError(errors.New("private commit"))
			} else if ok {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			if err := NewAgentTriggerStore(repo.db).MarkPublished(context.Background(), expected); (err == nil) != ok {
				t.Fatalf("mark=%v", err)
			}
		})
	}
	repo, _ := testMessageRepo(t)
	invalid := triggerTestIntent()
	invalid.MessageID = 0
	if err := NewAgentTriggerStore(repo.db).MarkPublished(context.Background(), invalid); err == nil {
		t.Fatal("invalid publication input accepted")
	}
}
