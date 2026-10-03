package agent

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type draftTextUpdateFunc func(context.Context, taskDraftRun, string, string) error

func (f draftTextUpdateFunc) updateDraftText(ctx context.Context, run taskDraftRun, title, description string) error {
	return f(ctx, run, title, description)
}

func editableRun() taskDraftRun {
	return taskDraftRun{Revision: 1,
		ID: 9001, Scope: draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400},
		Status: draftWaitingConfirmation,
		Draft:  taskDraft{Title: "旧标题", Description: "旧说明", AssigneeID: 500, DueAtUnixMs: 1000, SourceMessageID: 600},
	}
}

func TestDraftEditTextUsesCurrentAuthorizationAndPreservesOtherFields(t *testing.T) {
	r := testDraftAccessReader(t, 400,
		func(_ context.Context, runID, actorID int64) (taskDraftRun, error) {
			if runID != 9001 || actorID != 400 {
				t.Fatalf("load scope = %d, %d", runID, actorID)
			}
			return editableRun(), nil
		},
		func(_ context.Context, req *impb.CheckTeamGroupAccessRequest) error {
			if req.GetTeamId() != 200 || req.GetGroupId() != 300 {
				t.Fatalf("IM scope = %v", req)
			}
			return nil
		})
	r.editor = draftTextUpdateFunc(func(_ context.Context, run taskDraftRun, title, description string) error {
		if run.ID != 9001 || run.Scope.InitiatorID != 400 || run.Draft.Title != "旧标题" ||
			title != "新标题" || description != "新说明" {
			t.Fatalf("edit = %+v, %q, %q", run, title, description)
		}
		return nil
	})
	got, err := r.editText(context.Background(), "user-token", 9001, "旧标题", "旧说明", " 新标题 ", " 新说明 ", 1)
	if err != nil || got.Draft.Title != "新标题" || got.Draft.Description != "新说明" ||
		got.Draft.AssigneeID != 500 || got.Draft.DueAtUnixMs != 1000 || got.Draft.SourceMessageID != 600 {
		t.Fatalf("edited draft = %+v, %v", got, err)
	}
}

func TestDraftEditTextRejectsDeniedOrInvalidRequestsBeforeWriting(t *testing.T) {
	for _, tc := range []struct {
		name  string
		load  draftLoadFunc
		check draftAccessFunc
		title string
		want  codes.Code
	}{
		{"other initiator", func(context.Context, int64, int64) (taskDraftRun, error) {
			return taskDraftRun{}, status.Error(codes.NotFound, "draft run not found")
		}, nil, "新标题", codes.NotFound},
		{"left group", func(context.Context, int64, int64) (taskDraftRun, error) { return editableRun(), nil }, func(context.Context, *impb.CheckTeamGroupAccessRequest) error {
			return status.Error(codes.PermissionDenied, "left group")
		}, "新标题", codes.PermissionDenied},
		{"invalid title", func(context.Context, int64, int64) (taskDraftRun, error) { return editableRun(), nil }, nil, "  ", codes.InvalidArgument},
		{"already confirmed", func(context.Context, int64, int64) (taskDraftRun, error) {
			run := editableRun()
			run.Status = "completed"
			return run, nil
		}, nil, "新标题", codes.FailedPrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := tc.check
			if check == nil {
				check = func(context.Context, *impb.CheckTeamGroupAccessRequest) error { return nil }
			}
			r := testDraftAccessReader(t, 400, tc.load, check)
			r.editor = draftTextUpdateFunc(func(context.Context, taskDraftRun, string, string) error {
				t.Fatal("denied edit reached storage")
				return nil
			})
			got, err := r.editText(context.Background(), "user-token", 9001, "旧标题", "旧说明", tc.title, "新说明", 1)
			if got.ID != 0 || status.Code(err) != tc.want {
				t.Fatalf("result = %+v, %v", got, err)
			}
		})
	}
}

func TestDraftEditTextRejectsStaleClientView(t *testing.T) {
	r := testDraftAccessReader(t, 400,
		func(context.Context, int64, int64) (taskDraftRun, error) { return editableRun(), nil },
		func(context.Context, *impb.CheckTeamGroupAccessRequest) error { return nil })
	r.editor = draftTextUpdateFunc(func(context.Context, taskDraftRun, string, string) error {
		t.Fatal("stale client reached storage")
		return nil
	})
	got, err := r.editText(context.Background(), "user-token", 9001, "更早的标题", "旧说明", "新标题", "新说明", 1)
	if got.ID != 0 || status.Code(err) != codes.Aborted {
		t.Fatalf("stale edit = %+v, %v", got, err)
	}
}

func TestDraftStoreUpdatesOnlyWaitingFirstItemAfterComparingOldText(t *testing.T) {
	store, mock := testDraftStore(t)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT r.status, d.title, d.description, d.revision FROM agent_runs")).
		WithArgs(int64(9001), int64(400)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "title", "description", "revision"}).AddRow("waiting_confirmation", "旧标题", "旧说明", 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_task_drafts SET title = ?, description = ?, revision = revision + 1")).
		WithArgs("新标题", "新说明", int64(9001)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := store.updateDraftText(context.Background(), editableRun(), " 新标题 ", " 新说明 "); err != nil {
		t.Fatal(err)
	}
}

func TestDraftStoreRejectsStaleAndFinishedEdits(t *testing.T) {
	for _, tc := range []struct {
		name, status, title string
		want                codes.Code
	}{
		{"stale title", "waiting_confirmation", "别人修改了", codes.Aborted},
		{"finished run", "completed", "旧标题", codes.FailedPrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta("SELECT r.status, d.title, d.description, d.revision FROM agent_runs")).
				WithArgs(int64(9001), int64(400)).
				WillReturnRows(sqlmock.NewRows([]string{"status", "title", "description", "revision"}).AddRow(tc.status, tc.title, "旧说明", 1))
			mock.ExpectRollback()
			err := store.updateDraftText(context.Background(), editableRun(), "新标题", "新说明")
			if status.Code(err) != tc.want {
				t.Fatalf("edit error = %v", err)
			}
		})
	}
}

func TestDraftStoreMasksWriteFailure(t *testing.T) {
	store, mock := testDraftStore(t)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT r.status, d.title, d.description, d.revision FROM agent_runs")).
		WithArgs(int64(9001), int64(400)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "title", "description", "revision"}).AddRow("waiting_confirmation", "旧标题", "旧说明", 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_task_drafts SET title = ?, description = ?, revision = revision + 1")).
		WithArgs("新标题", "新说明", int64(9001)).WillReturnError(errors.New("private database detail"))
	mock.ExpectRollback()
	err := store.updateDraftText(context.Background(), editableRun(), "新标题", "新说明")
	if status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private database detail") {
		t.Fatalf("write error = %v", err)
	}
}
