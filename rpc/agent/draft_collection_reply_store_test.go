package agent

import (
	"context"
	"database/sql/driver"
	"errors"
	"math"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/internal/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func replyCollectionFixture() taskDraftCollection {
	c := confirmationCollectionFixture()
	for i := range c.Items {
		c.Items[i].Status = draftSucceeded
		c.Items[i].TaskRequestKey = draftCollectionTaskRequestKey(c.ID, int32(i))
		c.Items[i].TaskID = 9007199254740993 + int64(i)
	}
	return c
}

var itemReplyColumns = []string{"run_id", "item_index", "task_id", "team_id", "group_id", "initiator_id", "msg_id", "content", "accepted"}

func itemReplyValues(r draftReplyRecord) []driver.Value {
	return []driver.Value{r.RunID, int64(r.ItemIndex), r.TaskID, r.TeamID, r.GroupID, r.InitiatorID, r.MsgID, r.Content, r.Accepted}
}

func itemReplyRows(records ...draftReplyRecord) *sqlmock.Rows {
	rows := sqlmock.NewRows(itemReplyColumns)
	for _, record := range records {
		rows.AddRow(itemReplyValues(record)...)
	}
	return rows
}

func expectedItemReply(t *testing.T, c taskDraftCollection, index int32) draftReplyRecord {
	t.Helper()
	r, err := collectionReplyIntent(c, index)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func expectItemReplyRead(mock sqlmock.Sqlmock, r draftReplyRecord, rows *sqlmock.Rows) {
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftCollectionReply)).WithArgs(r.RunID, int64(r.ItemIndex), r.InitiatorID).WillReturnRows(rows)
}

func TestCollectionReplyIntentIndependentItemsAndLegacyZero(t *testing.T) {
	c := replyCollectionFixture()
	zero, one := expectedItemReply(t, c, 0), expectedItemReply(t, c, 1)
	if zero.MsgID != "bot-task:9001" || one.MsgID != "bot-task:9001:1" || zero.Content == one.Content || one.TaskID != 9007199254740994 {
		t.Fatalf("independent identities: %+v %+v", zero, one)
	}
	legacy := c.Items[0]
	legacy.TaskRequestKey = draftTaskRequestKey(legacy.ID)
	old, err := replyIntent(legacy)
	if err != nil || old != zero {
		t.Fatalf("legacy zero changed: %+v %+v %v", old, zero, err)
	}
	one.Accepted = true
	if !one.matchesCollection(c, 1) || one.matchesCollection(c, 0) {
		t.Fatal("accepted advance or item isolation broken")
	}
	large := replyCollectionFixture()
	large.ID = math.MaxInt64
	for i := range large.Items {
		large.Items[i].ID = large.ID
		large.Items[i].TaskRequestKey = draftCollectionTaskRequestKey(large.ID, int32(i))
	}
	if expectedItemReply(t, large, 1).MsgID != "bot-task:9223372036854775807:1" {
		t.Fatal("run identity rounded")
	}
}

func TestCollectionReplyIntentRejectsUnsuccessfulOrInvalidTargetWithoutSQL(t *testing.T) {
	for _, name := range []string{"waiting", "creating", "skipped", "wrong key", "zero task", "zero revision", "wrong scope", "zero actor", "bad title", "unresolved assignee", "unresolved deadline", "negative index", "missing index", "index five"} {
		t.Run(name, func(t *testing.T) {
			store, _ := testDraftStore(t)
			c, index := replyCollectionFixture(), int32(0)
			r := &c.Items[0]
			switch name {
			case "waiting", "creating", "skipped":
				r.Status = draftRunStatus(name)
				if name == "waiting" {
					r.Status = draftWaitingConfirmation
				}
			case "wrong key":
				// Item 0 intentionally shares the legacy key; item 1 must not.
				index = 1
				c.Items[index].TaskRequestKey = draftTaskRequestKey(c.ID)
			case "zero task":
				r.TaskID = 0
			case "zero revision":
				r.Revision = 0
			case "wrong scope":
				r.Scope.TeamID++
			case "zero actor":
				c.Scope.InitiatorID, r.Scope.InitiatorID = 0, 0
			case "bad title":
				r.Draft.Title = " bad "
			case "unresolved assignee":
				r.Draft.AssigneeResolution = assigneeAmbiguous
			case "unresolved deadline":
				r.Draft.Deadline.Resolution = "needs_input"
			case "negative index":
				index = -1
			case "missing index":
				index = 2
			case "index five":
				index = 5
			}
			got, err := store.prepareCollectionReply(context.Background(), c, index)
			if got != (draftReplyRecord{}) || status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("prepare=%+v,%v", got, err)
			}
			_, found, err := store.loadCollectionReply(context.Background(), c, index)
			if found || status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("load found=%v,%v", found, err)
			}
		})
	}
}

func TestCollectionReplyPrepareCommitsFixedIntentAndReplays(t *testing.T) {
	for _, name := range []string{"new zero", "new one", "pending", "accepted", "other item changed"} {
		t.Run(name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			c, current, index := replyCollectionFixture(), replyCollectionFixture(), int32(1)
			if name == "new zero" {
				index = 0
			}
			if name == "other item changed" {
				c.Items[0].Status, c.Items[0].TaskRequestKey, c.Items[0].TaskID = draftWaitingConfirmation, "", 0
				current.Items[0] = c.Items[0]
				current.Items[0].Revision++
				current.Items[0].Draft.Description = "independent edit"
			}
			r := expectedItemReply(t, c, index)
			expectCollectionLock(mock, current)
			existing := name == "pending" || name == "accepted"
			if existing {
				r.Accepted = name == "accepted"
				expectItemReplyRead(mock, r, itemReplyRows(r))
			} else {
				expectItemReplyRead(mock, r, itemReplyRows())
				mock.ExpectExec(regexp.QuoteMeta(insertDraftCollectionReply)).WithArgs(itemReplyValues(r)...).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectCommit()
			got, err := store.prepareCollectionReply(context.Background(), c, index)
			if err != nil || got != r {
				t.Fatalf("prepare=%+v,%v want=%+v", got, err, r)
			}
		})
	}
}

func TestCollectionReplyPrepareRejectsTargetOrScopeRace(t *testing.T) {
	for _, name := range []string{"title", "description", "task", "revision", "source", "metadata", "scope", "count", "state"} {
		t.Run(name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			c, current := replyCollectionFixture(), replyCollectionFixture()
			switch name {
			case "title":
				current.Items[0].Draft.Title = "changed"
			case "description":
				current.Items[0].Draft.Description = "changed"
			case "task":
				current.Items[0].TaskID++
			case "revision":
				current.Items[0].Revision++
			case "source":
				current.Items[0].Draft.SourceMessageID++
			case "metadata":
				current.Items[0].Draft.Deadline.Text = "原依据变动"
			case "scope":
				current.Scope.GroupID++
				for i := range current.Items {
					current.Items[i].Scope = current.Scope
				}
			case "count":
				current.Items = current.Items[:1]
			case "state":
				current.Items[0].Status, current.Items[0].TaskID = draftCreating, 0
			}
			expectCollectionLock(mock, current)
			mock.ExpectRollback()
			got, err := store.prepareCollectionReply(context.Background(), c, 0)
			if got != (draftReplyRecord{}) || status.Code(err) != codes.Aborted {
				t.Fatalf("race=%+v,%v", got, err)
			}
		})
	}
}

func TestCollectionReplyPrepareRejectsConflictingRecordsAndWriteFailures(t *testing.T) {
	for _, name := range []string{"different record", "multiple records", "NULL index", "zero rows", "two rows", "SQL error", "commit error"} {
		t.Run(name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			c := replyCollectionFixture()
			r := expectedItemReply(t, c, 1)
			expectCollectionLock(mock, c)
			want := codes.Unavailable
			switch name {
			case "different record":
				wrong := r
				wrong.Content, _ = model.EncodeTaskCreatedCard(r.TaskID, "different")
				expectItemReplyRead(mock, r, itemReplyRows(wrong))
				want = codes.AlreadyExists
			case "multiple records":
				expectItemReplyRead(mock, r, itemReplyRows(r, r))
			case "NULL index":
				values := itemReplyValues(r)
				values[1] = nil
				expectItemReplyRead(mock, r, sqlmock.NewRows(itemReplyColumns).AddRow(values...))
			default:
				expectItemReplyRead(mock, r, itemReplyRows())
				exec := mock.ExpectExec(regexp.QuoteMeta(insertDraftCollectionReply))
				if name == "SQL error" {
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
			if name == "commit error" {
				mock.ExpectCommit().WillReturnError(errors.New("private commit"))
			} else {
				mock.ExpectRollback()
			}
			got, err := store.prepareCollectionReply(context.Background(), c, 1)
			if got != (draftReplyRecord{}) || status.Code(err) != want {
				t.Fatalf("failure=%+v,%v want=%v", got, err, want)
			}
		})
	}
}

func TestCollectionReplyLoadIsReadOnlyAndRejectsWrongCompleteRecord(t *testing.T) {
	for _, name := range []string{"none", "pending", "accepted", "run", "index", "task", "team", "group", "actor", "message", "content", "multiple", "NULL", "SQL"} {
		t.Run(name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			c := replyCollectionFixture()
			r, wrong := expectedItemReply(t, c, 0), expectedItemReply(t, c, 0)
			want := codes.Unavailable
			switch name {
			case "none", "pending", "accepted":
				want = codes.OK
				wrong.Accepted = name == "accepted"
			case "run":
				wrong.RunID++
			case "index":
				wrong.ItemIndex++
			case "task":
				wrong.TaskID++
			case "team":
				wrong.TeamID++
			case "group":
				wrong.GroupID++
			case "actor":
				wrong.InitiatorID++
			case "message":
				wrong.MsgID += ":1"
			case "content":
				wrong.Content += " "
			}
			switch name {
			case "none":
				expectItemReplyRead(mock, r, itemReplyRows())
			case "multiple":
				expectItemReplyRead(mock, r, itemReplyRows(r, r))
			case "NULL":
				values := itemReplyValues(r)
				values[1] = nil
				expectItemReplyRead(mock, r, sqlmock.NewRows(itemReplyColumns).AddRow(values...))
			case "SQL":
				mock.ExpectQuery(regexp.QuoteMeta(selectDraftCollectionReply)).WillReturnError(errors.New("private SQL"))
			default:
				expectItemReplyRead(mock, r, itemReplyRows(wrong))
			}
			got, found, err := store.loadCollectionReply(context.Background(), c, 0)
			if status.Code(err) != want || found != (want == codes.OK && name != "none") || (found && got != wrong) || (want != codes.OK && got != (draftReplyRecord{})) {
				t.Fatalf("load=%+v,%v,%v want=%v", got, found, err, want)
			}
		})
	}
}

func TestCollectionReplyAcceptRequiresExactRecordOrMatchingAcceptedReplay(t *testing.T) {
	for _, name := range []string{"one", "already accepted", "missing", "pending", "wrong content", "wrong task", "wrong index", "multiple", "two updated", "SQL"} {
		t.Run(name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			r := expectedItemReply(t, replyCollectionFixture(), 1)
			exec := mock.ExpectExec(regexp.QuoteMeta(acceptDraftCollectionReply)).WithArgs(itemReplyValues(r)[:8]...)
			want := codes.Unavailable
			if name == "SQL" {
				exec.WillReturnError(errors.New("private SQL"))
			} else if name == "one" {
				exec.WillReturnResult(sqlmock.NewResult(0, 1))
				want = codes.OK
			} else if name == "two updated" {
				exec.WillReturnResult(sqlmock.NewResult(0, 2))
			} else {
				exec.WillReturnResult(sqlmock.NewResult(0, 0))
				saved := r
				saved.Accepted = true
				switch name {
				case "already accepted":
					want = codes.OK
				case "pending":
					saved.Accepted = false
				case "wrong content":
					saved.Content += " "
				case "wrong task":
					saved.TaskID++
				case "wrong index":
					saved.ItemIndex = 0
				}
				if name == "missing" {
					expectItemReplyRead(mock, r, itemReplyRows())
				} else if name == "multiple" {
					expectItemReplyRead(mock, r, itemReplyRows(saved, saved))
				} else {
					expectItemReplyRead(mock, r, itemReplyRows(saved))
				}
			}
			if err := store.acceptCollectionReply(context.Background(), r); status.Code(err) != want {
				t.Fatalf("accept=%v want=%v", err, want)
			}
		})
	}
}

func TestCollectionReplyAcceptanceAndLegacyGuardRejectBadInputBeforeSQL(t *testing.T) {
	for _, name := range []string{"bad run", "bad index", "bad message", "bad task", "bad card", "task card mismatch", "bad actor"} {
		t.Run(name, func(t *testing.T) {
			store, _ := testDraftStore(t)
			r := expectedItemReply(t, replyCollectionFixture(), 1)
			switch name {
			case "bad run":
				r.RunID = 0
			case "bad index":
				r.ItemIndex = 5
			case "bad message":
				r.MsgID = "bot-task:9001"
			case "bad task":
				r.TaskID = 0
			case "bad card":
				r.Content = "{}"
			case "task card mismatch":
				r.TaskID++
			case "bad actor":
				r.InitiatorID = 0
			}
			if err := store.acceptCollectionReply(context.Background(), r); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("invalid accept=%v", err)
			}
		})
	}
	store, _ := testDraftStore(t)
	if err := store.acceptReply(context.Background(), expectedItemReply(t, replyCollectionFixture(), 1)); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("single accepted collection item=%v", err)
	}
}
