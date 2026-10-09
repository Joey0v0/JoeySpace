package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func mentionPushTestContext() context.Context {
	leaf := &x509.Certificate{DNSNames: []string{imMentionPushDNSName}}
	return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
		Version: tls.VersionTLS13, PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}},
	}}})
}

func TestIMMentionRejectsPlaintextAndPartialConfig(t *testing.T) {
	s := &imMentionServer{}
	request := &pb.ValidateGroupMentionTargetsRequest{GroupId: 300, Sender: &pb.MentionMemberGeneration{UserId: 42, TeamGeneration: 1}, Targets: []*pb.MentionMemberGeneration{{UserId: 43, TeamGeneration: 1}}}
	if _, err := s.ValidateGroupMentionTargets(context.Background(), request); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("plaintext validation status = %v", err)
	}
	if _, err := loadIMMentionConfig(func(name string) string {
		if name == "IM_MENTION_LISTEN_ON" {
			return "127.0.0.1:9019"
		}
		return ""
	}); err == nil {
		t.Fatal("partial TLS configuration accepted")
	}
	if err := validateIMMentionStartup(imMentionConfig{ListenOn: "127.0.0.1:9019", Files: testMentionFiles()}, "127.0.0.1:9019", "", "", ""); err == nil {
		t.Fatal("shared ordinary port accepted")
	}
}

func testMentionFiles() (files struct{ CertFile, KeyFile, CAFile string }) {
	return struct{ CertFile, KeyFile, CAFile string }{"cert", "key", "ca"}
}

func TestIMMentionRejectsInvalidTargetsBeforeDatabase(t *testing.T) {
	s, mock := testIMServer(t)
	server := &imMentionServer{db: s.db}
	ctx := mentionPushTestContext()
	cases := []*pb.ValidateGroupMentionTargetsRequest{
		{GroupId: 300, Sender: &pb.MentionMemberGeneration{UserId: 42, TeamGeneration: 1}},
		{GroupId: 300, Sender: &pb.MentionMemberGeneration{UserId: 42, TeamGeneration: 1}, Targets: []*pb.MentionMemberGeneration{{UserId: 42, TeamGeneration: 1}}},
		{GroupId: 300, Sender: &pb.MentionMemberGeneration{UserId: 42, TeamGeneration: 1}, Targets: []*pb.MentionMemberGeneration{{UserId: 43, TeamGeneration: 0}}},
	}
	for _, request := range cases {
		if _, err := server.ValidateGroupMentionTargets(ctx, request); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid targets status = %v", err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIMMentionRequiresCurrentGroupGeneration(t *testing.T) {
	s, mock := testIMServer(t)
	server := &imMentionServer{db: s.db}
	request := &pb.ValidateGroupMentionTargetsRequest{GroupId: 300, Sender: &pb.MentionMemberGeneration{UserId: 42, TeamGeneration: 1}, Targets: []*pb.MentionMemberGeneration{{UserId: 43, TeamGeneration: 1}}}
	mock.ExpectQuery("SELECT .*team_id.*FROM .*groups.*").WithArgs(int64(300), 1).WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(int64(200)))
	// The sender's current group membership has already closed, so validation
	// must stop before accepting any target supplied by the Kafka event.
	expectTeamGroupReadFence(mock, 300, 42, 200, nil, false)
	if _, err := server.ValidateGroupMentionTargets(mentionPushTestContext(), request); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("closed sender status = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
