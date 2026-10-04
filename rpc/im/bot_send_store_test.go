package main

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	driver "github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/internal/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func validBotIntent(t *testing.T) botSendIntent {
	t.Helper()
	content, err := model.EncodeTaskCreatedCard(9007199254740993, "已确认标题")
	if err != nil {
		t.Fatal(err)
	}
	return botSendIntent{RunID: 9, BotID: 7, InitiatorID: 42, TeamID: 200, GroupID: 300, Content: content}
}

func storedBotRecord(i botSendIntent) botSendRecord {
	msgID, _ := model.BotTaskItemMsgID(i.RunID, i.ItemIndex)
	return botSendRecord{MsgID: msgID, RunID: i.RunID, ItemIndex: i.ItemIndex, BotID: i.BotID, InitiatorID: i.InitiatorID,
		TeamID: i.TeamID, GroupID: i.GroupID, Content: i.Content, TimestampUnixMs: 1790000000000}
}

func botSendRows(r botSendRecord) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"msg_id", "run_id", "item_index", "bot_id", "initiator_id", "team_id", "group_id", "content", "timestamp_unix_ms", "accepted"}).
		AddRow(r.MsgID, r.RunID, r.ItemIndex, r.BotID, r.InitiatorID, r.TeamID, r.GroupID, r.Content, r.TimestampUnixMs, r.Accepted)
}

func TestBotSendStoreCommitsIntentBeforePublishing(t *testing.T) {
	im, mock := testIMServer(t)
	i := validBotIntent(t)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `im_bot_sends`")).WithArgs(
		"bot-task:9", i.RunID, i.ItemIndex, i.BotID, i.InitiatorID, i.TeamID, i.GroupID, i.Content,
		sqlmock.AnyArg(), false, sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	r, err := (&mysqlBotSendStore{db: im.db}).Prepare(context.Background(), i)
	if err != nil || r.MsgID != "bot-task:9" || !r.sameIntent(i) || r.TimestampUnixMs <= 0 || r.Accepted {
		t.Fatalf("record=%+v err=%v", r, err)
	}
}

func TestBotSendStoreKeepsIntentAcrossRetriesAndRejectsChanges(t *testing.T) {
	for _, change := range []string{"none", "bot", "actor", "team", "group", "content"} {
		t.Run(change, func(t *testing.T) {
			im, mock := testIMServer(t)
			i := validBotIntent(t)
			stored := storedBotRecord(i)
			stored.Accepted = true
			switch change {
			case "bot":
				i.BotID++
			case "actor":
				i.InitiatorID++
			case "team":
				i.TeamID++
			case "group":
				i.GroupID++
			case "content":
				i.Content, _ = model.EncodeTaskCreatedCard(99, "另一个任务")
			}
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `im_bot_sends`")).WillReturnError(&driver.MySQLError{Number: 1062})
			mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `im_bot_sends` WHERE msg_id = ? LIMIT ?")).WithArgs("bot-task:9", 1).WillReturnRows(botSendRows(stored))
			r, err := (&mysqlBotSendStore{db: im.db}).Prepare(context.Background(), i)
			if change == "none" {
				if err != nil || !r.Accepted || r.TimestampUnixMs != stored.TimestampUnixMs || r.MsgID != stored.MsgID {
					t.Fatalf("retry changed result: %+v %v", r, err)
				}
			} else if status.Code(err) != codes.AlreadyExists {
				t.Fatalf("changed %s err=%v", change, err)
			}
		})
	}
}

func TestBotSendStoreRejectsInvalidIntentAndDatabaseFailure(t *testing.T) {
	im, mock := testIMServer(t)
	i := validBotIntent(t)
	bad := i
	bad.RunID = 0
	if _, err := (&mysqlBotSendStore{db: im.db}).Prepare(context.Background(), bad); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	bad = i
	bad.Content = "broken"
	if _, err := (&mysqlBotSendStore{db: im.db}).Prepare(context.Background(), bad); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `im_bot_sends`")).WillReturnError(errors.New("private DB detail"))
	if _, err := (&mysqlBotSendStore{db: im.db}).Prepare(context.Background(), i); status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
}

func TestBotSendStoreAcceptanceIsMonotonicAndMustBeSaved(t *testing.T) {
	for _, tc := range []struct {
		name     string
		affected int64
		saved    bool
		dbErr    bool
		want     codes.Code
	}{
		{"first acknowledgement", 1, true, false, codes.OK}, {"concurrent acknowledgement", 0, true, false, codes.OK},
		{"missing acknowledgement", 0, false, false, codes.Internal}, {"write failed", 0, false, true, codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			im, mock := testIMServer(t)
			q := mock.ExpectExec(regexp.QuoteMeta("UPDATE `im_bot_sends` SET `accepted`=?,`updated_at`=? WHERE msg_id = ?")).WithArgs(true, sqlmock.AnyArg(), "bot-task:9")
			if tc.dbErr {
				q.WillReturnError(errors.New("DB failed"))
			} else {
				q.WillReturnResult(sqlmock.NewResult(0, tc.affected))
				if tc.affected == 0 {
					mock.ExpectQuery(regexp.QuoteMeta("SELECT `accepted` FROM `im_bot_sends` WHERE msg_id = ? LIMIT ?")).WithArgs("bot-task:9", 1).
						WillReturnRows(sqlmock.NewRows([]string{"accepted"}).AddRow(tc.saved))
				}
			}
			if err := (&mysqlBotSendStore{db: im.db}).MarkAccepted(context.Background(), "bot-task:9"); status.Code(err) != tc.want {
				t.Fatal(err)
			}
		})
	}
}
