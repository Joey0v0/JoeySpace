package main

import (
	"context"
	"errors"
	"net"
	"regexp"
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

func teamContext(t *testing.T) context.Context {
	t.Helper()
	token, err := pkgjwt.GenerateToken(42, profileTestSecret, 1)
	if err != nil {
		t.Fatal(err)
	}
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
}

func expectTeamCreator(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta(profileQuery)).WithArgs(int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "nickname", "status"}).AddRow(42, "alice", "Alice", 1))
}

func TestCreateTeamRejectsInvalidInputBeforeWrite(t *testing.T) {
	s, _ := registerTestServer(t)
	for _, name := range []string{"", "  ", string(make([]byte, 65)), "\xff"} {
		result, err := s.CreateTeam(teamContext(t), &pb.CreateTeamRequest{Name: name})
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("name %q: %v, %v", name, result, err)
		}
	}
	result, err := s.CreateTeam(context.Background(), &pb.CreateTeamRequest{Name: "team"})
	if result != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token: %v, %v", result, err)
	}
}

func TestCreateTeamTransactionOverRPC(t *testing.T) {
	s, mock := registerTestServer(t)
	expectTeamCreator(mock)
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `teams`").WithArgs("Project A", int64(42), positiveIDArg{}).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO `team_members`").WithArgs(positiveIDArg{}, int64(42), int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

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
	result, err := pb.NewUserClient(conn).CreateTeam(ctx, &pb.CreateTeamRequest{Name: " Project A "})
	if err != nil || result.GetTeamId() <= 0 {
		t.Fatalf("create team RPC: %v, %v", result, err)
	}
}

func TestCreateTeamRollsBackWhenMemberInsertFails(t *testing.T) {
	s, mock := registerTestServer(t)
	expectTeamCreator(mock)
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `teams`").WithArgs("Project A", int64(42), positiveIDArg{}).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO `team_members`").WithArgs(positiveIDArg{}, int64(42), int64(2)).
		WillReturnError(errors.New("member write failed"))
	mock.ExpectRollback()
	result, err := s.CreateTeam(teamContext(t), &pb.CreateTeamRequest{Name: "Project A"})
	if result != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("member insert failure: %v, %v", result, err)
	}
}
