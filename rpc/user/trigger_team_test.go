package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

const triggerTeamQuery = "SELECT users.status FROM `team_members` JOIN users ON users.id = team_members.user_id WHERE team_members.team_id = ? AND team_members.user_id = ? LIMIT ?"
const triggerTeamIMName = "im.go-im.internal"

func triggerTeamIMContext(parent context.Context) context.Context {
	cert := &x509.Certificate{Raw: []byte{1}, DNSNames: []string{triggerTeamIMName}}
	return peer.NewContext(parent, &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
		Version: tls.VersionTLS13, PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}},
	}}})
}

func TestTriggerTeamMemberReturnsOnlyExactCurrentScopeWithoutJWT(t *testing.T) {
	for _, actorID := range []int64{42, 9007199254740993, math.MaxInt64} {
		users, mock := newTestUserServer(t)
		s := &triggerTeamServer{db: users.db, imDNSName: triggerTeamIMName}
		const teamID int64 = 9007199254740995
		mock.ExpectQuery(regexp.QuoteMeta(triggerTeamQuery)).WithArgs(teamID, actorID, 2).
			WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(1))
		ctx := triggerTeamIMContext(context.Background())
		response, err := s.CheckTriggerTeamMember(ctx, &pb.CheckTriggerTeamMemberRequest{ActorId: actorID, TeamId: teamID})
		if err != nil || response.GetActorId() != actorID || response.GetTeamId() != teamID {
			t.Fatalf("scope=%v err=%v", response, err)
		}
		if fields := response.ProtoReflect().Descriptor().Fields(); fields.Len() != 2 {
			t.Fatal("trigger response exposes fields beyond the checked scope")
		}
	}
}

func TestTriggerTeamMemberChecksTLSBeforeRequestsConfigurationOrMetadata(t *testing.T) {
	for _, name := range []string{"plaintext", "metadata", "Agent SAN", "CN only", "wildcard", "old TLS", "unverified chain", "wrong leaf", "empty peer", "wrong configured name"} {
		t.Run(name, func(t *testing.T) {
			users, _ := newTestUserServer(t)
			s := &triggerTeamServer{db: users.db, imDNSName: triggerTeamIMName}
			cert := &x509.Certificate{Raw: []byte{1}, DNSNames: []string{triggerTeamIMName}}
			state := tls.ConnectionState{Version: tls.VersionTLS13, PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}
			ctx := context.Background()
			switch name {
			case "Agent SAN":
				cert.DNSNames = []string{"agent.go-im.internal"}
			case "CN only":
				cert.DNSNames = nil
				cert.Subject = pkix.Name{CommonName: triggerTeamIMName}
			case "wildcard":
				cert.DNSNames = []string{"*.go-im.internal"}
			case "old TLS":
				state.Version = tls.VersionTLS12
			case "unverified chain":
				state.VerifiedChains = nil
			case "wrong leaf":
				state.VerifiedChains = [][]*x509.Certificate{{{Raw: []byte{2}, DNSNames: []string{triggerTeamIMName}}}}
			case "empty peer":
				state.PeerCertificates = nil
			case "wrong configured name":
				s.imDNSName = "other.go-im.internal"
			}
			if name != "plaintext" && name != "metadata" {
				ctx = peer.NewContext(ctx, &peer.Peer{AuthInfo: credentials.TLSInfo{State: state}})
			}
			ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer fake", "actor_id", "42", "team_id", "200", "service_identity", triggerTeamIMName))
			for _, req := range []*pb.CheckTriggerTeamMemberRequest{nil, {ActorId: 42, TeamId: 200}} {
				response, err := s.CheckTriggerTeamMember(ctx, req)
				if response != nil || status.Code(err) != codes.Unauthenticated {
					t.Fatalf("identity accepted: response=%v err=%v", response, err)
				}
			}
		})
	}
}

func TestTriggerTeamMemberRejectsInvalidIDsOrUnavailableConfigurationWithoutSQL(t *testing.T) {
	users, _ := newTestUserServer(t)
	s := &triggerTeamServer{db: users.db, imDNSName: triggerTeamIMName}
	ctx := triggerTeamIMContext(context.Background())
	for _, req := range []*pb.CheckTriggerTeamMemberRequest{nil, {}, {ActorId: -1, TeamId: 200}, {ActorId: 42, TeamId: -1}, {ActorId: 42}, {TeamId: 200}} {
		response, err := s.CheckTriggerTeamMember(ctx, req)
		if response != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid request=%v %v", response, err)
		}
	}
	disabled := &triggerTeamServer{imDNSName: triggerTeamIMName}
	response, err := disabled.CheckTriggerTeamMember(ctx, &pb.CheckTriggerTeamMemberRequest{ActorId: 42, TeamId: 200})
	if response != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("disabled=%v %v", response, err)
	}
	var absent *triggerTeamServer
	response, err = absent.CheckTriggerTeamMember(ctx, nil)
	if response != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("nil server=%v %v", response, err)
	}
	response, err = s.CheckTriggerTeamMember(nil, nil)
	if response != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("nil context=%v %v", response, err)
	}
}

func TestTriggerTeamMemberRejectsMissingDisabledAndDamagedDatabaseResults(t *testing.T) {
	for _, name := range []string{"left team", "disabled", "zero", "negative", "NULL", "not number", "overflow", "multiple", "SQL", "row error"} {
		t.Run(name, func(t *testing.T) {
			users, mock := newTestUserServer(t)
			s := &triggerTeamServer{db: users.db, imDNSName: triggerTeamIMName}
			query := mock.ExpectQuery(regexp.QuoteMeta(triggerTeamQuery)).WithArgs(int64(200), int64(42), 2)
			rows := sqlmock.NewRows([]string{"status"})
			want := codes.Unavailable
			switch name {
			case "left team":
				want = codes.PermissionDenied
			case "disabled":
				rows.AddRow(2)
				want = codes.PermissionDenied
			case "zero":
				rows.AddRow(0)
				want = codes.PermissionDenied
			case "negative":
				rows.AddRow(-1)
				want = codes.PermissionDenied
			case "NULL":
				rows.AddRow(nil)
			case "not number":
				rows.AddRow("active")
			case "overflow":
				rows.AddRow(999)
			case "multiple":
				rows.AddRow(1).AddRow(1)
			case "row error":
				rows.AddRow(1).RowError(0, errors.New("private password / SQL data"))
			}
			if name == "SQL" {
				query.WillReturnError(errors.New("private password / SQL data"))
			} else {
				query.WillReturnRows(rows)
			}
			response, err := s.CheckTriggerTeamMember(triggerTeamIMContext(context.Background()), &pb.CheckTriggerTeamMemberRequest{ActorId: 42, TeamId: 200})
			if response != nil || status.Code(err) != want || strings.Contains(status.Convert(err).Message(), "private") {
				t.Fatalf("database=%v err=%v want=%v", response, err, want)
			}
		})
	}
}

func TestTriggerTeamMemberHonorsCancellationAndDeadline(t *testing.T) {
	for _, expired := range []bool{false, true} {
		users, _ := newTestUserServer(t)
		s := &triggerTeamServer{db: users.db, imDNSName: triggerTeamIMName}
		ctx, cancel := context.WithCancel(context.Background())
		want := codes.Canceled
		if expired {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			want = codes.DeadlineExceeded
		} else {
			cancel()
		}
		response, err := s.CheckTriggerTeamMember(triggerTeamIMContext(ctx), &pb.CheckTriggerTeamMemberRequest{ActorId: 42, TeamId: 200})
		cancel()
		if response != nil || status.Code(err) != want {
			t.Fatalf("canceled request=%v %v", response, err)
		}
	}
	users, mock := newTestUserServer(t)
	s := &triggerTeamServer{db: users.db, imDNSName: triggerTeamIMName}
	mock.ExpectQuery(regexp.QuoteMeta(triggerTeamQuery)).WithArgs(int64(200), int64(42), 2).
		WillDelayFor(100 * time.Millisecond).WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(1))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	response, err := s.CheckTriggerTeamMember(triggerTeamIMContext(ctx), &pb.CheckTriggerTeamMemberRequest{ActorId: 42, TeamId: 200})
	if response != nil || status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("query timeout=%v %v", response, err)
	}
}
