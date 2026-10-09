package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/internal/model"
)

func mentionMessage() *model.Message {
	return &model.Message{ID: 9, MsgID: "mention-client-id", FromID: 1, SenderType: model.MessageSenderUser, ToID: 300, ChatType: 2, ContentType: 1, Content: "hello @member"}
}

func TestCreateWithMentionsOneTransactionAndRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		repo, mock := testMessageRepo(t)
		mock.ExpectBegin()
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `messages`")).WillReturnResult(sqlmock.NewResult(9, 1))
		insert := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `im_group_message_mentions`"))
		if fail {
			insert.WillReturnError(errors.New("relation unavailable"))
			mock.ExpectRollback()
		} else {
			insert.WillReturnResult(sqlmock.NewResult(0, 2))
			mock.ExpectCommit()
		}
		err := repo.CreateWithMentions(context.Background(), mentionMessage(), 300, []int64{2, 3})
		if (err != nil) != fail {
			t.Fatalf("fail=%v err=%v", fail, err)
		}
	}
	repo, mock := testMessageRepo(t)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `messages`")).WillReturnResult(sqlmock.NewResult(9, 1))
	mock.ExpectCommit()
	if err := repo.CreateWithMentions(context.Background(), mentionMessage(), 300, nil); err != nil {
		t.Fatalf("empty relation set: %v", err)
	}
}

func TestCreateWithMentionsReplayRequiresExactSet(t *testing.T) {
	for _, tc := range []struct {
		name      string
		existing  []int64
		requested []int64
		content   string
		wantErr   bool
	}{
		{"same set different order", []int64{3, 2}, []int64{2, 3}, "hello @member", false},
		{"changed target", []int64{2, 4}, []int64{2, 3}, "hello @member", true},
		{"lost target", []int64{2}, []int64{2, 3}, "hello @member", true},
		{"empty to mentioned", nil, []int64{2, 3}, "hello @member", true},
		{"mentioned to empty", []int64{2, 3}, nil, "hello @member", true},
		{"empty to empty", nil, nil, "hello @member", false},
		{"changed content", []int64{2, 3}, []int64{2, 3}, "other", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, mock := testMessageRepo(t)
			mock.ExpectBegin()
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `messages`")).WillReturnError(&mysqldriver.MySQLError{Number: 1062})
			mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `messages` WHERE msg_id = ? LIMIT ?")).WithArgs("mention-client-id", 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "msg_id", "from_id", "sender_type", "initiator_id", "to_id", "chat_type", "content_type", "content"}).AddRow(7, "mention-client-id", 1, 1, 0, 300, 2, 1, tc.content))
			if tc.content == "hello @member" {
				rows := sqlmock.NewRows([]string{"message_id", "group_id", "mentioned_user_id"})
				for _, id := range tc.existing {
					rows.AddRow(7, 300, id)
				}
				mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `im_group_message_mentions` WHERE message_id = ?")).WithArgs(7).WillReturnRows(rows)
			}
			if tc.wantErr {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}
			msg := mentionMessage()
			err := repo.CreateWithMentions(context.Background(), msg, 300, tc.requested)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v expected conflict=%v", err, tc.wantErr)
			}
			if !tc.wantErr && msg.ID != 7 {
				t.Fatalf("replay ID=%d", msg.ID)
			}
		})
	}
}

func TestCreateWithMentionsRejectsMalformedSetBeforeDatabase(t *testing.T) {
	repo, _ := testMessageRepo(t)
	for _, ids := range [][]int64{{2, 2}, {0}, {-1}} {
		if err := repo.CreateWithMentions(context.Background(), mentionMessage(), 300, ids); err == nil {
			t.Fatalf("accepted %v", ids)
		}
	}
	msg := mentionMessage()
	msg.ContentType = 2
	if err := repo.CreateWithMentions(context.Background(), msg, 300, []int64{2}); err == nil {
		t.Fatal("accepted non-text mention")
	}
}

func TestAgentTriggerWithMentionsKeepsOutboxInOuterTransaction(t *testing.T) {
	for _, ids := range [][]int64{nil, {2}} {
		repo, mock := testMessageRepo(t)
		msg := triggerTestMessage()
		msg.ID = triggerTestIntent().MessageID
		msg.SenderType = model.MessageSenderUser
		msg.MentionedUserIDs = ids
		mock.ExpectBegin()
		mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT")).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `messages`")).WillReturnResult(sqlmock.NewResult(msg.ID, 1))
		expectTriggerSource(mock, persistedTriggerMessage())
		expectTriggerGroup(mock, sqlmock.NewRows([]string{"team_id"}).AddRow(200))
		mock.ExpectExec(regexp.QuoteMeta(insertAgentTrigger)).WithArgs(triggerTestValues(triggerTestIntent())...).WillReturnResult(sqlmock.NewResult(0, 1))
		if len(ids) > 0 {
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `im_group_message_mentions`")).WillReturnResult(sqlmock.NewResult(0, 1))
		}
		mock.ExpectCommit()
		if err := NewAgentTriggerMessageRepository(repo.db).CreateWithMentions(context.Background(), &msg, 300, ids); err != nil {
			t.Fatal(err)
		}
	}
}
