package main

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const botProfileQuery = "SELECT `id`,`code`,`display_name`,`status` FROM `im_bots` WHERE code = ? LIMIT ?"

func TestLookupEnabledBotUsesConfiguredProfile(t *testing.T) {
	for _, tc := range []struct {
		name        string
		id          int64
		state       int
		displayName string
		missing     bool
		dbError     error
		want        codes.Code
	}{
		{name: "enabled", id: 7, state: 1, displayName: "AI 助手", want: codes.OK},
		{name: "disabled", id: 7, state: 2, displayName: "AI 助手", want: codes.FailedPrecondition},
		{name: "invalid identity", id: 0, state: 1, displayName: "AI 助手", want: codes.FailedPrecondition},
		{name: "missing display name", id: 7, state: 1, displayName: " ", want: codes.FailedPrecondition},
		{name: "missing", missing: true, want: codes.FailedPrecondition},
		{name: "database error", dbError: errors.New("private database detail"), want: codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := testIMServer(t)
			query := mock.ExpectQuery(regexp.QuoteMeta(botProfileQuery)).WithArgs("task-assistant", 1)
			if tc.dbError != nil {
				query.WillReturnError(tc.dbError)
			} else {
				rows := sqlmock.NewRows([]string{"id", "code", "display_name", "status"})
				if !tc.missing {
					rows.AddRow(tc.id, "task-assistant", tc.displayName, tc.state)
				}
				query.WillReturnRows(rows)
			}
			bot, err := lookupEnabledBot(context.Background(), s.db, "task-assistant")
			if status.Code(err) != tc.want || tc.want == codes.OK && (bot.ID != 7 || bot.Code != "task-assistant" || bot.DisplayName != "AI 助手") {
				t.Fatalf("bot=%+v err=%v", bot, err)
			}
			if tc.dbError != nil && status.Convert(err).Message() == tc.dbError.Error() {
				t.Fatal("database details leaked")
			}
		})
	}
}

func TestLookupEnabledBotRejectsMissingConfigurationBeforeQuery(t *testing.T) {
	s, _ := testIMServer(t)
	for _, code := range []string{"", " task-assistant", "abcdefghijklmnopqrstuvwxyz0123456"} {
		if _, err := lookupEnabledBot(context.Background(), s.db, code); status.Code(err) != codes.Unavailable {
			t.Fatalf("invalid code=%q err=%v", code, err)
		}
	}
	if _, err := lookupEnabledBot(context.Background(), nil, "task-assistant"); status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
}
