package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// The complete predicate guards team/status scope, OR grouping, parameterized
// names, and binary comparison; sqlmock does not execute MySQL collations.
const resolveMemberQuery = "SELECT team_members.user_id AS user_id, users.username, users.nickname, team_members.role FROM `team_members` JOIN users ON users.id = team_members.user_id WHERE (team_members.team_id = ? AND users.status = ?) AND ((CAST(users.username AS BINARY) = CAST(? AS BINARY) OR CAST(users.nickname AS BINARY) = CAST(? AS BINARY))) ORDER BY team_members.user_id ASC LIMIT ?"

func expectMemberResolution(mock sqlmock.Sqlmock, name string) *sqlmock.ExpectedQuery {
	expectTeamCreator(mock)
	expectTeamMembership(mock, true, 0)
	return mock.ExpectQuery("^"+regexp.QuoteMeta(resolveMemberQuery)+"$").
		WithArgs(int64(100), 1, name, name, 21)
}

func resolvedMemberRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"user_id", "username", "nickname", "role"})
}

func TestResolveTeamMemberRejectsInvalidInput(t *testing.T) {
	s, _ := newTestUserServer(t)
	for _, req := range []*pb.ResolveTeamMemberRequest{
		nil, {TeamId: 0, Name: "张三"}, {TeamId: -1, Name: "张三"},
		{TeamId: 100}, {TeamId: 100, Name: " \t\u3000\n"},
		{TeamId: 100, Name: "\xff"}, {TeamId: 100, Name: strings.Repeat("张", 65)},
	} {
		result, err := s.ResolveTeamMember(teamContext(t), req)
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid request %v: %v, %v", req, result, err)
		}
	}
}

func TestResolveTeamMemberRejectsInvalidIdentityBeforeQuery(t *testing.T) {
	for _, headers := range [][]string{nil, {"Bearer invalid"}, {"Bearer one", "Bearer two"}} {
		s, _ := newTestUserServer(t)
		ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{"authorization": headers})
		result, err := s.ResolveTeamMember(ctx, &pb.ResolveTeamMemberRequest{TeamId: 100, Name: "张三"})
		if result != nil || status.Code(err) != codes.Unauthenticated {
			t.Fatalf("invalid identity: %v, %v", result, err)
		}
	}
}

func TestResolveTeamMemberRejectsDisabledCallerAndNonMember(t *testing.T) {
	t.Run("disabled caller", func(t *testing.T) {
		s, mock := newTestUserServer(t)
		mock.ExpectQuery(regexp.QuoteMeta(profileQuery)).WithArgs(int64(42), 1).
			WillReturnRows(sqlmock.NewRows([]string{"id", "username", "nickname", "status"}).AddRow(42, "alice", "Alice", 0))
		result, err := s.ResolveTeamMember(teamContext(t), &pb.ResolveTeamMemberRequest{TeamId: 100, Name: "张三"})
		if result != nil || status.Code(err) != codes.PermissionDenied {
			t.Fatalf("disabled caller: %v, %v", result, err)
		}
	})
	t.Run("outside team", func(t *testing.T) {
		s, mock := newTestUserServer(t)
		expectTeamCreator(mock)
		expectTeamMembership(mock, false, 0)
		result, err := s.ResolveTeamMember(teamContext(t), &pb.ResolveTeamMemberRequest{TeamId: 100, Name: "张三"})
		if result != nil || status.Code(err) != codes.PermissionDenied {
			t.Fatalf("non-member: %v, %v", result, err)
		}
	})
}

func TestResolveTeamMemberKeepsDistinctCandidatesAndEmptyResult(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows *sqlmock.Rows
		ids  []int64
	}{
		{"no match", resolvedMemberRows(), nil},
		{"one member matches both fields", resolvedMemberRows().AddRow(77, "张三", "张三", 0), []int64{77}},
		{"nickname homonyms", resolvedMemberRows().AddRow(77, "zhang-a", "张三", 0).AddRow(88, "zhang-b", "张三", 1), []int64{77, 88}},
		{"username and other nickname", resolvedMemberRows().AddRow(77, "张三", "小张", 0).AddRow(88, "zhang-b", "张三", 1), []int64{77, 88}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestUserServer(t)
			expectMemberResolution(mock, "张三").WillReturnRows(tc.rows)
			result, err := s.ResolveTeamMember(teamContext(t), &pb.ResolveTeamMemberRequest{TeamId: 100, Name: "\u3000张三 \n"})
			if err != nil || result.GetTruncated() || len(result.GetCandidates()) != len(tc.ids) {
				t.Fatalf("candidate result: %v, %v", result, err)
			}
			for i, id := range tc.ids {
				if result.GetCandidates()[i].GetUserId() != id {
					t.Fatalf("candidate %d: %v", i, result)
				}
			}
		})
	}
}

func TestResolveTeamMemberReportsTruncationOnlyWhenExtraMatchExists(t *testing.T) {
	for _, count := range []int{20, 21} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, mock := newTestUserServer(t)
			rows := resolvedMemberRows()
			for i := 1; i <= count; i++ {
				rows.AddRow(i, fmt.Sprintf("zhang-%d", i), "张三", 0)
			}
			expectMemberResolution(mock, "张三").WillReturnRows(rows)
			result, err := s.ResolveTeamMember(teamContext(t), &pb.ResolveTeamMemberRequest{TeamId: 100, Name: "张三"})
			if err != nil || len(result.GetCandidates()) != 20 || result.GetTruncated() != (count == 21) || result.GetCandidates()[19].GetUserId() != 20 {
				t.Fatalf("bounded candidates: %v, %v", result, err)
			}
		})
	}
}

func TestResolveTeamMemberKeepsNamesAsParameters(t *testing.T) {
	// Preserve exact case/accents; quotes, SQL wildcards and backslashes must
	// remain bound data, not fuzzy patterns or query fragments.
	for _, name := range []string{"Alice", "alice", "é", "e\u0301", "%_\\' OR 1=1 --", strings.Repeat("张", 64)} {
		t.Run(name, func(t *testing.T) {
			s, mock := newTestUserServer(t)
			expectMemberResolution(mock, name).WillReturnRows(resolvedMemberRows())
			result, err := s.ResolveTeamMember(teamContext(t), &pb.ResolveTeamMemberRequest{TeamId: 100, Name: name})
			if err != nil || result == nil || len(result.GetCandidates()) != 0 || result.GetTruncated() {
				t.Fatalf("bound name %q: %v, %v", name, result, err)
			}
		})
	}
}

func TestResolveTeamMemberDoesNotHideDatabaseFailure(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectMemberResolution(mock, "张三").WillReturnError(errors.New("private database detail"))
	result, err := s.ResolveTeamMember(teamContext(t), &pb.ResolveTeamMemberRequest{TeamId: 100, Name: "张三"})
	if result != nil || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private database detail") {
		t.Fatalf("query failure is not an empty match: %v, %v", result, err)
	}
	result, err = (&userServer{}).ResolveTeamMember(teamContext(t), &pb.ResolveTeamMemberRequest{TeamId: 100, Name: "张三"})
	if result != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("disabled database: %v, %v", result, err)
	}
}

func TestResolveTeamMemberOverRPC(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectMemberResolution(mock, "张三").WillReturnRows(resolvedMemberRows().AddRow(77, "zhang-san", "张三", 1))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	pb.RegisterUserServer(server, s)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", metadata.ValueFromIncomingContext(teamContext(t), "authorization")[0]))
	result, err := pb.NewUserClient(conn).ResolveTeamMember(ctx, &pb.ResolveTeamMemberRequest{TeamId: 100, Name: " 张三 "})
	if err != nil || len(result.GetCandidates()) != 1 || result.GetTruncated() {
		t.Fatalf("resolution RPC: %v, %v", result, err)
	}
	member := result.GetCandidates()[0]
	if member.GetUserId() != 77 || member.GetUsername() != "zhang-san" || member.GetNickname() != "张三" || member.GetRole() != 1 {
		t.Fatalf("member fields lost over RPC: %v", member)
	}
}
