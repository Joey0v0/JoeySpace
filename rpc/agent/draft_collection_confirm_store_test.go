package agent

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func confirmationCollectionFixture() taskDraftCollection {
	collection := collectionEditFixture()
	collection.Items[1].Draft.AssigneeResolution = assigneeUnassigned
	collection.Items[1].Draft.Deadline.Resolution = "unset"
	return collection
}

func collectionReview(collection taskDraftCollection, index int32) draftCollectionConfirmationReview {
	run := collection.Items[index]
	return draftCollectionConfirmationReview{Revision: run.Revision, Title: run.Draft.Title, Description: run.Draft.Description, AssigneeID: run.Draft.AssigneeID, DueAtUnixMs: run.Draft.DueAtUnixMs, DeadlineResolution: run.Draft.Deadline.Resolution}
}

func freezeCollectionFixture(collection taskDraftCollection, index int32) taskDraftCollection {
	collection.Items = append([]taskDraftRun(nil), collection.Items...)
	collection.Items[index].Status = draftCreating
	collection.Items[index].TaskRequestKey = draftCollectionTaskRequestKey(collection.ID, index)
	return collection
}

func TestCollectionFreezeUsesIndependentKeysAndKeepsVersionAndOtherItem(t *testing.T) {
	for _, index := range []int32{0, 1} {
		store, mock := testDraftStore(t)
		authorized := confirmationCollectionFixture()
		expectCollectionLock(mock, authorized)
		mock.ExpectExec(regexp.QuoteMeta(freezeDraftCollectionItemSQL)).WithArgs("creating", draftCollectionTaskRequestKey(9001, index), int64(9001), int64(index), int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		saved, err := store.freezeDraftCollectionItem(context.Background(), authorized, index, collectionReview(authorized, index))
		if err != nil || saved.Items[index].Status != draftCreating || saved.Items[index].TaskRequestKey != draftCollectionTaskRequestKey(9001, index) || saved.Items[index].Revision != 1 || saved.Items[index].Draft != authorized.Items[index].Draft || saved.Items[1-index] != authorized.Items[1-index] {
			t.Fatalf("freeze=%+v,%v", saved, err)
		}
	}
}

func TestCollectionFreezeAllowsSameContentProgressButRejectsConflict(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*taskDraftCollection)
		code   codes.Code
	}{
		{"creating won", func(c *taskDraftCollection) { *c = freezeCollectionFixture(*c, 0) }, codes.OK},
		{"succeeded won", func(c *taskDraftCollection) {
			*c = freezeCollectionFixture(*c, 0)
			c.Items[0].Status = draftSucceeded
			c.Items[0].TaskID = 7001
		}, codes.OK},
		{"target revision", func(c *taskDraftCollection) { c.Items[0].Revision++ }, codes.Aborted},
		{"target content", func(c *taskDraftCollection) { c.Items[0].Draft.Description = "changed" }, codes.Aborted},
		{"scope", func(c *taskDraftCollection) {
			c.Scope.GroupID++
			for i := range c.Items {
				c.Items[i].Scope = c.Scope
			}
		}, codes.Aborted},
		{"count", func(c *taskDraftCollection) { c.Items = c.Items[:1] }, codes.Aborted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			authorized, current := confirmationCollectionFixture(), confirmationCollectionFixture()
			tc.mutate(&current)
			expectCollectionLock(mock, current)
			if tc.code == codes.OK {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			saved, err := store.freezeDraftCollectionItem(context.Background(), authorized, 0, collectionReview(authorized, 0))
			if status.Code(err) != tc.code || (tc.code != codes.OK && (saved.ID != 0 || saved.Items != nil)) {
				t.Fatalf("progress=%+v,%v", saved, err)
			}
			if tc.code == codes.OK && saved.Items[0] != current.Items[0] {
				t.Fatalf("lost winner=%+v", saved)
			}
		})
	}
}

func TestCollectionFreezeRollsBackRowCountOrSQLError(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows int64
		err  error
		code codes.Code
	}{{"zero", 0, nil, codes.Aborted}, {"two", 2, nil, codes.Aborted}, {"SQL", 0, errors.New("private SQL detail"), codes.Unavailable}} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			collection := confirmationCollectionFixture()
			expectCollectionLock(mock, collection)
			expect := mock.ExpectExec(regexp.QuoteMeta(freezeDraftCollectionItemSQL))
			if tc.err != nil {
				expect.WillReturnError(tc.err)
			} else {
				expect.WillReturnResult(sqlmock.NewResult(0, tc.rows))
			}
			mock.ExpectRollback()
			saved, err := store.freezeDraftCollectionItem(context.Background(), collection, 0, collectionReview(collection, 0))
			if saved.ID != 0 || status.Code(err) != tc.code {
				t.Fatalf("rollback=%+v,%v", saved, err)
			}
		})
	}
}

func TestCollectionCompletePersistsResultAndReplaysMatchingSuccess(t *testing.T) {
	for _, existing := range []int64{0, 7001, 7002} {
		store, mock := testDraftStore(t)
		frozen := freezeCollectionFixture(confirmationCollectionFixture(), 1)
		current := freezeCollectionFixture(confirmationCollectionFixture(), 1)
		current.Items[0].Draft.Description = "other item changed"
		current.Items[0].Revision = 4
		if existing > 0 {
			current.Items[1].Status, current.Items[1].TaskID = draftSucceeded, existing
		}
		expectCollectionLock(mock, current)
		if existing == 0 {
			mock.ExpectExec(regexp.QuoteMeta(completeDraftCollectionItemSQL)).WithArgs("succeeded", int64(7001), int64(9001), int64(1), int64(1), "agent-task-9001-1").WillReturnResult(sqlmock.NewResult(0, 1))
		}
		if existing == 7002 {
			mock.ExpectRollback()
		} else {
			mock.ExpectCommit()
		}
		saved, err := store.completeDraftCollectionItem(context.Background(), frozen, 1, 7001)
		if existing == 7002 {
			if saved.ID != 0 || status.Code(err) != codes.AlreadyExists {
				t.Fatalf("mismatch=%+v,%v", saved, err)
			}
			continue
		}
		if err != nil || saved.Items[1].Status != draftSucceeded || saved.Items[1].TaskID != 7001 || saved.Items[1].Revision != 1 || saved.Items[1].Draft != frozen.Items[1].Draft || saved.Items[0] != current.Items[0] {
			t.Fatalf("complete=%+v,%v", saved, err)
		}
	}
}

func TestCollectionCompleteRejectsContentStateRegressionAndUpdateFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*taskDraftCollection)
		rows   int64
		err    error
		code   codes.Code
	}{
		{"content", func(c *taskDraftCollection) { c.Items[0].Draft.Description = "changed" }, 1, nil, codes.Aborted},
		{"regression", func(c *taskDraftCollection) {
			c.Items[0].Status = draftWaitingConfirmation
			c.Items[0].TaskRequestKey = ""
		}, 1, nil, codes.Aborted},
		{"zero", nil, 0, nil, codes.Aborted}, {"two", nil, 2, nil, codes.Aborted}, {"SQL", nil, 0, errors.New("private SQL detail"), codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			frozen, current := freezeCollectionFixture(confirmationCollectionFixture(), 0), freezeCollectionFixture(confirmationCollectionFixture(), 0)
			if tc.mutate != nil {
				tc.mutate(&current)
			}
			expectCollectionLock(mock, current)
			if tc.mutate == nil {
				expect := mock.ExpectExec(regexp.QuoteMeta(completeDraftCollectionItemSQL))
				if tc.err != nil {
					expect.WillReturnError(tc.err)
				} else {
					expect.WillReturnResult(sqlmock.NewResult(0, tc.rows))
				}
			}
			mock.ExpectRollback()
			saved, err := store.completeDraftCollectionItem(context.Background(), frozen, 0, 7001)
			if saved.ID != 0 || status.Code(err) != tc.code {
				t.Fatalf("complete failure=%+v,%v", saved, err)
			}
		})
	}
}

func TestCollectionMixedStatesRemainReadableAndWaitingItemEditable(t *testing.T) {
	store, mock := testDraftStore(t)
	collection := freezeCollectionFixture(confirmationCollectionFixture(), 0)
	collection.Items[0].Status, collection.Items[0].TaskID = draftSucceeded, 7001
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftCollectionForInitiator)).WithArgs(int64(9001), int64(400)).WillReturnRows(collectionEditRows(collection))
	loaded, err := store.loadDraftCollectionForInitiator(context.Background(), 9001, 400)
	if err != nil || loaded.Items[0].TaskID != 7001 || loaded.Items[1].Status != draftWaitingConfirmation {
		t.Fatalf("mixed read=%+v,%v", loaded, err)
	}
	candidate := collection.Items[1].Draft
	candidate.Title = "可继续编辑"
	expectCollectionLock(mock, collection)
	mock.ExpectExec(regexp.QuoteMeta(updateDraftCollectionText)).WithArgs(candidate.Title, candidate.Description, int64(9001), int64(1), int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	saved, err := store.updateDraftCollectionItem(context.Background(), collection, 1, candidate, collectionEditText)
	if err != nil || saved.Items[0] != collection.Items[0] || saved.Items[1].Revision != 2 {
		t.Fatalf("mixed edit=%+v,%v", saved, err)
	}
}

func TestCollectionFrozenStatesRejectWrongKeysIDsUnresolvedAndSkipped(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*taskDraftCollection)
	}{
		{"key for other item", func(c *taskDraftCollection) { c.Items[0].TaskRequestKey = "agent-task-9001-1" }},
		{"creating positive result", func(c *taskDraftCollection) { c.Items[0].TaskID = 7001 }},
		{"succeeded no result", func(c *taskDraftCollection) { c.Items[0].Status = draftSucceeded }},
		{"unresolved member", func(c *taskDraftCollection) {
			c.Items[0].Draft.AssigneeID = 0
			c.Items[0].Draft.AssigneeResolution = assigneeAmbiguous
		}},
		{"unresolved time", func(c *taskDraftCollection) {
			c.Items[0].Draft = c.Items[1].Draft
			c.Items[0].Draft.Deadline.Resolution = "needs_input"
		}},
		{"skipped", func(c *taskDraftCollection) { c.Items[0].Status = "skipped" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			collection := freezeCollectionFixture(confirmationCollectionFixture(), 0)
			tc.mutate(&collection)
			mock.ExpectQuery(regexp.QuoteMeta(selectDraftCollectionForInitiator)).WithArgs(int64(9001), int64(400)).WillReturnRows(collectionEditRows(collection))
			saved, err := store.loadDraftCollectionForInitiator(context.Background(), 9001, 400)
			if saved.ID != 0 || saved.Items != nil || status.Code(err) != codes.Unavailable {
				t.Fatalf("damage=%+v,%v", saved, err)
			}
		})
	}
}
