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
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const loginQuery = "SELECT id, password, status FROM `users` WHERE username = ? LIMIT ?"

func TestLoginRejectsMissingFieldsBeforeQuery(t *testing.T) {
	s, _ := newTestUserServer(t)
	for _, req := range []*pb.LoginRequest{{}, {Username: "alice"}, {Password: "password"}} {
		result, err := s.Login(context.Background(), req)
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("unexpected result: %v, %v", result, err)
		}
	}
}

func TestLoginDatabaseOutcomes(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		password string
		rows     *sqlmock.Rows
		dbErr    error
		want     codes.Code
	}{
		{"wrong password", "wrong-password", sqlmock.NewRows([]string{"id", "password", "status"}).AddRow(42, string(hash), 1), nil, codes.Unauthenticated},
		{"disabled with wrong password", "wrong-password", sqlmock.NewRows([]string{"id", "password", "status"}).AddRow(42, string(hash), 2), nil, codes.Unauthenticated},
		{"disabled", "correct-password", sqlmock.NewRows([]string{"id", "password", "status"}).AddRow(42, string(hash), 2), nil, codes.PermissionDenied},
		{"unknown user", "correct-password", sqlmock.NewRows([]string{"id", "password", "status"}), nil, codes.Unauthenticated},
		{"database failure", "correct-password", nil, errors.New("connection lost"), codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestUserServer(t)
			expect := mock.ExpectQuery(regexp.QuoteMeta(loginQuery)).WithArgs("alice", 1)
			if tc.dbErr != nil {
				expect.WillReturnError(tc.dbErr)
			} else {
				expect.WillReturnRows(tc.rows)
			}
			result, err := s.Login(context.Background(), &pb.LoginRequest{Username: "alice", Password: tc.password})
			if result != nil || status.Code(err) != tc.want {
				t.Fatalf("unexpected result: %v, %v", result, err)
			}
		})
	}
}

func TestLoginTokenCanQueryOwnProfileOverRPC(t *testing.T) {
	s, mock := newTestUserServer(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(loginQuery)).WithArgs("alice", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "password", "status"}).AddRow(42, string(hash), 1))
	mock.ExpectQuery(regexp.QuoteMeta(profileQuery)).WithArgs(int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "nickname", "status"}).AddRow(42, "alice", "Alice", 1))

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
	client := pb.NewUserClient(conn)
	login, err := client.Login(ctx, &pb.LoginRequest{Username: "alice", Password: "correct-password"})
	if err != nil || login.GetToken() == "" {
		t.Fatalf("login failed: %v", err)
	}
	claims, err := pkgjwt.ParseToken(login.GetToken(), profileTestSecret)
	if err != nil || claims.UserID != 42 || claims.Issuer != "go-im" {
		t.Fatalf("token is not compatible: %v, %v", claims, err)
	}
	profileCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+login.GetToken()))
	profile, err := client.GetMyInfo(profileCtx, &pb.GetMyInfoRequest{})
	if err != nil || profile.GetUsername() != "alice" {
		t.Fatalf("profile query failed: %v, %v", profile, err)
	}
}
