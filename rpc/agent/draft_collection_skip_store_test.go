package agent

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func skippedCollectionFixture(collection taskDraftCollection, index int32) taskDraftCollection {
	collection.Items = append([]taskDraftRun(nil), collection.Items...)
	collection.Items[index].Status = draftSkipped
	return collection
}

func TestCollectionSkipKeepsUnresolvedEvidenceAndVersionIncludingMaximum(t *testing.T) {
	for _, maximum := range []bool{false, true} {
		store, mock := testDraftStore(t)
		authorized := collectionEditFixture()
		if maximum {
			authorized.Items[1].Revision = int64(1<<63 - 1)
		}
		current := collectionEditFixture()
		current.Items[1].Revision = authorized.Items[1].Revision
		current.Items[0].Revision = 7
		current.Items[0].Draft.Description = "其他项编辑"
		expectCollectionLock(mock, current)
		mock.ExpectExec(regexp.QuoteMeta(skipDraftCollectionItemSQL)).WithArgs("skipped", int64(9001), int64(1), authorized.Items[1].Revision).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		saved, err := store.skipDraftCollectionItem(context.Background(), authorized, 1)
		if err != nil || saved.Items[1].Status != draftSkipped || saved.Items[1].Draft != authorized.Items[1].Draft || saved.Items[1].Revision != authorized.Items[1].Revision || saved.Items[1].TaskID != 0 || saved.Items[1].TaskRequestKey != "" || saved.Items[0] != current.Items[0] {
			t.Fatalf("skip=%+v,%v", saved, err)
		}
	}
}

func TestCollectionSkipSameVersionReplayAndConcurrentWinnerAvoidUpdate(t *testing.T) {
	for _, alreadyReadSkipped := range []bool{false, true} {
		store, mock := testDraftStore(t)
		authorized := collectionEditFixture()
		if alreadyReadSkipped {
			authorized = skippedCollectionFixture(authorized, 1)
		}
		current := skippedCollectionFixture(collectionEditFixture(), 1)
		expectCollectionLock(mock, current)
		mock.ExpectCommit()
		saved, err := store.skipDraftCollectionItem(context.Background(), authorized, 1)
		if err != nil || saved.Items[1] != current.Items[1] {
			t.Fatalf("replay=%+v,%v", saved, err)
		}
	}
}

func TestCollectionSkipRejectsFrozenCompetitorChangedContentAndStateRegression(t *testing.T) {
	for _, tc := range []struct {
		name           string
		mutate         func(*taskDraftCollection)
		alreadySkipped bool
		code           codes.Code
	}{
		{"creating won", func(c *taskDraftCollection) { *c = freezeCollectionFixture(*c, 0) }, false, codes.FailedPrecondition},
		{"succeeded won", func(c *taskDraftCollection) {
			*c = freezeCollectionFixture(*c, 0)
			c.Items[0].Status = draftSucceeded
			c.Items[0].TaskID = 7001
		}, false, codes.FailedPrecondition},
		{"version", func(c *taskDraftCollection) { c.Items[0].Revision++ }, false, codes.Aborted},
		{"content", func(c *taskDraftCollection) { c.Items[0].Draft.Description = "changed" }, false, codes.Aborted},
		{"scope", func(c *taskDraftCollection) {
			c.Scope.GroupID++
			for i := range c.Items {
				c.Items[i].Scope = c.Scope
			}
		}, false, codes.Aborted},
		{"count", func(c *taskDraftCollection) { c.Items = c.Items[:1] }, false, codes.Aborted},
		{"skipped back to waiting", func(*taskDraftCollection) {}, true, codes.FailedPrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			authorized, current := confirmationCollectionFixture(), confirmationCollectionFixture()
			if tc.alreadySkipped {
				authorized = skippedCollectionFixture(authorized, 0)
			}
			tc.mutate(&current)
			expectCollectionLock(mock, current)
			mock.ExpectRollback()
			saved, err := store.skipDraftCollectionItem(context.Background(), authorized, 0)
			if saved.ID != 0 || saved.Items != nil || status.Code(err) != tc.code {
				t.Fatalf("conflict=%+v,%v", saved, err)
			}
		})
	}
}

func TestCollectionSkipUpdateAndCommitFailuresNeverReportSuccess(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rows      int64
		err       error
		commitErr bool
		code      codes.Code
	}{
		{"zero rows", 0, nil, false, codes.Aborted}, {"two rows", 2, nil, false, codes.Aborted}, {"SQL error", 0, errors.New("private SQL detail"), false, codes.Unavailable}, {"commit error", 1, nil, true, codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			authorized := collectionEditFixture()
			expectCollectionLock(mock, authorized)
			expect := mock.ExpectExec(regexp.QuoteMeta(skipDraftCollectionItemSQL)).WithArgs("skipped", int64(9001), int64(1), int64(1))
			if tc.err != nil {
				expect.WillReturnError(tc.err)
			} else {
				expect.WillReturnResult(sqlmock.NewResult(0, tc.rows))
			}
			if tc.commitErr {
				mock.ExpectCommit().WillReturnError(errors.New("private commit detail"))
			} else {
				mock.ExpectRollback()
			}
			saved, err := store.skipDraftCollectionItem(context.Background(), authorized, 1)
			if saved.ID != 0 || saved.Items != nil || status.Code(err) != tc.code {
				t.Fatalf("failure=%+v,%v", saved, err)
			}
		})
	}
}

func TestCollectionSkipWinningLockPreventsConfirmationTaskCall(t *testing.T) {
	store, mock := testDraftStore(t)
	authorized := confirmationCollectionFixture()
	current := skippedCollectionFixture(confirmationCollectionFixture(), 0)
	confirmer := &draftConfirmer{store: store, tasks: draftTaskCreateFunc(func(context.Context, *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
		t.Fatal("skip-winning transaction called Task")
		return nil, nil
	})}
	expectCollectionLock(mock, current)
	mock.ExpectRollback()
	saved, err := confirmer.confirmCollectionItem(context.Background(), "user-token", authorized, 0, collectionReview(authorized, 0))
	if saved.ID != 0 || status.Code(err) != codes.Aborted {
		t.Fatalf("freeze loser=%+v,%v", saved, err)
	}
}

func TestCollectionSkippedReadAndOtherWaitingEditsAndConfirmationRemainAvailable(t *testing.T) {
	store, mock := testDraftStore(t)
	collection := skippedCollectionFixture(collectionEditFixture(), 1)
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftCollectionForInitiator)).WithArgs(int64(9001), int64(400)).WillReturnRows(collectionEditRows(collection))
	loaded, err := store.loadDraftCollectionForInitiator(context.Background(), 9001, 400)
	if err != nil || loaded.Items[1] != collection.Items[1] {
		t.Fatalf("read=%+v,%v", loaded, err)
	}
	candidate := collection.Items[0].Draft
	candidate.Description = "可继续编辑"
	expectCollectionLock(mock, collection)
	mock.ExpectExec(regexp.QuoteMeta(updateDraftCollectionText)).WithArgs(candidate.Title, candidate.Description, int64(9001), int64(0), int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	edited, err := store.updateDraftCollectionItem(context.Background(), collection, 0, candidate, collectionEditText)
	if err != nil || edited.Items[1] != collection.Items[1] || edited.Items[0].Revision != 2 {
		t.Fatalf("other edit=%+v,%v", edited, err)
	}
	expectCollectionLock(mock, edited)
	mock.ExpectExec(regexp.QuoteMeta(freezeDraftCollectionItemSQL)).WithArgs("creating", "agent-task-9001-0", int64(9001), int64(0), int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	frozen, err := store.freezeDraftCollectionItem(context.Background(), edited, 0, collectionReview(edited, 0))
	if err != nil || frozen.Items[0].Status != draftCreating || frozen.Items[1] != collection.Items[1] {
		t.Fatalf("other confirmation=%+v,%v", frozen, err)
	}
}
