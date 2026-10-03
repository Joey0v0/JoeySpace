package repository

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	driver "github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/internal/model"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testMessageRepo(t *testing.T) (*messageRepository, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{
		SkipDefaultTransaction: true,
		Logger:                 logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	return &messageRepository{db: db}, mock
}

func TestCreateMessageReusesSameClientMessageID(t *testing.T) {
	for _, tc := range []struct {
		name       string
		oldContent string
		wantError  bool
	}{
		{"same message", "hello", false},
		{"same ID different content", "changed", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, mock := testMessageRepo(t)
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `messages`")).WillReturnError(&driver.MySQLError{Number: 1062, Message: "duplicate"})
			mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `messages` WHERE msg_id = ? LIMIT ?")).
				WithArgs("m1", 1).WillReturnRows(sqlmock.NewRows([]string{"id", "msg_id", "from_id", "to_id", "chat_type", "content_type", "content"}).
				AddRow(int64(7), "m1", int64(42), int64(100), 2, 1, tc.oldContent))
			msg := &model.Message{ID: 9, MsgID: "m1", FromID: 42, ToID: 100, ChatType: 2, ContentType: 1, Content: "hello"}
			err := repo.Create(context.Background(), msg)
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "different content") {
					t.Fatalf("expected conflicting content error, got %v", err)
				}
			} else if err != nil || msg.ID != 7 || msg.SenderType != model.MessageSenderUser {
				t.Fatalf("expected existing message ID 7, got ID=%d, err=%v", msg.ID, err)
			}
		})
	}
}

func TestCreateMessageDedupChecksSenderIdentity(t *testing.T) {
	for _, tc := range []struct {
		name         string
		sender       int8
		initiator    int64
		oldSender    int8
		oldInitiator int64
		wantConflict bool
	}{
		{"same bot and initiator", 2, 42, 2, 42, false},
		{"bot cannot reuse user message", 2, 42, 1, 0, true},
		{"user cannot reuse bot message", 1, 0, 2, 42, true},
		{"different bot initiator", 2, 43, 2, 42, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, mock := testMessageRepo(t)
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `messages`")).
				WillReturnError(&driver.MySQLError{Number: 1062, Message: "duplicate"})
			mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `messages` WHERE msg_id = ? LIMIT ?")).
				WithArgs("m1", 1).WillReturnRows(sqlmock.NewRows([]string{
				"id", "msg_id", "from_id", "sender_type", "initiator_id", "to_id", "chat_type", "content_type", "content",
			}).AddRow(7, "m1", 42, tc.oldSender, tc.oldInitiator, 100, 2, 1, "hello"))
			msg := &model.Message{ID: 9, MsgID: "m1", FromID: 42, SenderType: tc.sender,
				InitiatorID: tc.initiator, ToID: 100, ChatType: 2, ContentType: 1, Content: "hello"}
			err := repo.Create(context.Background(), msg)
			if tc.wantConflict {
				if err == nil || !strings.Contains(err.Error(), "different content") || msg.ID != 9 {
					t.Fatalf("identity conflict must not reuse stored result: msg=%+v err=%v", msg, err)
				}
			} else if err != nil || msg.ID != 7 {
				t.Fatalf("same identity must reuse stored result: msg=%+v err=%v", msg, err)
			}
		})
	}
}

func TestCreateMessageRejectsInvalidSenderBeforeWriting(t *testing.T) {
	for _, msg := range []*model.Message{
		nil,
		{SenderType: 3},
		{SenderType: model.MessageSenderUser, InitiatorID: 42},
		{SenderType: model.MessageSenderBot, FromID: 9, ToID: 100, ChatType: 2},
		{SenderType: model.MessageSenderBot, InitiatorID: 42, ToID: 100, ChatType: 2},
		{SenderType: model.MessageSenderBot, FromID: 9, InitiatorID: 42, ToID: 100, ChatType: 1},
		{SenderType: model.MessageSenderBot, FromID: 9, InitiatorID: 42, ChatType: 2},
	} {
		// No database: reaching persistence instead of validating would fail the test.
		repo := &messageRepository{}
		if err := repo.Create(context.Background(), msg); err == nil {
			t.Fatalf("invalid identity accepted: %+v", msg)
		}
	}
}

func TestCreateTaskCardRejectsInvalidContentBeforeWriting(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sender   int8
		chatType int8
		content  string
	}{
		{"user cannot write task card", 1, 2, `{"version":1,"task_id":"9","title":"ok"}`},
		{"legacy user cannot write task card", 0, 2, `{"version":1,"task_id":"9","title":"ok"}`},
		{"bot cannot write private task card", 2, 1, `{"version":1,"task_id":"9","title":"ok"}`},
		{"invalid content", 2, 2, `not JSON`},
		{"unsupported version", 2, 2, `{"version":2,"task_id":"9","title":"ok"}`},
		{"numeric task ID", 2, 2, `{"version":1,"task_id":9007199254740993,"title":"ok"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := &model.Message{FromID: 1, ToID: 100, SenderType: tc.sender, ChatType: tc.chatType,
				ContentType: model.MessageContentTaskCard, Content: tc.content}
			if tc.sender == model.MessageSenderBot {
				msg.InitiatorID = 42
			}
			// No database: validation must happen before persistence.
			if err := (&messageRepository{}).Create(context.Background(), msg); err == nil {
				t.Fatalf("invalid card reached persistence: %+v", msg)
			}
		})
	}
}

func TestCreateTaskCardDedupKeepsFrozenResult(t *testing.T) {
	content, err := model.EncodeTaskCreatedCard(9007199254740993, "已确认标题")
	if err != nil {
		t.Fatal(err)
	}
	for _, sameContent := range []bool{true, false} {
		t.Run(map[bool]string{true: "same result", false: "different task"}[sameContent], func(t *testing.T) {
			repo, mock := testMessageRepo(t)
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `messages`")).
				WillReturnError(&driver.MySQLError{Number: 1062, Message: "duplicate"})
			stored := content
			if !sameContent {
				stored, _ = model.EncodeTaskCreatedCard(9007199254740995, "已确认标题")
			}
			mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `messages` WHERE msg_id = ? LIMIT ?")).
				WithArgs("fixed-card", 1).WillReturnRows(sqlmock.NewRows([]string{
				"id", "msg_id", "from_id", "sender_type", "initiator_id", "to_id", "chat_type", "content_type", "content",
			}).AddRow(7, "fixed-card", 1, 2, 42, 100, 2, 4, stored))
			msg := &model.Message{ID: 9, MsgID: "fixed-card", FromID: 1, SenderType: 2,
				InitiatorID: 42, ToID: 100, ChatType: 2, ContentType: model.MessageContentTaskCard, Content: content}
			err := repo.Create(context.Background(), msg)
			if sameContent && (err != nil || msg.ID != 7) {
				t.Fatalf("retry must reuse the stored card: %+v err=%v", msg, err)
			}
			if !sameContent && (err == nil || !strings.Contains(err.Error(), "different content")) {
				t.Fatalf("different task must conflict: %+v err=%v", msg, err)
			}
		})
	}
}

func TestCreateOfflineReusesExistingUserMessagePair(t *testing.T) {
	repo, mock := testMessageRepo(t)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `offline_messages`")).
		WillReturnError(&driver.MySQLError{Number: 1062, Message: "duplicate"})
	mock.ExpectQuery(regexp.QuoteMeta("SELECT `id` FROM `offline_messages` WHERE user_id = ? AND message_id = ? LIMIT ?")).
		WithArgs(int64(42), int64(7), 1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(11)))
	offline := &model.OfflineMessage{UserID: 42, MessageID: 7}
	if err := repo.CreateOffline(context.Background(), offline); err != nil || offline.ID != 11 {
		t.Fatalf("expected existing offline ID 11, got ID=%d, err=%v", offline.ID, err)
	}
}

func TestCreateOfflineDoesNotHideOtherDatabaseErrors(t *testing.T) {
	repo, mock := testMessageRepo(t)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `offline_messages`")).
		WillReturnError(&driver.MySQLError{Number: 2006, Message: "server gone"})
	err := repo.CreateOffline(context.Background(), &model.OfflineMessage{UserID: 42, MessageID: 7})
	if err == nil || !strings.Contains(err.Error(), "server gone") {
		t.Fatalf("expected database error, got %v", err)
	}
}

func TestDeleteOfflineOnlyRemovesFetchedMessageIDs(t *testing.T) {
	repo, mock := testMessageRepo(t)
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM `offline_messages` WHERE user_id = ? AND message_id IN (?,?)")).
		WithArgs(int64(42), int64(7), int64(8)).WillReturnResult(sqlmock.NewResult(0, 2))
	if err := repo.DeleteOffline(context.Background(), 42, []int64{7, 8}); err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM `offline_messages` WHERE user_id = ? AND message_id IN (?)")).
		WithArgs(int64(42), int64(7)).WillReturnResult(sqlmock.NewResult(0, 0))
	if err := repo.DeleteOffline(context.Background(), 42, []int64{7}); err != nil {
		t.Fatalf("repeated acknowledgement: %v", err)
	}
	if err := repo.DeleteOffline(context.Background(), 42, nil); err != nil {
		t.Fatal(err)
	}
}
