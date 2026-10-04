package agent

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	driver "github.com/go-sql-driver/mysql"
	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/push"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Exercises the existing publisher's actual JSON/Key with the new receiver.
type inboxFlowOutbox struct {
	row model.AgentTriggerOutbox
}

func (s *inboxFlowOutbox) ListPending(context.Context, int) ([]model.AgentTriggerOutbox, error) {
	return []model.AgentTriggerOutbox{s.row}, nil
}
func (s *inboxFlowOutbox) MarkPublished(_ context.Context, row model.AgentTriggerOutbox) error {
	if row != s.row {
		return errors.New("publisher changed saved scope")
	}
	s.row.Published = true
	return nil
}

type inboxFlowBroker struct {
	message      kafka.Message
	fetches      int
	commits      int
	closeCalls   int
	commitFailed bool
	committed    func()
}

func (b *inboxFlowBroker) WriteMessages(_ context.Context, messages ...kafka.Message) error {
	if len(messages) != 1 {
		return errors.New("expected one source notification")
	}
	b.message = messages[0]
	b.message.Topic, b.message.Partition, b.message.Offset = "agent_task_triggers", 0, 18
	return nil
}
func (b *inboxFlowBroker) FetchMessage(context.Context) (kafka.Message, error) {
	b.fetches++
	return b.message, nil
}
func (b *inboxFlowBroker) CommitMessages(_ context.Context, messages ...kafka.Message) error {
	b.commits++
	if len(messages) != 1 || messages[0].Offset != b.message.Offset {
		return errors.New("wrong offset acknowledgement")
	}
	if b.commitFailed {
		b.commitFailed = false
		return errors.New("private broker commit detail")
	}
	b.committed()
	return nil
}
func (b *inboxFlowBroker) Close() error { b.closeCalls++; return nil }

func publishedInboxFlowNotification(t *testing.T) *inboxFlowBroker {
	t.Helper()
	source := &inboxFlowOutbox{row: model.AgentTriggerOutbox{MessageID: 9007199254740993, MsgID: "persisted-source", ActorID: 9007199254740995,
		TeamID: 200, GroupID: 300, Action: model.AgentTriggerAction, EventVersion: model.AgentTriggerVersion, Instruction: "整理任务",
		ReferenceTimeMS: time.Date(2026, 10, 4, 2, 0, 0, 0, time.UTC).UnixMilli()}}
	b := &inboxFlowBroker{}
	if err := push.NewAgentTriggerPublisher(source, b, nil).RunOnce(context.Background()); err != nil || !source.row.Published {
		t.Fatalf("production publisher did not acknowledge: %v", err)
	}
	return b
}

func expectInboxFlowInsert(mock sqlmock.Sqlmock) *sqlmock.ExpectedExec {
	mock.ExpectBegin()
	return mock.ExpectExec(regexp.QuoteMeta(insertTriggerInbox)).WithArgs(int64(9007199254740993), model.AgentTriggerAction, model.AgentTriggerVersion, TriggerInboxQueued)
}

func expectInboxFlowReplay(mock sqlmock.Sqlmock) {
	expectInboxFlowInsert(mock).WillReturnError(&driver.MySQLError{Number: 1062, Message: "same source already saved"})
	mock.ExpectQuery(regexp.QuoteMeta(selectTriggerInboxForUpdate)).WithArgs(int64(9007199254740993)).WillReturnRows(sqlmock.NewRows([]string{
		"message_id", "action", "event_version", "status", "received_at",
	}).AddRow(int64(9007199254740993), model.AgentTriggerAction, model.AgentTriggerVersion, TriggerInboxQueued, time.Date(2026, 10, 4, 3, 0, 0, 0, time.UTC)))
	mock.ExpectCommit()
}

func runInboxFlowConsumer(t *testing.T, store *TriggerInboxStore, broker *inboxFlowBroker, mock sqlmock.Sqlmock) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	broker.committed = func() {
		// Even COMMIT failures must recover durable receipt before Kafka commits.
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Kafka acknowledged before required SQL: %v", err)
		}
		cancel()
	}
	consumer, err := NewTriggerConsumer(broker, store)
	if err != nil {
		t.Fatal(err)
	}
	consumer.retryDelay = time.Millisecond
	err = consumer.Run(ctx)
	if !errors.Is(err, context.Canceled) && status.Code(err) != codes.Canceled {
		t.Fatalf("consumer did not stop after receipt: %v", err)
	}
	if err := consumer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := consumer.Close(); err != nil || broker.closeCalls != 1 {
		t.Fatalf("reader close was not idempotent: %v calls=%d", err, broker.closeCalls)
	}
}

func TestTriggerInboxFlowPublisherSQLFailureCommitRetryAndRestartReplay(t *testing.T) {
	drafts, mock := testDraftStore(t)
	store := NewTriggerInboxStore(drafts.db)
	b := publishedInboxFlowNotification(t)
	b.commitFailed = true
	expectInboxFlowInsert(mock).WillReturnError(errors.New("private SQL failure"))
	mock.ExpectRollback()
	expectInboxFlowInsert(mock).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	runInboxFlowConsumer(t, store, b, mock)
	if b.fetches != 1 || b.commits != 2 {
		t.Fatalf("advanced receipt or repeated persistence on commit retry: fetch=%d commit=%d", b.fetches, b.commits)
	}
	// A process may restart after DB commit but before learning Kafka ACK.
	replay := &inboxFlowBroker{message: b.message}
	expectInboxFlowReplay(mock)
	runInboxFlowConsumer(t, store, replay, mock)
	if replay.fetches != 1 || replay.commits != 1 {
		t.Fatalf("replay did not preserve source identity: fetch=%d commit=%d", replay.fetches, replay.commits)
	}
}

func TestTriggerInboxFlowUncertainSQLCommitRequiresVerifiedDuplicateBeforeKafkaACK(t *testing.T) {
	drafts, mock := testDraftStore(t)
	b := publishedInboxFlowNotification(t)
	expectInboxFlowInsert(mock).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(errors.New("uncertain commit result"))
	expectInboxFlowReplay(mock)
	runInboxFlowConsumer(t, NewTriggerInboxStore(drafts.db), b, mock)
	if b.fetches != 1 || b.commits != 1 {
		t.Fatalf("uncertain SQL result advanced offset: fetch=%d commit=%d", b.fetches, b.commits)
	}
}

func TestTriggerInboxFlowInvalidSavedFactStopsWithoutAcknowledging(t *testing.T) {
	drafts, mock := testDraftStore(t)
	b := publishedInboxFlowNotification(t)
	expectInboxFlowInsert(mock).WillReturnError(&driver.MySQLError{Number: 1062})
	mock.ExpectQuery(regexp.QuoteMeta(selectTriggerInboxForUpdate)).WithArgs(int64(9007199254740993)).WillReturnRows(sqlmock.NewRows([]string{
		"message_id", "action", "event_version", "status", "received_at",
	}).AddRow(int64(9007199254740993), model.AgentTriggerAction, 2, TriggerInboxQueued, time.Now()))
	mock.ExpectRollback()
	consumer, err := NewTriggerConsumer(b, NewTriggerInboxStore(drafts.db))
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := consumer.Run(ctx); !errors.Is(err, ErrInvalidTriggerInbox) || b.commits != 0 || b.fetches != 1 {
		t.Fatalf("invalid persisted identity was skipped: %v fetch=%d commit=%d", err, b.fetches, b.commits)
	}
}

func TestTriggerInboxMigrationMatchesInitialization(t *testing.T) {
	migration, err := os.ReadFile("../../deploy/mysql/migrations/023_agent_trigger_inbox.sql")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := os.ReadFile("../../deploy/mysql/init.sql")
	if err != nil {
		t.Fatal(err)
	}
	table := regexp.MustCompile(`(?s)CREATE TABLE agent_task_trigger_inbox \(.*?\) ENGINE=InnoDB;`)
	normalize := func(text []byte) string { return table.FindString(strings.ReplaceAll(string(text), "\r\n", "\n")) }
	if upgraded, fresh := normalize(migration), normalize(initial); upgraded == "" || upgraded != fresh {
		t.Fatalf("fresh/upgrade trigger inbox definitions differ: upgrade=%q fresh=%q", upgraded, fresh)
	}
}
