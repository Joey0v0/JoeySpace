package agent

import (
	"context"
	"database/sql/driver"
	"errors"
	"reflect"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func collectionEditFixture() taskDraftCollection {
	scope := draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}
	first := taskDraft{Title: "修复缓存", AssigneeName: "张三", AssigneeID: 501, AssigneeResolution: assigneeMatched, SourceMessageID: 9007199254740993, DueAtUnixMs: 1000,
		Deadline: draftDeadlineMetadata{Text: "明天 15:30", Source: "message", SourceMessageID: 9007199254740995, ReferenceUnixMs: 2000, Timezone: draftDeadlineTimezone, Resolution: "parsed", ParsedUnixMs: 1000, InstructionReferenceUnixMs: 3000}}
	second := taskDraft{Title: "整理文档", AssigneeName: "李四", AssigneeResolution: assigneeAmbiguous, SourceMessageID: 9007199254740995,
		Deadline: draftDeadlineMetadata{Text: "明天下午", Source: "instruction", ReferenceUnixMs: 3000, Timezone: draftDeadlineTimezone, Resolution: "needs_input", Reason: "unsupported_expression", InstructionReferenceUnixMs: 3000}}
	return taskDraftCollection{ID: 9001, Scope: scope, Items: []taskDraftRun{
		{ID: 9001, Scope: scope, Revision: 1, Status: draftWaitingConfirmation, Draft: first},
		{ID: 9001, Scope: scope, Revision: 1, Status: draftWaitingConfirmation, Draft: second},
	}}
}

func collectionEditRows(collection taskDraftCollection) *sqlmock.Rows {
	rows := sqlmock.NewRows(collectionColumns)
	for i, run := range collection.Items {
		values := collectionRowValues(i, len(collection.Items), run.Draft)
		values[0], values[4], values[5], values[6] = collection.ID, run.Scope.TeamID, run.Scope.GroupID, run.Scope.InitiatorID
		values[8], values[14], values[15], values[18] = string(run.Status), run.TaskRequestKey, run.TaskID, run.Revision
		rows.AddRow(values...)
	}
	return rows
}

func expectCollectionLock(mock sqlmock.Sqlmock, collection taskDraftCollection) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftCollectionForInitiator+" FOR UPDATE")).WithArgs(collection.ID, collection.Scope.InitiatorID).WillReturnRows(collectionEditRows(collection))
}

func TestDraftCollectionItemStoreChangesOnlyTargetAndPermittedFields(t *testing.T) {
	for _, name := range []string{"text", "same matched ID explicitly selected", "ambiguous explicitly unassigned", "same parsed due selected", "needs input explicitly unset"} {
		t.Run(name, func(t *testing.T) {
			authorized := collectionEditFixture()
			index := int32(0)
			kind, query := collectionEditText, updateDraftCollectionText
			candidate := authorized.Items[0].Draft
			var args []driver.Value
			switch name {
			case "text":
				candidate.Title, candidate.Description = "新标题", "复核"
				args = []driver.Value{"新标题", "复核"}
			case "same matched ID explicitly selected":
				kind, query = collectionEditAssignee, updateDraftCollectionAssignee
				candidate.AssigneeResolution = assigneeSelected
				args = []driver.Value{int64(501), "selected"}
			case "ambiguous explicitly unassigned":
				index = 1
				candidate = authorized.Items[index].Draft
				kind, query = collectionEditAssignee, updateDraftCollectionAssignee
				candidate.AssigneeResolution = assigneeUnassigned
				args = []driver.Value{int64(0), "unassigned"}
			case "same parsed due selected":
				kind, query = collectionEditDeadline, updateDraftCollectionDeadline
				candidate.Deadline.Resolution = "selected"
				args = []driver.Value{int64(1000), "selected"}
			case "needs input explicitly unset":
				index = 1
				candidate = authorized.Items[index].Draft
				kind, query = collectionEditDeadline, updateDraftCollectionDeadline
				candidate.Deadline.Resolution = "unset"
				args = []driver.Value{int64(0), "unset"}
			}
			store, mock := testDraftStore(t)
			expectCollectionLock(mock, authorized)
			args = append(args, authorized.ID, int64(index), int64(1))
			mock.ExpectExec(regexp.QuoteMeta(query)).WithArgs(args...).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			saved, err := store.updateDraftCollectionItem(context.Background(), authorized, index, candidate, kind)
			if err != nil || saved.ID != authorized.ID || saved.Scope != authorized.Scope || len(saved.Items) != 2 || saved.Items[index].Revision != 2 || saved.Items[index].Draft != candidate || saved.Items[1-index] != authorized.Items[1-index] {
				t.Fatalf("saved=%+v,%v", saved, err)
			}
			if !reflect.DeepEqual(authorized, collectionEditFixture()) {
				t.Fatal("authorized snapshot mutated")
			}
		})
	}
}

func TestDraftCollectionItemStoreIndependentVersionsAndMaxNoOp(t *testing.T) {
	for _, tc := range []struct {
		name         string
		max, changed bool
		code         codes.Code
	}{
		{"another item changed", false, true, codes.OK}, {"no-op at max", true, false, codes.OK}, {"change at max", true, true, codes.FailedPrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authorized := collectionEditFixture()
			current := collectionEditFixture()
			if tc.max {
				authorized.Items[1].Revision = int64(1<<63 - 1)
				current.Items[1].Revision = authorized.Items[1].Revision
			}
			current.Items[0].Revision = 9
			current.Items[0].Draft.Description = "另项编辑"
			candidate := authorized.Items[1].Draft
			if tc.changed {
				candidate.Description = "本人编辑"
			}
			store, mock := testDraftStore(t)
			expectCollectionLock(mock, current)
			if tc.code == codes.OK {
				if tc.changed {
					mock.ExpectExec(regexp.QuoteMeta(updateDraftCollectionText)).WithArgs(candidate.Title, candidate.Description, authorized.ID, int64(1), authorized.Items[1].Revision).WillReturnResult(sqlmock.NewResult(0, 1))
				}
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			saved, err := store.updateDraftCollectionItem(context.Background(), authorized, 1, candidate, collectionEditText)
			if status.Code(err) != tc.code {
				t.Fatalf("saved=%+v,%v", saved, err)
			}
			if tc.code == codes.OK && (saved.Items[0] != current.Items[0] || saved.Items[1].Revision != authorized.Items[1].Revision+boolIncrement(tc.changed)) {
				t.Fatalf("independent/no-op=%+v", saved)
			}
		})
	}
}

func boolIncrement(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func TestDraftCollectionItemStoreRejectsChangedTargetAndInvalidUpdate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*taskDraftCollection)
		affected  int64
		updateErr error
		code      codes.Code
	}{
		{"target revision", func(c *taskDraftCollection) { c.Items[1].Revision++ }, 1, nil, codes.Aborted},
		{"target content without revision", func(c *taskDraftCollection) { c.Items[1].Draft.Description = "changed" }, 1, nil, codes.Aborted},
		{"count", func(c *taskDraftCollection) { c.Items = c.Items[:1] }, 1, nil, codes.Aborted},
		{"scope", func(c *taskDraftCollection) {
			c.Scope.GroupID = 301
			for i := range c.Items {
				c.Items[i].Scope = c.Scope
			}
		}, 1, nil, codes.Aborted},
		{"target frozen", func(c *taskDraftCollection) {
			c.Items[1].Status = draftCreating
			c.Items[1].TaskRequestKey = draftCollectionTaskRequestKey(c.ID, 1)
			c.Items[1].Draft.AssigneeResolution = assigneeUnassigned
			c.Items[1].Draft.Deadline.Resolution = "unset"
		}, 1, nil, codes.FailedPrecondition},
		{"zero affected", nil, 0, nil, codes.Aborted}, {"two affected", nil, 2, nil, codes.Aborted}, {"update failed", nil, 1, errors.New("private SQL failure"), codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authorized, current := collectionEditFixture(), collectionEditFixture()
			if tc.mutate != nil {
				tc.mutate(&current)
			}
			candidate := authorized.Items[1].Draft
			candidate.Description = "new"
			store, mock := testDraftStore(t)
			expectCollectionLock(mock, current)
			if tc.mutate == nil {
				expect := mock.ExpectExec(regexp.QuoteMeta(updateDraftCollectionText)).WithArgs(candidate.Title, "new", authorized.ID, int64(1), int64(1))
				if tc.updateErr != nil {
					expect.WillReturnError(tc.updateErr)
				} else {
					expect.WillReturnResult(sqlmock.NewResult(0, tc.affected))
				}
			}
			mock.ExpectRollback()
			saved, err := store.updateDraftCollectionItem(context.Background(), authorized, 1, candidate, collectionEditText)
			if saved.ID != 0 || saved.Items != nil || status.Code(err) != tc.code {
				t.Fatalf("conflict=%+v,%v", saved, err)
			}
		})
	}
}

func TestDraftCollectionEditFrozenClassificationStillValidatesIntegrity(t *testing.T) {
	for _, damaged := range []bool{false, true} {
		store, mock := testDraftStore(t)
		collection := collectionEditFixture()
		collection.Items[1].Status = draftCreating
		collection.Items[1].TaskRequestKey = draftCollectionTaskRequestKey(collection.ID, 1)
		collection.Items[1].Draft.AssigneeResolution = assigneeUnassigned
		collection.Items[1].Draft.Deadline.Resolution = "unset"
		if damaged {
			collection.Items[1].Draft.Deadline.Timezone = "invalid"
		}
		mock.ExpectQuery(regexp.QuoteMeta(selectDraftCollectionForInitiator)).WithArgs(int64(9001), int64(400)).WillReturnRows(collectionEditRows(collection))
		saved, err := store.loadDraftCollectionForItemEdit(context.Background(), 9001, 400, 1)
		want := codes.FailedPrecondition
		if damaged {
			want = codes.Unavailable
		}
		if saved.ID != 0 || status.Code(err) != want {
			t.Fatalf("frozen=%+v,%v", saved, err)
		}
	}
}
