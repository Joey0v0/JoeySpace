package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func resultDraftHelperScope() draftRunScope {
	return draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}
}

func resultDraftHelperItems() []taskDraft {
	const sourceID int64 = 9007199254740993
	reference := time.Date(2026, 10, 4, 2, 0, 0, 0, time.UTC).UnixMilli()
	due := time.Date(2026, 10, 5, 7, 30, 0, 0, time.UTC).UnixMilli()
	return []taskDraft{
		{Title: "修复缓存", Description: "保留原文依据", SourceMessageID: sourceID, AssigneeName: "张三", AssigneeID: sourceID + 2, AssigneeResolution: assigneeMatched, DueAtUnixMs: due,
			Deadline: draftDeadlineMetadata{Text: "明天 15:30", Source: "message", SourceMessageID: sourceID, ReferenceUnixMs: reference, Timezone: draftDeadlineTimezone, Resolution: "parsed", ParsedUnixMs: due, InstructionReferenceUnixMs: reference}},
		{Title: "整理文档", SourceMessageID: sourceID - 1, AssigneeName: "李四", AssigneeResolution: assigneeAmbiguous,
			Deadline: draftDeadlineMetadata{Text: "明天下午", Source: "instruction", ReferenceUnixMs: reference, Timezone: draftDeadlineTimezone, Resolution: "needs_input", Reason: "unsupported_expression", InstructionReferenceUnixMs: reference}},
	}
}

func expectResultDraftHelperRun(mock sqlmock.Sqlmock, runID int64, count int, key string) *sqlmock.ExpectedExec {
	scope := resultDraftHelperScope()
	return mock.ExpectExec(regexp.QuoteMeta(insertDraftCollectionRun)).WithArgs(runID, scope.TeamID, scope.GroupID, scope.InitiatorID, key, testDraftFingerprint, "waiting_confirmation", "collection", int64(count))
}

func expectResultDraftHelperItem(mock sqlmock.Sqlmock, runID int64, index int, item taskDraft) *sqlmock.ExpectedExec {
	return mock.ExpectExec(regexp.QuoteMeta(insertDraftCollectionItem)).WithArgs(collectionItemArgs(runID, index, item)...)
}

func TestPrepareWaitingDraftCollectionNormalizesWithoutChangingOriginalEvidence(t *testing.T) {
	drafts := resultDraftHelperItems()
	drafts[0].Title = "  修复缓存  "
	drafts[0].Description = " 保留原文依据\n"
	drafts[1].Description = "  后续待本人审查  "
	original := append([]taskDraft(nil), drafts...)
	items, err := prepareWaitingDraftCollection(resultDraftHelperScope(), drafts)
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	for i, item := range items {
		want := original[i]
		want.Title = strings.TrimSpace(want.Title)
		want.Description = strings.TrimSpace(want.Description)
		if item != want || drafts[i] != original[i] {
			t.Fatalf("item %d changed evidence or mutated input: item=%+v input=%+v", i, item, drafts[i])
		}
	}
	if items[1].AssigneeResolution != assigneeAmbiguous || items[1].Deadline.Resolution != "needs_input" {
		t.Fatal("waiting drafts must retain unresolved choices for human review")
	}
	items[0].Deadline.Text = "changed output"
	items[0].Title = "changed output"
	if drafts[0] != original[0] || items[1].Deadline != original[1].Deadline {
		t.Fatal("prepared output aliases input or another item")
	}
}

func TestPrepareWaitingDraftCollectionRequiresEveryItemBeforeSQL(t *testing.T) {
	for _, name := range []string{"empty", "six items", "invalid scope", "bad last title", "legacy assignee", "legacy deadline", "bad deadline evidence", "bad assignee state"} {
		t.Run(name, func(t *testing.T) {
			store, _ := testDraftStore(t) // No SQL is expected, including BEGIN.
			scope := resultDraftHelperScope()
			drafts := resultDraftHelperItems()
			switch name {
			case "empty":
				drafts = nil
			case "six items":
				drafts = append(drafts, drafts...)
				drafts = append(drafts, drafts[0], drafts[1])
			case "invalid scope":
				scope.InitiatorID = 0
			case "bad last title":
				drafts[1].Title = " "
			case "legacy assignee":
				drafts[1].AssigneeName = ""
				drafts[1].AssigneeResolution = ""
			case "legacy deadline":
				drafts[1].Deadline = draftDeadlineMetadata{}
			case "bad deadline evidence":
				drafts[1].Deadline.ReferenceUnixMs++
			case "bad assignee state":
				drafts[1].AssigneeID = 99
			}
			items, err := prepareWaitingDraftCollection(scope, drafts)
			if items != nil || status.Code(err) != codes.InvalidArgument {
				t.Fatalf("partial preparation=%+v err=%v", items, err)
			}
			id, err := store.saveWaitingDraftCollection(context.Background(), 9001, scope, drafts, "batch-1", testDraftFingerprint)
			if id != 0 || status.Code(err) != codes.InvalidArgument {
				t.Fatalf("invalid drafts reached SQL: id=%d err=%v", id, err)
			}
		})
	}
	for _, count := range []int{1, maxGeneratedTaskDrafts} {
		drafts := make([]taskDraft, count)
		for i := range drafts {
			drafts[i] = resultDraftHelperItems()[0]
		}
		if items, err := prepareWaitingDraftCollection(resultDraftHelperScope(), drafts); err != nil || len(items) != count {
			t.Fatalf("valid count %d rejected: items=%+v err=%v", count, items, err)
		}
	}
}

func TestInsertWaitingDraftCollectionUsesOnlyCallerTransaction(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprintf("outer rollback=%t", rollback), func(t *testing.T) {
			store, mock := testDraftStore(t)
			const runID int64 = 9007199254740993
			const key = "agent-trigger:tasks:9007199254740995"
			items, err := prepareWaitingDraftCollection(resultDraftHelperScope(), resultDraftHelperItems())
			if err != nil {
				t.Fatal(err)
			}
			mock.ExpectBegin()
			expectResultDraftHelperRun(mock, runID, len(items), key).WillReturnResult(sqlmock.NewResult(0, 1))
			for i, item := range items {
				expectResultDraftHelperItem(mock, runID, i, item).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			outerErr := errors.New("outer result update failed")
			if rollback {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}
			err = store.db.WithContext(context.Background()).Transaction(func(tx *gorm.DB) error {
				if err := insertWaitingDraftCollection(tx, runID, resultDraftHelperScope(), items, key, testDraftFingerprint); err != nil {
					return err
				}
				if rollback {
					return outerErr
				}
				return nil
			})
			if (rollback && !errors.Is(err, outerErr)) || (!rollback && err != nil) {
				t.Fatalf("outer transaction err=%v", err)
			}
		})
	}
}

func TestWaitingDraftCollectionRequiresOneRowForEveryInsert(t *testing.T) {
	for _, failedInsert := range []int{-1, 0, 1} { // -1 is the run; 0/1 are individual items.
		for _, count := range []int64{-1, 0, 2} {
			t.Run(fmt.Sprintf("insert=%d rows=%d", failedInsert, count), func(t *testing.T) {
				store, mock := testDraftStore(t)
				items := resultDraftHelperItems()
				mock.ExpectBegin()
				runCount := int64(1)
				if failedInsert == -1 {
					runCount = count
				}
				expectResultDraftHelperRun(mock, 9001, len(items), "batch-1").WillReturnResult(sqlmock.NewResult(0, runCount))
				for i := 0; i <= failedInsert; i++ {
					itemCount := int64(1)
					if i == failedInsert {
						itemCount = count
					}
					expectResultDraftHelperItem(mock, 9001, i, items[i]).WillReturnResult(sqlmock.NewResult(0, itemCount))
				}
				mock.ExpectRollback()
				id, err := store.saveWaitingDraftCollection(context.Background(), 9001, resultDraftHelperScope(), items, "batch-1", testDraftFingerprint)
				if id != 0 || status.Code(err) != codes.Unavailable {
					t.Fatalf("invalid insert reported success: id=%d err=%v", id, err)
				}
			})
		}
	}
}

func TestInsertWaitingDraftCollectionPreservesSQLErrorForCallerRollback(t *testing.T) {
	for _, sqlErr := range []error{errors.New("private database detail"), &mysql.MySQLError{Number: 1062, Message: "duplicate source key"}} {
		t.Run(sqlErr.Error(), func(t *testing.T) {
			store, mock := testDraftStore(t)
			items := resultDraftHelperItems()
			mock.ExpectBegin()
			expectResultDraftHelperRun(mock, 9001, len(items), "batch-1").WillReturnResult(sqlmock.NewResult(0, 1))
			expectResultDraftHelperItem(mock, 9001, 0, items[0]).WillReturnResult(sqlmock.NewResult(0, 1))
			expectResultDraftHelperItem(mock, 9001, 1, items[1]).WillReturnError(sqlErr)
			mock.ExpectRollback()
			err := store.db.WithContext(context.Background()).Transaction(func(tx *gorm.DB) error {
				return insertWaitingDraftCollection(tx, 9001, resultDraftHelperScope(), items, "batch-1", testDraftFingerprint)
			})
			if !errors.Is(err, sqlErr) {
				t.Fatalf("caller lost SQL error: %v", err)
			}
		})
	}
	if err := insertWaitingDraftCollection(nil, 9001, resultDraftHelperScope(), resultDraftHelperItems(), "batch-1", testDraftFingerprint); err == nil {
		t.Fatal("nil transaction must not panic or succeed")
	}
}

func TestWaitingDraftCollectionCommitFailureNeverReturnsRunID(t *testing.T) {
	store, mock := testDraftStore(t)
	items := resultDraftHelperItems()
	mock.ExpectBegin()
	expectResultDraftHelperRun(mock, 9001, len(items), "batch-1").WillReturnResult(sqlmock.NewResult(0, 1))
	for i, item := range items {
		expectResultDraftHelperItem(mock, 9001, i, item).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit().WillReturnError(errors.New("private uncertain commit"))
	id, err := store.saveWaitingDraftCollection(context.Background(), 9001, resultDraftHelperScope(), items, "batch-1", testDraftFingerprint)
	if id != 0 || status.Code(err) != codes.Unavailable || strings.Contains(status.Convert(err).Message(), "private") {
		t.Fatalf("uncertain commit reported success: id=%d err=%v", id, err)
	}
}
