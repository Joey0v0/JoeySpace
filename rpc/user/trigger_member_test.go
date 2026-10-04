package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func newTriggerMemberTestServer(t *testing.T) (*triggerTeamServer, sqlmock.Sqlmock) {
	t.Helper()
	users, mock := newTestUserServer(t)
	return &triggerTeamServer{db: users.db, imDNSName: triggerTeamIMName}, mock
}

func triggerMemberRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "username", "nickname"})
}

func expectTriggerMemberQualification(mock sqlmock.Sqlmock, req *pb.ResolveTriggerTeamMemberRequest, rows *sqlmock.Rows) {
	mock.ExpectQuery(regexp.QuoteMeta(triggerTeamQuery)).WithArgs(req.TeamId, req.ActorId, 2).WillReturnRows(rows)
}

func expectTriggerMemberQuery(mock sqlmock.Sqlmock, req *pb.ResolveTriggerTeamMemberRequest) *sqlmock.ExpectedQuery {
	return mock.ExpectQuery(regexp.QuoteMeta(resolveTriggerMemberQuery)).WithArgs(req.TeamId, req.Name, req.Name, req.TeamId, req.ActorId)
}

func TestResolveTriggerMemberLiteralMatchesEchoScopeAndIgnoreMetadata(t *testing.T) {
	for _, name := range []string{"username", "nickname", "cross field collision", "empty", "maximum strings and IDs"} {
		t.Run(name, func(t *testing.T) {
			s, mock := newTriggerMemberTestServer(t)
			req := &pb.ResolveTriggerTeamMemberRequest{ActorId: 9007199254740995, TeamId: 9007199254740997, Name: "张三"}
			rows := triggerMemberRows()
			want := []int64{9007199254740993}
			switch name {
			case "username":
				rows.AddRow(want[0], req.Name, "")
			case "nickname":
				rows.AddRow(want[0], "zhangsan", req.Name)
			case "cross field collision":
				want = append(want, math.MaxInt64)
				rows.AddRow(want[0], "other", req.Name).AddRow(want[1], req.Name, "other")
			case "empty":
				want = nil
			case "maximum strings and IDs":
				req.Name = strings.Repeat("张", 64)
				want[0] = math.MaxInt64
				rows.AddRow(want[0], req.Name, strings.Repeat("三", 64))
			}
			expectTriggerMemberQualification(mock, req, sqlmock.NewRows([]string{"status"}).AddRow(1))
			expectTriggerMemberQuery(mock, req).WillReturnRows(rows).RowsWillBeClosed()
			expectTriggerMemberQualification(mock, req, sqlmock.NewRows([]string{"status"}).AddRow(1))
			ctx := metadata.NewIncomingContext(triggerTeamIMContext(context.Background()), metadata.Pairs("authorization", "Bearer fake", "actor_id", "11", "team_id", "12", "name", "evil"))
			response, err := s.ResolveTriggerTeamMember(ctx, req)
			if err != nil || response.GetActorId() != req.ActorId || response.GetTeamId() != req.TeamId || response.GetName() != req.Name || response.GetTruncated() || len(response.GetCandidates()) != len(want) {
				t.Fatalf("response=%v err=%v", response, err)
			}
			for i, candidate := range response.Candidates {
				if candidate.UserId != want[i] || !model.ValidAgentTriggerMemberCandidate(candidate.UserId, candidate.Username, candidate.Nickname, req.Name) {
					t.Fatalf("candidate=%v want=%d", candidate, want[i])
				}
			}
			if response.ProtoReflect().Descriptor().Fields().Len() != 5 || (&pb.TriggerTeamMemberCandidate{}).ProtoReflect().Descriptor().Fields().Len() != 3 || proto.Size(response) > model.AgentTriggerMemberResponseLimit {
				t.Fatal("response must expose only bounded scope and candidate fields")
			}
		})
	}
}

func TestResolveTriggerMemberTwentyOneRowsOnlyMarksTruncated(t *testing.T) {
	for _, count := range []int{20, 21} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			s, mock := newTriggerMemberTestServer(t)
			req := &pb.ResolveTriggerTeamMemberRequest{ActorId: 42, TeamId: 200, Name: "张三"}
			rows := triggerMemberRows()
			for i := 1; i <= count; i++ {
				rows.AddRow(i, "username", req.Name)
			}
			expectTriggerMemberQualification(mock, req, sqlmock.NewRows([]string{"status"}).AddRow(1))
			expectTriggerMemberQuery(mock, req).WillReturnRows(rows).RowsWillBeClosed()
			expectTriggerMemberQualification(mock, req, sqlmock.NewRows([]string{"status"}).AddRow(1))
			response, err := s.ResolveTriggerTeamMember(triggerTeamIMContext(context.Background()), req)
			if err != nil || len(response.GetCandidates()) != 20 || response.GetTruncated() != (count == 21) || response.GetCandidates()[19].UserId != 20 {
				t.Fatalf("count=%d response=%v err=%v", count, response, err)
			}
		})
	}
}

func TestResolveTriggerMemberRejectsEveryMalformedCandidateIncludingLast(t *testing.T) {
	for _, name := range []string{"NULL ID", "NULL username", "NULL nickname", "zero ID", "negative ID", "ID overflow", "duplicate", "descending", "mismatch", "empty username", "long username", "long nickname", "bad username UTF8", "bad nickname UTF8", "case mismatch", "21st NULL", "21st mismatch", "22 rows", "row error"} {
		t.Run(name, func(t *testing.T) {
			s, mock := newTriggerMemberTestServer(t)
			req := &pb.ResolveTriggerTeamMemberRequest{ActorId: 42, TeamId: 200, Name: "张三"}
			rows := triggerMemberRows()
			switch name {
			case "NULL ID":
				rows.AddRow(nil, req.Name, "")
			case "NULL username":
				rows.AddRow(1, nil, req.Name)
			case "NULL nickname":
				rows.AddRow(1, req.Name, nil)
			case "zero ID":
				rows.AddRow(0, req.Name, "")
			case "negative ID":
				rows.AddRow(-1, req.Name, "")
			case "ID overflow":
				rows.AddRow("9223372036854775808", req.Name, "")
			case "duplicate":
				rows.AddRow(1, req.Name, "").AddRow(1, req.Name, "")
			case "descending":
				rows.AddRow(2, req.Name, "").AddRow(1, req.Name, "")
			case "mismatch":
				rows.AddRow(1, "张三四", "小张")
			case "empty username":
				rows.AddRow(1, "", req.Name)
			case "long username":
				rows.AddRow(1, strings.Repeat("x", 65), req.Name)
			case "long nickname":
				rows.AddRow(1, req.Name, strings.Repeat("张", 65))
			case "bad username UTF8":
				rows.AddRow(1, string([]byte{0xff}), req.Name)
			case "bad nickname UTF8":
				rows.AddRow(1, req.Name, string([]byte{0xff}))
			case "case mismatch":
				req.Name = "Alice"
				rows.AddRow(1, "alice", "ALICE")
			case "21st NULL", "21st mismatch", "22 rows":
				for i := 1; i <= 20; i++ {
					rows.AddRow(i, req.Name, "")
				}
				if name == "21st NULL" {
					rows.AddRow(21, req.Name, nil)
				} else if name == "21st mismatch" {
					rows.AddRow(21, "not matched", "")
				} else {
					rows.AddRow(21, req.Name, "").AddRow(22, req.Name, "")
				}
			case "row error":
				rows.AddRow(1, req.Name, "").RowError(0, errors.New("private name and SQL"))
			}
			expectTriggerMemberQualification(mock, req, sqlmock.NewRows([]string{"status"}).AddRow(1))
			expectTriggerMemberQuery(mock, req).WillReturnRows(rows).RowsWillBeClosed()
			response, err := s.ResolveTriggerTeamMember(triggerTeamIMContext(context.Background()), req)
			if response != nil || status.Code(err) != codes.Unavailable || strings.Contains(status.Convert(err).Message(), req.Name) || strings.Contains(status.Convert(err).Message(), "private") {
				t.Fatalf("damaged candidate returned partial result: %v %v", response, err)
			}
		})
	}
}

func TestResolveTriggerMemberTLSIdentityRequiredBeforeAllSQL(t *testing.T) {
	for _, name := range []string{"plaintext", "metadata", "Agent SAN", "CN only", "wildcard", "TLS12", "unverified", "wrong leaf", "nil context"} {
		t.Run(name, func(t *testing.T) {
			s, _ := newTriggerMemberTestServer(t)
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
			case "TLS12":
				state.Version = tls.VersionTLS12
			case "unverified":
				state.VerifiedChains = nil
			case "wrong leaf":
				state.VerifiedChains = [][]*x509.Certificate{{{Raw: []byte{2}, DNSNames: []string{triggerTeamIMName}}}}
			}
			if name != "plaintext" && name != "metadata" {
				ctx = peer.NewContext(ctx, &peer.Peer{AuthInfo: credentials.TLSInfo{State: state}})
			}
			ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer fake", "service_identity", triggerTeamIMName))
			if name == "nil context" {
				ctx = nil
			}
			for _, req := range []*pb.ResolveTriggerTeamMemberRequest{nil, {ActorId: 42, TeamId: 200, Name: "张三"}} {
				response, err := s.ResolveTriggerTeamMember(ctx, req)
				if response != nil || status.Code(err) != codes.Unauthenticated {
					t.Fatalf("unauthenticated response=%v err=%v", response, err)
				}
			}
		})
	}
}

func TestResolveTriggerMemberInvalidRequestAndConfigurationWithoutSQL(t *testing.T) {
	s, _ := newTriggerMemberTestServer(t)
	ctx := triggerTeamIMContext(context.Background())
	for _, req := range []*pb.ResolveTriggerTeamMemberRequest{nil, {}, {ActorId: -1, TeamId: 200, Name: "张三"}, {ActorId: 42, Name: "张三"}, {ActorId: 42, TeamId: -1, Name: "张三"}, {ActorId: 42, TeamId: 200}} {
		response, err := s.ResolveTriggerTeamMember(ctx, req)
		if response != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid request=%v %v", response, err)
		}
	}
	for _, name := range []string{" ", " 张三", "张三 ", "张三\n", strings.Repeat("张", 65), string([]byte{0xff})} {
		response, err := s.ResolveTriggerTeamMember(ctx, &pb.ResolveTriggerTeamMemberRequest{ActorId: 42, TeamId: 200, Name: name})
		if response != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid name response=%v err=%v", response, err)
		}
	}
	response, err := (&triggerTeamServer{imDNSName: triggerTeamIMName}).ResolveTriggerTeamMember(ctx, &pb.ResolveTriggerTeamMemberRequest{ActorId: 42, TeamId: 200, Name: "张三"})
	if response != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("disabled response=%v err=%v", response, err)
	}
	var absent *triggerTeamServer
	response, err = absent.ResolveTriggerTeamMember(ctx, nil)
	if response != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("nil server response=%v err=%v", response, err)
	}
}

func TestResolveTriggerMemberChecksQualificationBeforeAndAfterCandidateQuery(t *testing.T) {
	for _, name := range []string{"left before", "disabled before", "left after empty", "disabled after empty", "left after match", "disabled after match", "qualification SQL before", "qualification SQL after"} {
		t.Run(name, func(t *testing.T) {
			s, mock := newTriggerMemberTestServer(t)
			req := &pb.ResolveTriggerTeamMemberRequest{ActorId: 42, TeamId: 200, Name: "张三"}
			before := strings.HasSuffix(name, "before")
			if !before {
				expectTriggerMemberQualification(mock, req, sqlmock.NewRows([]string{"status"}).AddRow(1))
				rows := triggerMemberRows()
				if strings.HasSuffix(name, "match") {
					rows.AddRow(1, req.Name, "")
				}
				expectTriggerMemberQuery(mock, req).WillReturnRows(rows).RowsWillBeClosed()
			}
			query := mock.ExpectQuery(regexp.QuoteMeta(triggerTeamQuery)).WithArgs(req.TeamId, req.ActorId, 2)
			want := codes.PermissionDenied
			if strings.HasPrefix(name, "qualification SQL") {
				want = codes.Unavailable
				query.WillReturnError(errors.New("private SQL and name"))
			} else {
				rows := sqlmock.NewRows([]string{"status"})
				if strings.HasPrefix(name, "disabled") {
					rows.AddRow(2)
				}
				query.WillReturnRows(rows)
			}
			response, err := s.ResolveTriggerTeamMember(triggerTeamIMContext(context.Background()), req)
			if response != nil || status.Code(err) != want || strings.Contains(status.Convert(err).Message(), "private") {
				t.Fatalf("qualification response=%v err=%v want=%v", response, err, want)
			}
		})
	}
}

func TestResolveTriggerMemberDatabaseFailureCancellationAndDeadline(t *testing.T) {
	for _, name := range []string{"SQL", "canceled", "deadline", "candidate deadline", "final check deadline"} {
		t.Run(name, func(t *testing.T) {
			s, mock := newTriggerMemberTestServer(t)
			req := &pb.ResolveTriggerTeamMemberRequest{ActorId: 42, TeamId: 200, Name: "张三"}
			ctx := context.Background()
			cancel := func() {}
			want := codes.Unavailable
			switch name {
			case "canceled":
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				req.Name = ""
				want = codes.Canceled
			case "deadline":
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				req.ActorId = 0
				want = codes.DeadlineExceeded
			default:
				expectTriggerMemberQualification(mock, req, sqlmock.NewRows([]string{"status"}).AddRow(1))
				query := expectTriggerMemberQuery(mock, req)
				if name == "SQL" {
					query.WillReturnError(errors.New("private SQL " + req.Name))
				} else {
					ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
					want = codes.DeadlineExceeded
					if name == "candidate deadline" {
						query.WillDelayFor(100 * time.Millisecond).WillReturnRows(triggerMemberRows())
					} else {
						query.WillReturnRows(triggerMemberRows()).RowsWillBeClosed()
						mock.ExpectQuery(regexp.QuoteMeta(triggerTeamQuery)).WithArgs(req.TeamId, req.ActorId, 2).
							WillDelayFor(100 * time.Millisecond).WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(1))
					}
				}
			}
			defer cancel()
			response, err := s.ResolveTriggerTeamMember(triggerTeamIMContext(ctx), req)
			if response != nil || status.Code(err) != want || strings.Contains(status.Convert(err).Message(), "private") || (req.Name != "" && strings.Contains(status.Convert(err).Message(), req.Name)) {
				t.Fatalf("response=%v err=%v want=%v", response, err, want)
			}
		})
	}
}

func TestResolveTriggerMemberSQLKeepsCandidateAndActorRestrictions(t *testing.T) {
	for _, clause := range []string{"team_members.team_id = ? AND users.status = 1", "CAST(users.username AS BINARY) = CAST(? AS BINARY) OR CAST(users.nickname AS BINARY) = CAST(? AS BINARY)", "EXISTS (SELECT 1", "actor_members.team_id = ? AND actor_members.user_id = ? AND actor_user.status = 1", "ORDER BY users.id ASC LIMIT 21"} {
		if !strings.Contains(resolveTriggerMemberQuery, clause) {
			t.Fatalf("missing fixed SQL restriction %q", clause)
		}
	}
}
