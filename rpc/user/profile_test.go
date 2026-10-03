package main

import (
	"context"
	"errors"
	"net"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	jwtv5 "github.com/golang-jwt/jwt/v5"
	pkgjwt "github.com/yjydist/go-im/internal/pkg/jwt"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const profileTestSecret = "profile-test-secret-not-for-real-use"
const profileQuery = "SELECT id, username, nickname, status FROM `users` WHERE id = ? LIMIT ?"

func newTestUserServer(t *testing.T) (*userServer, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	gdb, err := gorm.Open(mysql.New(mysql.Config{Conn: db, SkipInitializeWithVersion: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	return &userServer{db: gdb, jwtSecret: profileTestSecret}, mock
}

func profileToken(t *testing.T, userID int64, secret string, expires time.Time, issuer string, method jwtv5.SigningMethod) string {
	t.Helper()
	claims := pkgjwt.Claims{UserID: userID, RegisteredClaims: jwtv5.RegisteredClaims{Issuer: issuer}}
	if !expires.IsZero() {
		claims.ExpiresAt = jwtv5.NewNumericDate(expires)
	}
	token, err := jwtv5.NewWithClaims(method, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestProfileRejectsInvalidCredentialsBeforeQuery(t *testing.T) {
	s, _ := newTestUserServer(t) // 没有设置任何 SQL 期望：无效身份不得查询数据库。
	future := time.Now().Add(time.Hour)
	valid, err := pkgjwt.GenerateToken(42, profileTestSecret, 1)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"missing":         nil,
		"malformed":       {"Bearer invalid"},
		"duplicate":       {"Bearer " + valid, "Bearer " + valid},
		"wrong signature": {"Bearer " + profileToken(t, 42, "wrong-secret", future, "go-im", jwtv5.SigningMethodHS256)},
		"expired":         {"Bearer " + profileToken(t, 42, profileTestSecret, time.Now().Add(-time.Hour), "go-im", jwtv5.SigningMethodHS256)},
		"missing expiry":  {"Bearer " + profileToken(t, 42, profileTestSecret, time.Time{}, "go-im", jwtv5.SigningMethodHS256)},
		"wrong issuer":    {"Bearer " + profileToken(t, 42, profileTestSecret, future, "other", jwtv5.SigningMethodHS256)},
		"wrong algorithm": {"Bearer " + profileToken(t, 42, profileTestSecret, future, "go-im", jwtv5.SigningMethodHS384)},
		"invalid user":    {"Bearer " + profileToken(t, 0, profileTestSecret, future, "go-im", jwtv5.SigningMethodHS256)},
	}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{"authorization": headers})
			_, err := s.GetMyInfo(ctx, &pb.GetMyInfoRequest{})
			if status.Code(err) != codes.Unauthenticated {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestProfileDatabaseOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows *sqlmock.Rows
		err  error
		code codes.Code
	}{
		{"active", sqlmock.NewRows([]string{"id", "username", "nickname", "status"}).AddRow(42, "alice", "Alice", 1), nil, codes.OK},
		{"disabled", sqlmock.NewRows([]string{"id", "username", "nickname", "status"}).AddRow(42, "alice", "Alice", 2), nil, codes.PermissionDenied},
		{"missing", sqlmock.NewRows([]string{"id", "username", "nickname", "status"}), nil, codes.NotFound},
		{"database failure", nil, errors.New("database disconnected"), codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestUserServer(t)
			expect := mock.ExpectQuery(regexp.QuoteMeta(profileQuery)).WithArgs(int64(42), 1)
			if tc.err != nil {
				expect.WillReturnError(tc.err)
			} else {
				expect.WillReturnRows(tc.rows)
			}
			token, err := pkgjwt.GenerateToken(42, profileTestSecret, 1)
			if err != nil {
				t.Fatal(err)
			}
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token, "user_id", "99"))
			user, err := s.GetMyInfo(ctx, &pb.GetMyInfoRequest{})
			if status.Code(err) != tc.code {
				t.Fatalf("got %v", err)
			}
			if tc.code == codes.OK && (user.GetId() != 42 || user.GetUsername() != "alice") {
				t.Fatalf("wrong user: %v", user)
			}
			if tc.code != codes.OK && user != nil {
				t.Fatal("failed query returned profile")
			}
		})
	}
}

func TestProfileOverRPC(t *testing.T) {
	s, mock := newTestUserServer(t)
	mock.ExpectQuery(regexp.QuoteMeta(profileQuery)).WithArgs(int64(42), 1).WillReturnRows(sqlmock.NewRows([]string{"id", "username", "nickname", "status"}).AddRow(42, "alice", "Alice", 1))
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
	token, err := pkgjwt.GenerateToken(42, profileTestSecret, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))
	user, err := pb.NewUserClient(conn).GetMyInfo(ctx, &pb.GetMyInfoRequest{})
	if err != nil || user.GetId() != 42 {
		t.Fatalf("RPC profile: %v, %v", user, err)
	}
}

func TestProfileDisabled(t *testing.T) {
	_, err := (&userServer{}).GetMyInfo(context.Background(), &pb.GetMyInfoRequest{})
	if status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
}
