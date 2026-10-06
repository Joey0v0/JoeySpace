package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

const pushTeamQuery = "SELECT users.status, team_members.generation FROM `team_members` JOIN users ON users.id = team_members.user_id WHERE team_members.team_id = ? AND team_members.user_id = ? AND team_members.membership_state = ? LIMIT ?"

func pushTeamContext(name string) context.Context {
	cert := &x509.Certificate{Raw: []byte{1}, DNSNames: []string{name}}
	return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
		Version: tls.VersionTLS13, PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}},
	}}})
}

func expectPushTeamQuery(mock sqlmock.Sqlmock, rows *sqlmock.Rows) {
	mock.ExpectQuery(regexp.QuoteMeta(pushTeamQuery)).WithArgs(int64(100), int64(42), model.TeamMembershipActive, 2).WillReturnRows(rows)
}

func TestPushTeamMemberReturnsOnlyActiveScopeAndGeneration(t *testing.T) {
	users, mock := newTestUserServer(t)
	s := &pushTeamServer{db: users.db}
	expectPushTeamQuery(mock, sqlmock.NewRows([]string{"status", "generation"}).AddRow(1, int64(9007199254740993)))
	response, err := s.CheckPushTeamMember(pushTeamContext(pushClientDNSName), &pb.CheckPushTeamMemberRequest{TeamId: 100, UserId: 42})
	if err != nil || response.GetTeamId() != 100 || response.GetUserId() != 42 || response.GetGeneration() != 9007199254740993 {
		t.Fatalf("response=%v err=%v", response, err)
	}
	if fields := response.ProtoReflect().Descriptor().Fields(); fields.Len() != 3 {
		t.Fatalf("unexpected response fields: %d", fields.Len())
	}
}

func TestPushTeamMemberRejectsOtherIdentityBeforeDatabase(t *testing.T) {
	users, _ := newTestUserServer(t)
	s := &pushTeamServer{db: users.db}
	for _, ctx := range []context.Context{
		context.Background(),
		metadata.NewIncomingContext(context.Background(), metadata.Pairs("service_identity", pushClientDNSName)),
		pushTeamContext("im.go-im.internal"),
		pushTeamContext("agent.go-im.internal"),
		pushTeamContext("*.go-im.internal"),
	} {
		if response, err := s.CheckPushTeamMember(ctx, &pb.CheckPushTeamMemberRequest{TeamId: 100, UserId: 42}); response != nil || status.Code(err) != codes.Unauthenticated {
			t.Fatalf("accepted untrusted Push identity: %v %v", response, err)
		}
	}
	if response, err := s.CheckPushTeamMember(nil, nil); response != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("nil context: %v %v", response, err)
	}
}

func TestPushTeamMemberRejectsInvalidScopeAndInactiveUsers(t *testing.T) {
	users, mock := newTestUserServer(t)
	s := &pushTeamServer{db: users.db}
	ctx := pushTeamContext(pushClientDNSName)
	for _, req := range []*pb.CheckPushTeamMemberRequest{nil, {}, {TeamId: 100}, {UserId: 42}, {TeamId: -1, UserId: 42}} {
		if response, err := s.CheckPushTeamMember(ctx, req); response != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid scope: %v %v", response, err)
		}
	}
	expectPushTeamQuery(mock, sqlmock.NewRows([]string{"status", "generation"}))
	if response, err := s.CheckPushTeamMember(ctx, &pb.CheckPushTeamMemberRequest{TeamId: 100, UserId: 42}); response != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("not member: %v %v", response, err)
	}
	expectPushTeamQuery(mock, sqlmock.NewRows([]string{"status", "generation"}).AddRow(2, 7))
	if response, err := s.CheckPushTeamMember(ctx, &pb.CheckPushTeamMemberRequest{TeamId: 100, UserId: 42}); response != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("disabled user: %v %v", response, err)
	}
}

func TestPushTeamMemberFailsClosedOnBadGenerationOrStorage(t *testing.T) {
	for _, rows := range []*sqlmock.Rows{
		sqlmock.NewRows([]string{"status", "generation"}).AddRow(1, 0),
		sqlmock.NewRows([]string{"status", "generation"}).AddRow(1, 7).AddRow(1, 7),
		sqlmock.NewRows([]string{"status", "generation"}).AddRow(1, 7).RowError(0, errors.New("private SQL")),
	} {
		users, mock := newTestUserServer(t)
		expectPushTeamQuery(mock, rows)
		response, err := (&pushTeamServer{db: users.db}).CheckPushTeamMember(pushTeamContext(pushClientDNSName), &pb.CheckPushTeamMemberRequest{TeamId: 100, UserId: 42})
		if response != nil || status.Code(err) != codes.Unavailable {
			t.Fatalf("bad row accepted: %v %v", response, err)
		}
	}
	users, mock := newTestUserServer(t)
	mock.ExpectQuery(regexp.QuoteMeta(pushTeamQuery)).WithArgs(int64(100), int64(42), model.TeamMembershipActive, 2).WillReturnError(errors.New("private SQL"))
	response, err := (&pushTeamServer{db: users.db}).CheckPushTeamMember(pushTeamContext(pushClientDNSName), &pb.CheckPushTeamMemberRequest{TeamId: 100, UserId: 42})
	if response != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("storage failure accepted: %v %v", response, err)
	}
}
