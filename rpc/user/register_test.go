package main

import (
	"context"
	"database/sql/driver"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/bwmarrin/snowflake"
	mysql "github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/rpc/user/pb"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

const registerLookup = "SELECT .* FROM `users` WHERE username = .* LIMIT .*"

type passwordHashArg string

func (a passwordHashArg) Match(value driver.Value) bool {
	hash, ok := value.(string)
	return ok && hash != string(a) && bcrypt.CompareHashAndPassword([]byte(hash), []byte(a)) == nil
}

type positiveIDArg struct{}

func (positiveIDArg) Match(value driver.Value) bool {
	id, ok := value.(int64)
	return ok && id > 0
}

func registerTestServer(t *testing.T) (*userServer, sqlmock.Sqlmock) {
	t.Helper()
	s, mock := newTestUserServer(t)
	node, err := snowflake.NewNode(2)
	if err != nil {
		t.Fatal(err)
	}
	s.idNode = node
	return s, mock
}

func TestRegisterValidatesBeforeDatabase(t *testing.T) {
	s, _ := registerTestServer(t)
	for _, req := range []*pb.RegisterRequest{
		{Username: "ab", Password: "password"},
		{Username: "alice", Password: "short"},
		{Username: "alice", Password: "password", Nickname: string(make([]byte, 65))},
	} {
		result, err := s.Register(context.Background(), req)
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("unexpected result: %v, %v", result, err)
		}
	}
}

func TestRegisterStoresHashAndDefaultNickname(t *testing.T) {
	s, mock := registerTestServer(t)
	mock.ExpectQuery(registerLookup).WithArgs("alice", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `users`").WithArgs("alice", passwordHashArg("correct-password"), "alice", 1, positiveIDArg{}).
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
	result, err := pb.NewUserClient(conn).Register(ctx, &pb.RegisterRequest{Username: "alice", Password: "correct-password"})
	if err != nil || result == nil {
		t.Fatalf("register failed: %v, %v", result, err)
	}
}

func TestRegisterDuplicateAndDatabaseFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		lookupRows *sqlmock.Rows
		lookupErr  error
		insertErr  error
		want       codes.Code
	}{
		{"existing username", sqlmock.NewRows([]string{"id"}).AddRow(42), nil, nil, codes.AlreadyExists},
		{"lookup failure", nil, errors.New("database disconnected"), nil, codes.Unavailable},
		{"concurrent duplicate", sqlmock.NewRows([]string{"id"}), nil, &mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}, codes.AlreadyExists},
		{"insert failure", sqlmock.NewRows([]string{"id"}), nil, errors.New("write failed"), codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := registerTestServer(t)
			expect := mock.ExpectQuery(registerLookup).WithArgs("alice", 1)
			if tc.lookupErr != nil {
				expect.WillReturnError(tc.lookupErr)
			} else {
				expect.WillReturnRows(tc.lookupRows)
			}
			if tc.insertErr != nil {
				mock.ExpectBegin()
				mock.ExpectExec("INSERT INTO `users`").WithArgs("alice", passwordHashArg("correct-password"), "Alice", 1, positiveIDArg{}).
					WillReturnError(tc.insertErr)
				mock.ExpectRollback()
			}
			result, err := s.Register(context.Background(), &pb.RegisterRequest{Username: "alice", Password: "correct-password", Nickname: "Alice"})
			if result != nil || status.Code(err) != tc.want {
				t.Fatalf("unexpected result: %v, %v", result, err)
			}
		})
	}
}
