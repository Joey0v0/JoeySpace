package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	driver "github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/internal/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func expectBotItemSendInsert(mock sqlmock.Sqlmock, i botSendIntent) *sqlmock.ExpectedExec {
	id, _ := model.BotTaskItemMsgID(i.RunID, i.ItemIndex)
	return mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `im_bot_sends`")).WithArgs(
		id, i.RunID, i.ItemIndex, i.BotID, i.InitiatorID, i.TeamID, i.GroupID, i.Content,
		sqlmock.AnyArg(), false, sqlmock.AnyArg(), sqlmock.AnyArg())
}

func TestBotItemStoreKeepsEachIdentityAndAcceptedReplay(t *testing.T) {
	im, mock := testIMServer(t)
	store := &mysqlBotSendStore{db: im.db}
	for _, index := range []int32{0, 1, 4} {
		i := validBotIntent(t)
		i.ItemIndex = index
		id, _ := model.BotTaskItemMsgID(i.RunID, index)
		expectBotItemSendInsert(mock, i).WillReturnResult(sqlmock.NewResult(0, 1))
		r, err := store.Prepare(context.Background(), i)
		if err != nil || r.MsgID != id || r.ItemIndex != index || !r.sameIntent(i) || r.Accepted || r.TimestampUnixMs <= 0 {
			t.Fatalf("index %d insert: %+v %v", index, r, err)
		}
		// A duplicate must use its original event time and acceptance state.
		stored := storedBotRecord(i)
		stored.Accepted = true
		expectBotItemSendInsert(mock, i).WillReturnError(&driver.MySQLError{Number: 1062})
		mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `im_bot_sends` WHERE msg_id = ? LIMIT ?")).
			WithArgs(id, 1).WillReturnRows(botSendRows(stored))
		r, err = store.Prepare(context.Background(), i)
		if err != nil || !r.Accepted || r.MsgID != id || r.TimestampUnixMs != stored.TimestampUnixMs || !r.sameIntent(i) {
			t.Fatalf("index %d replay: %+v %v", index, r, err)
		}
	}
}

func TestBotItemStoreRejectsIdentityAndDuplicateRecordChanges(t *testing.T) {
	for _, index := range []int32{0, 1, 4} {
		for _, change := range []string{"msg_id", "run", "index", "bot", "actor", "team", "group", "content", "time"} {
			t.Run(strconv.Itoa(int(index))+"/"+change, func(t *testing.T) {
				im, mock := testIMServer(t)
				i := validBotIntent(t)
				i.ItemIndex = index
				stored := storedBotRecord(i)
				stored.Accepted = true
				want := codes.AlreadyExists
				switch change {
				case "msg_id":
					stored.MsgID += ":other"
				case "run":
					stored.RunID++
				case "index":
					stored.ItemIndex = (index + 1) % 5
				case "bot":
					stored.BotID++
				case "actor":
					stored.InitiatorID++
				case "team":
					stored.TeamID++
				case "group":
					stored.GroupID++
				case "content":
					stored.Content, _ = model.EncodeTaskCreatedCard(99, "另一个任务")
				case "time":
					stored.TimestampUnixMs = 0
					want = codes.Internal
				}
				id, _ := model.BotTaskItemMsgID(i.RunID, index)
				expectBotItemSendInsert(mock, i).WillReturnError(&driver.MySQLError{Number: 1062})
				mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `im_bot_sends` WHERE msg_id = ? LIMIT ?")).
					WithArgs(id, 1).WillReturnRows(botSendRows(stored))
				if r, err := (&mysqlBotSendStore{db: im.db}).Prepare(context.Background(), i); status.Code(err) != want || r.MsgID != "" {
					t.Fatalf("invalid stored record accepted: %+v %v", r, err)
				}
			})
		}
	}
}

func TestBotItemStoreRejectsOutOfRangeIndexBeforeSQL(t *testing.T) {
	im, _ := testIMServer(t)
	store := &mysqlBotSendStore{db: im.db}
	for _, index := range []int32{-1, 5, 2147483647} {
		i := validBotIntent(t)
		i.ItemIndex = index
		if _, err := store.Prepare(context.Background(), i); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("index %d: %v", index, err)
		}
	}
}

type itemBotSendMemory struct {
	records map[string]botSendRecord
	markErr map[string]error
}

func (s *itemBotSendMemory) Prepare(_ context.Context, i botSendIntent) (botSendRecord, error) {
	id, err := model.BotTaskItemMsgID(i.RunID, i.ItemIndex)
	if err != nil {
		return botSendRecord{}, status.Error(codes.InvalidArgument, "invalid item")
	}
	if r, ok := s.records[id]; ok {
		if !r.sameIntent(i) {
			return botSendRecord{}, status.Error(codes.AlreadyExists, "different intent")
		}
		return r, nil
	}
	r := storedBotRecord(i)
	s.records[id] = r
	return r, nil
}

func (s *itemBotSendMemory) MarkAccepted(_ context.Context, id string) error {
	if err := s.markErr[id]; err != nil {
		return err
	}
	r := s.records[id]
	r.Accepted = true
	s.records[id] = r
	return nil
}

func TestBotItemPublisherKeepsOtherItemsIndependent(t *testing.T) {
	store := &itemBotSendMemory{records: make(map[string]botSendRecord), markErr: make(map[string]error)}
	writer := &botWriterStub{}
	p := &kafkaBotPublisher{store: store, writer: writer}
	i := validBotIntent(t)
	if id, err := p.Publish(context.Background(), i); err != nil || id != "bot-task:9" {
		t.Fatalf("legacy item: %s %v", id, err)
	}
	old := store.records["bot-task:9"]
	i.ItemIndex = 1
	store.markErr["bot-task:9:1"] = status.Error(codes.Unavailable, "save failed")
	if id, err := p.Publish(context.Background(), i); id != "" || status.Code(err) != codes.Unavailable || store.records["bot-task:9:1"].Accepted {
		t.Fatalf("failed item incorrectly accepted: %s %v", id, err)
	}
	i.ItemIndex = 4
	if id, err := p.Publish(context.Background(), i); err != nil || id != "bot-task:9:4" {
		t.Fatalf("independent item: %s %v", id, err)
	}
	fourth := store.records["bot-task:9:4"]
	i.ItemIndex = 1
	delete(store.markErr, "bot-task:9:1")
	if id, err := p.Publish(context.Background(), i); err != nil || id != "bot-task:9:1" || len(writer.messages) != 4 ||
		!bytes.Equal(writer.messages[1].Value, writer.messages[3].Value) || !bytes.Equal(writer.messages[1].Key, writer.messages[3].Key) {
		t.Fatalf("item retry changed event: %s %v", id, err)
	}
	if store.records["bot-task:9"] != old || store.records["bot-task:9:4"] != fourth {
		t.Fatal("retry changed another item's record")
	}
	for _, index := range []int32{0, 1, 4} {
		i.ItemIndex = index
		if _, err := p.Publish(context.Background(), i); err != nil || len(writer.messages) != 4 {
			t.Fatalf("accepted item %d republished: %v", index, err)
		}
	}
	for _, change := range []string{"bot", "actor", "team", "group", "content"} {
		changed := i
		changed.ItemIndex = 1
		switch change {
		case "bot":
			changed.BotID++
		case "actor":
			changed.InitiatorID++
		case "team":
			changed.TeamID++
		case "group":
			changed.GroupID++
		case "content":
			changed.Content, _ = model.EncodeTaskCreatedCard(99, "换正文")
		}
		if _, err := p.Publish(context.Background(), changed); status.Code(err) != codes.AlreadyExists || len(writer.messages) != 4 {
			t.Fatalf("changed %s published: %v", change, err)
		}
	}
}

func TestBotItemPublisherSQLRetriesFixedEventAfterUncertainResult(t *testing.T) {
	for _, index := range []int32{0, 1, 4} {
		for _, failure := range []string{"broker acknowledgement", "acceptance persistence", "acceptance saved but response lost"} {
			t.Run(strconv.Itoa(int(index))+"/"+failure, func(t *testing.T) {
				im, mock := testIMServer(t)
				i := validBotIntent(t)
				i.ItemIndex = index
				id, _ := model.BotTaskItemMsgID(i.RunID, index)
				writer := &botWriterStub{}
				store := &mysqlBotSendStore{db: im.db}
				p := &kafkaBotPublisher{store: store, writer: writer}
				expectBotItemSendInsert(mock, i).WillReturnResult(sqlmock.NewResult(0, 1))
				if failure == "broker acknowledgement" {
					writer.err = errors.New("ack lost")
				} else {
					mock.ExpectExec(regexp.QuoteMeta("UPDATE `im_bot_sends` SET `accepted`=?,`updated_at`=? WHERE msg_id = ?")).
						WithArgs(true, sqlmock.AnyArg(), id).WillReturnError(errors.New("save failed"))
				}
				if got, err := p.Publish(context.Background(), i); got != "" || status.Code(err) != codes.Unavailable || len(writer.messages) != 1 {
					t.Fatalf("uncertain result: %s %v", got, err)
				}
				var first model.ChatEvent
				if err := json.Unmarshal(writer.messages[0].Value, &first); err != nil || first.MsgID != id || first.Timestamp <= 0 {
					t.Fatalf("first event: %+v %v", first, err)
				}
				stored := storedBotRecord(i)
				stored.TimestampUnixMs = first.Timestamp
				stored.Accepted = failure == "acceptance saved but response lost"
				expectBotItemSendInsert(mock, i).WillReturnError(&driver.MySQLError{Number: 1062})
				mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `im_bot_sends` WHERE msg_id = ? LIMIT ?")).
					WithArgs(id, 1).WillReturnRows(botSendRows(stored))
				wantWrites := 1
				if !stored.Accepted {
					wantWrites = 2
					mock.ExpectExec(regexp.QuoteMeta("UPDATE `im_bot_sends` SET `accepted`=?,`updated_at`=? WHERE msg_id = ?")).
						WithArgs(true, sqlmock.AnyArg(), id).WillReturnResult(sqlmock.NewResult(0, 1))
				}
				writer.err = nil
				p = &kafkaBotPublisher{store: &mysqlBotSendStore{db: im.db}, writer: writer}
				if got, err := p.Publish(context.Background(), i); err != nil || got != id || len(writer.messages) != wantWrites ||
					!bytes.Equal(writer.messages[0].Value, writer.messages[wantWrites-1].Value) || !bytes.Equal(writer.messages[0].Key, writer.messages[wantWrites-1].Key) {
					t.Fatalf("retry changed fixed event: %s %v", got, err)
				}
				stored.Accepted = true
				expectBotItemSendInsert(mock, i).WillReturnError(&driver.MySQLError{Number: 1062})
				mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `im_bot_sends` WHERE msg_id = ? LIMIT ?")).
					WithArgs(id, 1).WillReturnRows(botSendRows(stored))
				if got, err := p.Publish(context.Background(), i); err != nil || got != id || len(writer.messages) != wantWrites {
					t.Fatalf("accepted replay: %s %v", got, err)
				}
			})
		}
	}
}

type botItemRecordStub struct {
	record    botSendRecord
	markCalls int
}

func (s *botItemRecordStub) Prepare(context.Context, botSendIntent) (botSendRecord, error) {
	return s.record, nil
}

func (s *botItemRecordStub) MarkAccepted(context.Context, string) error {
	s.markCalls++
	return nil
}

func TestBotItemPublisherRejectsMalformedStoreResultsBeforeAcceptedReplay(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		for _, change := range []string{"msg_id", "run", "index", "bot", "actor", "team", "group", "content", "time", "invalid scope", "invalid card", "invalid index"} {
			t.Run(strconv.FormatBool(accepted)+"/"+change, func(t *testing.T) {
				i := validBotIntent(t)
				i.ItemIndex = 1
				r := storedBotRecord(i)
				switch change {
				case "msg_id":
					r.MsgID = "bot-task:9:4"
				case "run":
					r.RunID++
				case "index":
					r.ItemIndex = 4
				case "bot":
					r.BotID++
				case "actor":
					r.InitiatorID++
				case "team":
					r.TeamID++
				case "group":
					r.GroupID++
				case "content":
					r.Content, _ = model.EncodeTaskCreatedCard(99, "另一个任务")
				case "time":
					r.TimestampUnixMs = 0
				case "invalid scope":
					i.TeamID, r.TeamID = 0, 0
				case "invalid card":
					i.Content, r.Content = "broken", "broken"
				case "invalid index":
					i.ItemIndex, r.ItemIndex = 5, 5
				}
				r.Accepted = accepted
				store := &botItemRecordStub{record: r}
				writer := &botWriterStub{}
				p := &kafkaBotPublisher{store: store, writer: writer}
				if id, err := p.Publish(context.Background(), i); id != "" || status.Code(err) != codes.Internal || len(writer.messages) != 0 || store.markCalls != 0 {
					t.Fatalf("invalid store result published: %s %v", id, err)
				}
			})
		}
	}
}
