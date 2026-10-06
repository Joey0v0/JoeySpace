package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql/driver"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type triggerContextTeamFunc func(context.Context, int64, int64) error

func (f triggerContextTeamFunc) Check(ctx context.Context, actorID, teamID int64) error {
	return f(ctx, actorID, teamID)
}

func (f triggerContextTeamFunc) CheckGeneration(ctx context.Context, actorID, teamID int64) (int64, error) {
	if err := f(ctx, actorID, teamID); err != nil {
		return 0, err
	}
	return 1, nil
}

func triggerContextFixture() (model.AgentTriggerOutbox, model.Message) {
	created := time.Date(2026, 10, 4, 2, 0, 0, 0, time.UTC)
	source := model.Message{ID: 9007199254740993, MsgID: "trigger-source", FromID: 9007199254740995, ToID: 300,
		SenderType: model.MessageSenderUser, ChatType: 2, ContentType: 1, Content: "@AI 整理任务 整理上线清单", CreatedAt: created}
	r := model.AgentTriggerOutbox{MessageID: source.ID, Action: model.AgentTriggerAction, EventVersion: 1, MsgID: source.MsgID,
		ActorID: source.FromID, TeamID: 200, GroupID: source.ToID, Instruction: "整理上线清单", ReferenceTimeMS: created.UnixMilli()}
	return r, source
}

var triggerContextOutboxColumns = []string{"message_id", "action", "event_version", "msg_id", "actor_id", "team_id", "group_id", "instruction", "reference_time_ms", "published"}
var triggerContextMessageColumns = []string{"id", "msg_id", "from_id", "sender_type", "initiator_id", "to_id", "chat_type", "content_type", "content", "created_at"}

func triggerContextOutboxValues(r model.AgentTriggerOutbox) []driver.Value {
	return []driver.Value{r.MessageID, r.Action, int64(r.EventVersion), r.MsgID, r.ActorID, r.TeamID, r.GroupID, r.Instruction, r.ReferenceTimeMS, r.Published}
}

func triggerContextOutboxRows(records ...model.AgentTriggerOutbox) *sqlmock.Rows {
	rows := sqlmock.NewRows(triggerContextOutboxColumns)
	for _, r := range records {
		rows.AddRow(triggerContextOutboxValues(r)...)
	}
	return rows
}

func triggerContextMessageValues(m model.Message) []driver.Value {
	return []driver.Value{m.ID, m.MsgID, m.FromID, int64(m.SenderType), m.InitiatorID, m.ToID, int64(m.ChatType), int64(m.ContentType), m.Content, m.CreatedAt}
}

func triggerContextMessageRows(messages ...model.Message) *sqlmock.Rows {
	rows := sqlmock.NewRows(triggerContextMessageColumns)
	for _, m := range messages {
		rows.AddRow(triggerContextMessageValues(m)...)
	}
	return rows
}

func triggerContextAgent(ctx context.Context) context.Context {
	cert := &x509.Certificate{Raw: []byte{1}, DNSNames: []string{"agent.go-im.internal"}}
	return peer.NewContext(ctx, &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
		Version: tls.VersionTLS13, PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}},
	}}})
}

func expectTriggerContextSource(mock sqlmock.Sqlmock, r model.AgentTriggerOutbox, source model.Message) {
	mock.ExpectQuery(regexp.QuoteMeta(triggerOutboxQuery)).WithArgs(r.MessageID).WillReturnRows(triggerContextOutboxRows(r))
	mock.ExpectQuery(regexp.QuoteMeta(triggerSourceQuery)).WithArgs(r.MessageID).WillReturnRows(triggerContextMessageRows(source))
}

func expectTriggerContextMembership(mock sqlmock.Sqlmock, r model.AgentTriggerOutbox) {
	mock.ExpectQuery(regexp.QuoteMeta(triggerMembershipQuery)).WithArgs(r.GroupID, r.TeamID, r.ActorID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "team_id", "user_id"}).AddRow(r.GroupID, r.TeamID, r.ActorID))
}

func expectTriggerContextHistory(mock sqlmock.Sqlmock, r model.AgentTriggerOutbox, rows *sqlmock.Rows) {
	expectTeamGroupReadFence(mock, r.GroupID, r.ActorID, r.TeamID, nil, true)
	mock.ExpectQuery(regexp.QuoteMeta(triggerHistoryQuery)).WithArgs(r.TeamID, r.ActorID, r.GroupID, r.MessageID).WillReturnRows(rows)
}

func TestTriggerContextDerivesPersistedScopeAndBoundedHistoryWithoutJWT(t *testing.T) {
	for _, published := range []bool{false, true} {
		im, mock := testIMServer(t)
		r, source := triggerContextFixture()
		r.Published = published
		source.SenderType = 0
		expectTriggerContextSource(mock, r, source)
		expectTriggerContextMembership(mock, r)
		earlier := source
		earlier.ID--
		earlier.MsgID = "earlier-message"
		earlier.Content = "另一个讨论"
		earlier.SenderType = model.MessageSenderBot
		earlier.InitiatorID = 42
		earlier.CreatedAt = earlier.CreatedAt.Add(time.Hour) // An ID bound is not a timestamp bound.
		expectTriggerContextHistory(mock, r, triggerContextMessageRows(source, earlier))
		expectTeamGroupReadFence(mock, r.GroupID, r.ActorID, r.TeamID, nil, true)
		calls := 0
		s := &triggerContextServer{db: im.db, agentDNSName: "agent.go-im.internal", teams: triggerContextTeamFunc(func(ctx context.Context, actorID, teamID int64) error {
			calls++
			if actorID != r.ActorID || teamID != r.TeamID {
				t.Fatal("qualification trusted supplied metadata instead of storage")
			}
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > triggerContextTimeout {
				t.Fatal("missing total request budget")
			}
			return nil
		})}
		ctx := metadata.NewIncomingContext(triggerContextAgent(context.Background()), metadata.Pairs("actor_id", "1", "team_id", "2", "group_id", "3", "authorization", "Bearer forged"))
		response, err := s.ReadTaskTriggerContext(ctx, &pb.ReadTaskTriggerContextRequest{MessageId: r.MessageID})
		if err != nil || response.GetMessageId() != r.MessageID || response.GetMsgId() != r.MsgID || response.GetActorId() != r.ActorID || response.GetTeamId() != r.TeamID ||
			response.GetGroupId() != r.GroupID || response.GetInstruction() != r.Instruction || response.GetReferenceTimeUnixMs() != r.ReferenceTimeMS || response.GetRequestKey() != r.RequestKey() || len(response.GetMessages()) != 2 || calls != 2 {
			t.Fatalf("context=%v err=%v checks=%d", response, err, calls)
		}
		if response.Messages[0].GetSenderType() != 1 || response.Messages[1].GetSenderType() != 2 || response.Messages[1].GetInitiatorId() != 42 {
			t.Fatal("message identities not preserved")
		}
	}
}

func TestTriggerContextAcceptsTwentyTextBoundedMessagesBelowResponseLimit(t *testing.T) {
	im, mock := testIMServer(t)
	r, source := triggerContextFixture()
	expectTriggerContextSource(mock, r, source)
	expectTriggerContextMembership(mock, r)
	messages := []model.Message{source}
	for i := 1; i < 20; i++ {
		m := source
		m.ID -= int64(i)
		m.MsgID = "earlier-" + strings.Repeat("x", i)
		m.Content = strings.Repeat("x", 65535)
		messages = append(messages, m)
	}
	expectTriggerContextHistory(mock, r, triggerContextMessageRows(messages...))
	expectTeamGroupReadFence(mock, r.GroupID, r.ActorID, r.TeamID, nil, true)
	s := &triggerContextServer{db: im.db, agentDNSName: "agent.go-im.internal", teams: triggerContextTeamFunc(func(context.Context, int64, int64) error { return nil })}
	response, err := s.ReadTaskTriggerContext(triggerContextAgent(context.Background()), &pb.ReadTaskTriggerContextRequest{MessageId: r.MessageID})
	if err != nil || len(response.GetMessages()) != 20 || proto.Size(response) > triggerContextMaxBytes {
		t.Fatalf("bounded context=%v messages=%d", err, len(response.GetMessages()))
	}
}

func TestTriggerContextRejectsInvalidServiceAndInputBeforeSQL(t *testing.T) {
	for _, name := range []string{"plaintext", "metadata only", "IM SAN", "CN only", "TLS12", "unverified", "nil request", "bad ID", "missing database", "missing team client", "nil server", "nil context"} {
		t.Run(name, func(t *testing.T) {
			im, _ := testIMServer(t)
			s := &triggerContextServer{db: im.db, agentDNSName: "agent.go-im.internal", teams: triggerContextTeamFunc(func(context.Context, int64, int64) error { t.Fatal("invalid request consulted User"); return nil })}
			ctx := triggerContextAgent(context.Background())
			req := &pb.ReadTaskTriggerContextRequest{MessageId: 9007199254740993}
			want := codes.Unauthenticated
			switch name {
			case "plaintext", "metadata only":
				ctx = metadata.NewIncomingContext(context.Background(), metadata.Pairs("service_identity", "agent.go-im.internal", "authorization", "Bearer fake"))
			case "IM SAN", "CN only", "TLS12", "unverified":
				cert := &x509.Certificate{Raw: []byte{1}, DNSNames: []string{"agent.go-im.internal"}}
				state := tls.ConnectionState{Version: tls.VersionTLS13, PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}
				if name == "IM SAN" {
					cert.DNSNames = []string{"im.go-im.internal"}
				}
				if name == "CN only" {
					cert.DNSNames = nil
					cert.Subject = pkix.Name{CommonName: "agent.go-im.internal"}
				}
				if name == "TLS12" {
					state.Version = tls.VersionTLS12
				}
				if name == "unverified" {
					state.VerifiedChains = nil
				}
				ctx = peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: state}})
			case "nil request":
				req = nil
				want = codes.InvalidArgument
			case "bad ID":
				req.MessageId = -1
				want = codes.InvalidArgument
			case "missing database":
				s.db = nil
				want = codes.Unavailable
			case "missing team client":
				s.teams = nil
				want = codes.Unavailable
			case "nil server":
				s = nil
			case "nil context":
				ctx = nil
			}
			response, err := s.ReadTaskTriggerContext(ctx, req)
			if response != nil || status.Code(err) != want {
				t.Fatalf("response=%v err=%v want=%v", response, err, want)
			}
		})
	}
}

func TestTriggerContextRejectsMissingInvalidOrDuplicateOutboxBeforeSource(t *testing.T) {
	for _, name := range []string{"missing", "different ID", "bad version", "bad action", "zero actor", "zero time", "duplicate", "NULL", "SQL"} {
		t.Run(name, func(t *testing.T) {
			im, mock := testIMServer(t)
			r, _ := triggerContextFixture()
			requested := r.MessageID
			want := codes.Unavailable
			switch name {
			case "missing":
				want = codes.NotFound
			case "different ID":
				r.MessageID++
			case "bad version":
				r.EventVersion = 2
			case "bad action":
				r.Action = "other"
			case "zero actor":
				r.ActorID = 0
			case "zero time":
				r.ReferenceTimeMS = 0
			}
			rows := triggerContextOutboxRows(r)
			if name == "missing" {
				rows = triggerContextOutboxRows()
			} else if name == "duplicate" {
				rows = triggerContextOutboxRows(r, r)
			} else if name == "NULL" {
				values := triggerContextOutboxValues(r)
				values[9] = nil
				rows = sqlmock.NewRows(triggerContextOutboxColumns).AddRow(values...)
			}
			query := mock.ExpectQuery(regexp.QuoteMeta(triggerOutboxQuery)).WithArgs(requested)
			if name == "SQL" {
				query.WillReturnError(errors.New("private SQL trigger content"))
			} else {
				query.WillReturnRows(rows)
			}
			s := &triggerContextServer{db: im.db, agentDNSName: "agent.go-im.internal", teams: triggerContextTeamFunc(func(context.Context, int64, int64) error { t.Fatal("bad facts consulted User"); return nil })}
			response, err := s.ReadTaskTriggerContext(triggerContextAgent(context.Background()), &pb.ReadTaskTriggerContextRequest{MessageId: requested})
			if response != nil || status.Code(err) != want || strings.Contains(status.Convert(err).Message(), "private") {
				t.Fatalf("outbox=%v,%v", response, err)
			}
		})
	}
}

func TestTriggerContextRejectsSourceFactOrReferenceMismatchBeforeQualification(t *testing.T) {
	for _, name := range []string{"missing", "duplicate", "ID", "MsgID", "actor", "group", "instruction", "time", "bot", "initiator", "private", "nontext", "NULL", "SQL"} {
		t.Run(name, func(t *testing.T) {
			im, mock := testIMServer(t)
			r, source := triggerContextFixture()
			want := codes.Unavailable
			switch name {
			case "missing":
				want = codes.NotFound
			case "ID":
				source.ID++
			case "MsgID":
				source.MsgID = "different"
			case "actor":
				source.FromID++
			case "group":
				source.ToID++
			case "instruction":
				source.Content = "@AI 整理任务 不同内容"
			case "time":
				source.CreatedAt = source.CreatedAt.Add(time.Second)
			case "bot":
				source.SenderType = 2
				source.InitiatorID = 42
			case "initiator":
				source.InitiatorID = 42
			case "private":
				source.ChatType = 1
			case "nontext":
				source.ContentType = 2
			}
			mock.ExpectQuery(regexp.QuoteMeta(triggerOutboxQuery)).WithArgs(r.MessageID).WillReturnRows(triggerContextOutboxRows(r))
			rows := triggerContextMessageRows(source)
			if name == "missing" {
				rows = triggerContextMessageRows()
			} else if name == "duplicate" {
				rows = triggerContextMessageRows(source, source)
			} else if name == "NULL" {
				values := triggerContextMessageValues(source)
				values[3] = nil
				rows = sqlmock.NewRows(triggerContextMessageColumns).AddRow(values...)
			}
			query := mock.ExpectQuery(regexp.QuoteMeta(triggerSourceQuery)).WithArgs(r.MessageID)
			if name == "SQL" {
				query.WillReturnError(errors.New("private SQL"))
			} else {
				query.WillReturnRows(rows)
			}
			s := &triggerContextServer{db: im.db, agentDNSName: "agent.go-im.internal", teams: triggerContextTeamFunc(func(context.Context, int64, int64) error { t.Fatal("bad source consulted User"); return nil })}
			response, err := s.ReadTaskTriggerContext(triggerContextAgent(context.Background()), &pb.ReadTaskTriggerContextRequest{MessageId: r.MessageID})
			if response != nil || status.Code(err) != want {
				t.Fatalf("source=%v,%v want=%v", response, err, want)
			}
		})
	}
}

func TestTriggerContextRequiresCurrentGroupAndCurrentUserQualification(t *testing.T) {
	for _, name := range []string{"left group", "group team changed", "duplicate membership", "NULL membership", "SQL membership", "left team", "User unauthorized", "User unavailable", "User canceled", "User deadline"} {
		t.Run(name, func(t *testing.T) {
			im, mock := testIMServer(t)
			r, source := triggerContextFixture()
			expectTriggerContextSource(mock, r, source)
			rows := sqlmock.NewRows([]string{"id", "team_id", "user_id"}).AddRow(r.GroupID, r.TeamID, r.ActorID)
			want := codes.Unavailable
			switch name {
			case "left group":
				rows = sqlmock.NewRows([]string{"id", "team_id", "user_id"})
				want = codes.PermissionDenied
			case "group team changed":
				rows = sqlmock.NewRows([]string{"id", "team_id", "user_id"}).AddRow(r.GroupID, 201, r.ActorID)
				want = codes.PermissionDenied
			case "duplicate membership":
				rows.AddRow(r.GroupID, r.TeamID, r.ActorID)
			case "NULL membership":
				rows = sqlmock.NewRows([]string{"id", "team_id", "user_id"}).AddRow(r.GroupID, nil, r.ActorID)
			case "left team":
				want = codes.PermissionDenied
			case "User canceled":
				want = codes.Canceled
			case "User deadline":
				want = codes.DeadlineExceeded
			}
			query := mock.ExpectQuery(regexp.QuoteMeta(triggerMembershipQuery)).WithArgs(r.GroupID, r.TeamID, r.ActorID)
			if name == "SQL membership" {
				query.WillReturnError(errors.New("private SQL"))
			} else {
				query.WillReturnRows(rows)
			}
			calls := 0
			s := &triggerContextServer{db: im.db, agentDNSName: "agent.go-im.internal", teams: triggerContextTeamFunc(func(context.Context, int64, int64) error {
				calls++
				code := codes.Unavailable
				if name == "left team" {
					code = codes.PermissionDenied
				} else if name == "User unauthorized" {
					code = codes.Unauthenticated
				} else if name == "User canceled" {
					code = codes.Canceled
				} else if name == "User deadline" {
					code = codes.DeadlineExceeded
				}
				return status.Error(code, "private User detail")
			})}
			response, err := s.ReadTaskTriggerContext(triggerContextAgent(context.Background()), &pb.ReadTaskTriggerContextRequest{MessageId: r.MessageID})
			userStage := name == "left team" || strings.HasPrefix(name, "User")
			if response != nil || status.Code(err) != want || (calls != 0) != userStage || strings.Contains(status.Convert(err).Message(), "private") {
				t.Fatalf("qualification=%v,%v calls=%d", response, err, calls)
			}
		})
	}
}

func TestTriggerContextRechecksHistorySourceAndRejectsInvalidMessages(t *testing.T) {
	for _, name := range []string{"empty after revocation", "changed source", "changed timestamp", "missing source", "duplicate ID", "duplicate MsgID", "above bound", "wrong group", "bad sender", "user initiator", "bot no initiator", "bad type", "invalid UTF8", "TEXT overflow", "NULL", "21 rows", "SQL"} {
		t.Run(name, func(t *testing.T) {
			im, mock := testIMServer(t)
			r, source := triggerContextFixture()
			expectTriggerContextSource(mock, r, source)
			expectTriggerContextMembership(mock, r)
			older := source
			older.ID--
			older.MsgID = "earlier"
			older.Content = "older discussion"
			messages := []model.Message{source, older}
			want := codes.Unavailable
			switch name {
			case "empty after revocation":
				messages = nil
				want = codes.PermissionDenied
			case "changed source":
				messages[0].Content = "@AI 整理任务 改动"
			case "changed timestamp":
				messages[0].CreatedAt = messages[0].CreatedAt.Add(time.Second)
			case "missing source":
				messages = messages[1:]
			case "duplicate ID":
				messages[1].ID = source.ID
			case "duplicate MsgID":
				messages[1].MsgID = source.MsgID
			case "above bound":
				messages[1].ID = source.ID + 1
			case "wrong group":
				messages[1].ToID++
			case "bad sender":
				messages[1].SenderType = 3
			case "user initiator":
				messages[1].InitiatorID = 42
			case "bot no initiator":
				messages[1].SenderType = 2
			case "bad type":
				messages[1].ContentType = 5
			case "invalid UTF8":
				messages[1].Content = string([]byte{0xff})
			case "TEXT overflow":
				messages[1].Content = strings.Repeat("x", 65536)
			case "21 rows":
				for len(messages) < 21 {
					messages = append(messages, older)
				}
			}
			rows := triggerContextMessageRows(messages...)
			if name == "NULL" {
				values := triggerContextMessageValues(source)
				values[9] = nil
				rows = sqlmock.NewRows(triggerContextMessageColumns).AddRow(values...)
			}
			expectTeamGroupReadFence(mock, r.GroupID, r.ActorID, r.TeamID, nil, true)
			query := mock.ExpectQuery(regexp.QuoteMeta(triggerHistoryQuery)).WithArgs(r.TeamID, r.ActorID, r.GroupID, r.MessageID)
			if name == "SQL" {
				query.WillReturnError(errors.New("private SQL"))
			} else {
				query.WillReturnRows(rows)
			}
			s := &triggerContextServer{db: im.db, agentDNSName: "agent.go-im.internal", teams: triggerContextTeamFunc(func(context.Context, int64, int64) error { return nil })}
			response, err := s.ReadTaskTriggerContext(triggerContextAgent(context.Background()), &pb.ReadTaskTriggerContextRequest{MessageId: r.MessageID})
			if response != nil || status.Code(err) != want {
				t.Fatalf("history=%v,%v want=%v", response, err, want)
			}
		})
	}
}

func TestTriggerContextPreservesCanceledAndDeadlineWithoutLeakingData(t *testing.T) {
	for _, expired := range []bool{false, true} {
		im, _ := testIMServer(t)
		ctx, cancel := context.WithCancel(context.Background())
		want := codes.Canceled
		if expired {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			want = codes.DeadlineExceeded
		} else {
			cancel()
		}
		s := &triggerContextServer{db: im.db, agentDNSName: "agent.go-im.internal", teams: triggerContextTeamFunc(func(context.Context, int64, int64) error { t.Fatal("canceled request checked User"); return nil })}
		response, err := s.ReadTaskTriggerContext(triggerContextAgent(ctx), &pb.ReadTaskTriggerContextRequest{MessageId: 9007199254740993})
		cancel()
		if response != nil || status.Code(err) != want {
			t.Fatalf("context=%v %v", response, err)
		}
	}
	im, mock := testIMServer(t)
	r, _ := triggerContextFixture()
	mock.ExpectQuery(regexp.QuoteMeta(triggerOutboxQuery)).WithArgs(r.MessageID).WillDelayFor(100 * time.Millisecond).WillReturnRows(triggerContextOutboxRows(r))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	s := &triggerContextServer{db: im.db, agentDNSName: "agent.go-im.internal", teams: triggerContextTeamFunc(func(context.Context, int64, int64) error { t.Fatal("expired query checked User"); return nil })}
	response, err := s.ReadTaskTriggerContext(triggerContextAgent(ctx), &pb.ReadTaskTriggerContextRequest{MessageId: r.MessageID})
	if response != nil || status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("SQL cancellation=%v %v", response, err)
	}
}
