package main

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestResolveTriggerMemberRequiresSamePositiveGenerationAcrossLookup(t *testing.T) {
	const largeGeneration int64 = 9007199254740993
	for _, scenario := range []struct {
		name      string
		before    int64
		after     int64
		candidate bool
		wantCode  codes.Code
	}{
		{name: "large unchanged empty", before: largeGeneration, after: largeGeneration, wantCode: codes.OK},
		{name: "large unchanged match", before: largeGeneration, after: largeGeneration, candidate: true, wantCode: codes.OK},
		{name: "rejoined empty", before: largeGeneration, after: largeGeneration + 1, wantCode: codes.PermissionDenied},
		{name: "rejoined match", before: largeGeneration, after: largeGeneration + 1, candidate: true, wantCode: codes.PermissionDenied},
		{name: "changed backwards", before: largeGeneration + 1, after: largeGeneration, candidate: true, wantCode: codes.PermissionDenied},
		{name: "bad first version", before: 0, wantCode: codes.Unavailable},
		{name: "bad final version", before: 1, after: 0, candidate: true, wantCode: codes.Unavailable},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			s, mock := newTriggerMemberTestServer(t)
			req := &pb.ResolveTriggerTeamMemberRequest{ActorId: 42, TeamId: 200, Name: "张三"}
			expectTriggerMemberQualification(mock, req, sqlmock.NewRows([]string{"status", "generation"}).AddRow(1, scenario.before))
			if scenario.before > 0 {
				rows := triggerMemberRows()
				if scenario.candidate {
					rows.AddRow(77, "zhangsan", req.Name)
				}
				expectTriggerMemberQuery(mock, req).WillReturnRows(rows).RowsWillBeClosed()
				expectTriggerMemberQualification(mock, req, sqlmock.NewRows([]string{"status", "generation"}).AddRow(1, scenario.after))
			}
			response, err := s.ResolveTriggerTeamMember(triggerTeamIMContext(context.Background()), req)
			if status.Code(err) != scenario.wantCode {
				t.Fatalf("response=%v err=%v want=%v", response, err, scenario.wantCode)
			}
			if scenario.wantCode != codes.OK {
				if response != nil || strings.Contains(status.Convert(err).Message(), req.Name) {
					t.Fatalf("failed qualification leaked candidates: %v %v", response, err)
				}
				return
			}
			want := 0
			if scenario.candidate {
				want = 1
			}
			if len(response.GetCandidates()) != want {
				t.Fatalf("response=%v want %d candidates", response, want)
			}
		})
	}
}
