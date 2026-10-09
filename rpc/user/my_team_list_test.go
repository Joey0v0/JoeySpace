package main

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	pkgjwt "github.com/yjydist/go-im/internal/pkg/jwt"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const myTeamsQuery = "SELECT team_members.team_id AS team_id, teams.name, team_members.role FROM `team_members` JOIN teams ON teams.id = team_members.team_id WHERE team_members.user_id = ? AND team_members.membership_state = ? AND team_members.team_id > ? ORDER BY team_members.team_id ASC LIMIT ?"

func TestListMyTeamsIdentityAndActivePagination(t *testing.T) {
	for _, id := range []int64{42, 73} {
		t.Run(strconv.FormatInt(id, 10), func(t *testing.T) {
			s, mock := newTestUserServer(t)
			token, err := pkgjwt.GenerateToken(id, profileTestSecret, 1)
			if err != nil {
				t.Fatal(err)
			}
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
			mock.ExpectQuery(regexp.QuoteMeta(profileQuery)).WithArgs(id, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "username", "nickname", "status"}).AddRow(id, "member", "Member", 1))
			// Exact active filter excludes leaving/left; identity comes only from the signed token.
			mock.ExpectQuery("^"+regexp.QuoteMeta(myTeamsQuery)+"$").WithArgs(id, int64(0), int64(9007199254740993), 3).WillReturnRows(sqlmock.NewRows([]string{"team_id", "name", "role"}).AddRow(9007199254740994, "First", 2).AddRow(9007199254740995, "Second", 1).AddRow(9007199254740996, "Next", 0))
			got, err := s.ListMyTeams(ctx, &pb.ListMyTeamsRequest{AfterTeamId: 9007199254740993, Limit: 2})
			if err != nil || len(got.GetTeams()) != 2 || got.GetTeams()[0].GetTeamId() != 9007199254740994 || got.GetTeams()[1].GetName() != "Second" || got.GetTeams()[1].GetRole() != 1 || got.GetNextAfterTeamId() != 9007199254740995 {
				t.Fatalf("pagination: %v, %v", got, err)
			}
		})
	}
}

func TestListMyTeamsDefaultLimitAndLastPage(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	mock.ExpectQuery("^"+regexp.QuoteMeta(myTeamsQuery)+"$").WithArgs(int64(42), int64(0), int64(0), 21).WillReturnRows(sqlmock.NewRows([]string{"team_id", "name", "role"}).AddRow(100, "Only", 0))
	got, err := s.ListMyTeams(teamContext(t), &pb.ListMyTeamsRequest{})
	if err != nil || len(got.GetTeams()) != 1 || got.GetNextAfterTeamId() != 0 {
		t.Fatalf("last page: %v, %v", got, err)
	}
}

func TestListMyTeamsRejectsInvalidInputAndMissingIdentity(t *testing.T) {
	s, _ := newTestUserServer(t)
	for _, req := range []*pb.ListMyTeamsRequest{{AfterTeamId: -1}, {Limit: -1}, {Limit: 101}} {
		got, err := s.ListMyTeams(teamContext(t), req)
		if got != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid request: %v, %v", got, err)
		}
	}
	got, err := s.ListMyTeams(context.Background(), &pb.ListMyTeamsRequest{})
	if got != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing identity: %v, %v", got, err)
	}
}

func TestListMyTeamsDisabledIdentity(t *testing.T) {
	s, mock := newTestUserServer(t)
	mock.ExpectQuery(regexp.QuoteMeta(profileQuery)).WithArgs(int64(42), 1).WillReturnRows(sqlmock.NewRows([]string{"id", "username", "nickname", "status"}).AddRow(42, "alice", "Alice", 0))
	got, err := s.ListMyTeams(teamContext(t), &pb.ListMyTeamsRequest{})
	if got != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("disabled identity: %v, %v", got, err)
	}
}

func TestListMyTeamsEmptyAndDatabaseFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "failure"}[fail], func(t *testing.T) {
			s, mock := newTestUserServer(t)
			expectTeamCreator(mock)
			q := mock.ExpectQuery("^"+regexp.QuoteMeta(myTeamsQuery)+"$").WithArgs(int64(42), int64(0), int64(0), 101)
			if fail {
				q.WillReturnError(errors.New("database lost"))
			} else {
				q.WillReturnRows(sqlmock.NewRows([]string{"team_id", "name", "role"}))
			}
			got, err := s.ListMyTeams(teamContext(t), &pb.ListMyTeamsRequest{Limit: 100})
			if fail {
				if got != nil || status.Code(err) != codes.Unavailable {
					t.Fatalf("failure: %v, %v", got, err)
				}
			} else if err != nil || got == nil || len(got.GetTeams()) != 0 || got.GetNextAfterTeamId() != 0 {
				t.Fatalf("empty: %v, %v", got, err)
			}
		})
	}
}

func TestListMyTeamsOverRPCPreservesLargeID(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	mock.ExpectQuery("^"+regexp.QuoteMeta(myTeamsQuery)+"$").WithArgs(int64(42), int64(0), int64(0), 21).WillReturnRows(sqlmock.NewRows([]string{"team_id", "name", "role"}).AddRow(9007199254740993, "Large", 2))
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
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", metadata.ValueFromIncomingContext(teamContext(t), "authorization")[0]))
	got, err := pb.NewUserClient(conn).ListMyTeams(ctx, &pb.ListMyTeamsRequest{})
	if err != nil || len(got.GetTeams()) != 1 || got.GetTeams()[0].GetTeamId() != 9007199254740993 {
		t.Fatalf("RPC: %v, %v", got, err)
	}
}
