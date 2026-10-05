package agent

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/bwmarrin/snowflake"
	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Every mutation delegates to the production store. Only lease entropy is
// fixed for SQL expectations, and completion/status calls are counted so that
// an unexpected failed SQL call cannot hide behind a later successful Release.
type revocationRecoveryStore struct {
	*TriggerInboxStore
	token                                      string
	onEmpty                                    func()
	claims, releases, completions, statusReads atomic.Int32
}

func (s *revocationRecoveryStore) Claim(ctx context.Context) (*TriggerLease, error) {
	s.claims.Add(1)
	lease, err := s.claimWithToken(ctx, func() (string, error) { return s.token, nil })
	if err == nil && lease == nil && s.onEmpty != nil {
		s.onEmpty()
	}
	return lease, err
}
func (s *revocationRecoveryStore) Release(ctx context.Context, lease TriggerLease) error {
	err := s.TriggerInboxStore.Release(ctx, lease)
	if err == nil {
		s.releases.Add(1)
	}
	return err
}
func (s *revocationRecoveryStore) CompleteDraftCollection(ctx context.Context, lease TriggerLease, runID int64, scope draftRunScope, drafts []taskDraft, key, fingerprint string) (int64, error) {
	s.completions.Add(1)
	return s.TriggerInboxStore.CompleteDraftCollection(ctx, lease, runID, scope, drafts, key, fingerprint)
}
func (s *revocationRecoveryStore) loadTaskTriggerStatus(ctx context.Context, messageID int64) (taskTriggerStatus, error) {
	s.statusReads.Add(1)
	return s.TriggerInboxStore.loadTaskTriggerStatus(ctx, messageID)
}

func expectRevocationRecoveryClaim(mock sqlmock.Sqlmock, queued, running triggerExecutionRow) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(selectClaimableTriggerLease)).WillReturnRows(executionFixtureRows(queued))
	mock.ExpectExec(regexp.QuoteMeta(claimTriggerLeaseSQL)).WithArgs(running.Token.String, queued.Event.MessageID, TriggerInboxQueued, 0, 0, "").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(running.Event.MessageID, running.Token.String).WillReturnRows(executionFixtureRows(running))
	mock.ExpectCommit()
}

func revocationRecoveryStatusClient(t *testing.T, server *Server) pb.AgentClient {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rpcServer := grpc.NewServer()
	pb.RegisterAgentServer(rpcServer, server)
	go func() { _ = rpcServer.Serve(listener) }()
	t.Cleanup(func() { rpcServer.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewAgentClient(conn)
}

// Real Worker -> production Processor -> actual local mTLS source RPC and
// production SQL store transactions; then actual TCP Agent status RPC against
// the same source/store. IM authorization and User identity are RPC substitutes,
// the model is a barrier-controlled substitute, and SQL substitutes for MySQL.
// This proves composition and write intent, not real DB timing/concurrency.
func TestTriggerRevocationRecoveryWorkerAndStatusShareCurrentAuthorization(t *testing.T) {
	for _, when := range []string{"before_first_read", "during_model"} {
		t.Run(when, func(t *testing.T) {
			drafts, mock := testDraftStore(t)
			facts := validTriggerClientResponse(triggerClientSourceID)
			queued := executionFixtureRow()
			queued.Event.MessageID = facts.MessageId
			queued.Status, queued.Token, queued.Until = TriggerInboxQueued, sql.NullString{}, sql.NullTime{}
			queued.ModelAttempts, queued.ModelStarted = 0, 0
			running := queued
			running.Status, running.Token, running.Until = TriggerInboxRunning, sql.NullString{String: strings.Repeat("b", 64), Valid: true}, sql.NullTime{Time: time.Now().Add(30 * time.Second), Valid: true}
			expectRevocationRecoveryClaim(mock, queued, running)
			spent := running
			if when == "during_model" {
				mock.ExpectBegin()
				mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(running.Event.MessageID, running.Token.String).WillReturnRows(executionFixtureRows(running))
				mock.ExpectExec(regexp.QuoteMeta(beginTriggerModel)).WithArgs(running.Event.MessageID, running.Token.String, TriggerModelAttemptLimit).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
				spent.ModelAttempts, spent.ModelStarted = 1, 1
			}
			// No run/item/completion write is expected. Failure returns to queued
			// with DB-relative 30-second backoff, preserving 0 or 1 model calls.
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(spent.Event.MessageID, spent.Token.String).WillReturnRows(executionFixtureRows(spent))
			mock.ExpectExec(regexp.QuoteMeta(releaseTriggerLease)).WithArgs(TriggerInboxQueued, TriggerInboxQueued, 30, 1, spent.Event.MessageID, spent.Token.String, 0, spent.ModelAttempts).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(selectClaimableTriggerLease)).WillReturnRows(sqlmock.NewRows(executionFixtureColumns))
			mock.ExpectCommit()

			var revoked, identityDenied atomic.Bool
			var sourceCalls, models, identities atomic.Int32
			revoked.Store(when == "before_first_read")
			certs := botClientCertificates(t)
			address := startTriggerContextTLSStub(t, certs["im.go-im.internal"], true, func(ctx context.Context, req *impb.ReadTaskTriggerContextRequest) (*impb.ReadTaskTriggerContextResponse, error) {
				if err := rpcauth.RequireServiceIdentity(ctx, "agent.go-im.internal"); err != nil {
					return nil, err
				}
				md, _ := metadata.FromIncomingContext(ctx)
				for _, key := range []string{"authorization", "actor-id", "team-id", "group-id"} {
					if len(md.Get(key)) != 0 {
						t.Errorf("caller identity reached service source: %s", key)
					}
				}
				if req.MessageId != facts.MessageId {
					t.Error("source ID changed")
				}
				sourceCalls.Add(1)
				if revoked.Load() {
					return nil, status.Error(codes.PermissionDenied, "private current membership revoked")
				}
				return proto.Clone(facts).(*impb.ReadTaskTriggerContextResponse), nil
			})
			source, err := NewTriggerContextClient(triggerContextEnvironment(address, "im.go-im.internal", certs["agent.go-im.internal"]))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = source.Close() })
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			store := &revocationRecoveryStore{TriggerInboxStore: NewTriggerInboxStore(drafts.db), token: running.Token.String, onEmpty: cancel}
			modelEntered, modelResume := make(chan struct{}), make(chan struct{})
			var resumeOnce sync.Once
			resume := func() { resumeOnce.Do(func() { close(modelResume) }) }
			t.Cleanup(resume)
			server := NewServer(nil)
			node, err := snowflake.NewNode(5)
			if err != nil {
				t.Fatal(err)
			}
			server.preparer = &draftPreparer{idNode: node, generator: collectionGeneratorFunc(func(ctx context.Context, instruction string, messages []*impb.TeamGroupMessage) ([]taskDraft, error) {
				models.Add(1)
				if instruction != facts.Instruction || messages[0].Id != facts.MessageId {
					t.Error("model did not use saved source")
				}
				close(modelEntered)
				select {
				case <-modelResume:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return []taskDraft{{Title: "跟进发布", Deadline: draftDeadlineMetadata{Source: "none"}}}, nil
			})}
			processor, err := NewTriggerTaskProcessor(server, source, store.TriggerInboxStore)
			if err != nil {
				t.Fatal(err)
			}
			processor.(*triggerTaskProcessor).store = store // record calls; writes still delegate unchanged
			worker, err := NewTriggerWorker(store, processor)
			if err != nil {
				t.Fatal(err)
			}
			worker.renewInterval = time.Hour // no renewal timing claim in this bounded flow
			done, finished := make(chan error, 1), make(chan struct{})
			go func() { defer close(finished); done <- worker.Run(ctx) }()
			t.Cleanup(func() {
				cancel()
				resume()
				select {
				case <-finished:
				case <-time.After(2 * time.Second):
					t.Error("canceled recovery worker did not stop")
				}
			})
			if when == "during_model" {
				select {
				case <-modelEntered:
				case <-time.After(2 * time.Second):
					t.Fatal("model not reached after committed permission")
				}
				revoked.Store(true)
				resume()
			}
			if err := waitTestWorker(t, done); !errors.Is(err, context.Canceled) {
				t.Fatalf("worker did not release then reach empty queue: %v", err)
			}
			wantModels, wantReads := int32(0), int32(1)
			if when == "during_model" {
				wantModels, wantReads = 1, 2
			}
			if models.Load() != wantModels || sourceCalls.Load() != wantReads || store.claims.Load() != 2 || store.releases.Load() != 1 || store.completions.Load() != 0 {
				t.Fatalf("revoked source generated/saved/retried: models=%d reads=%d claims=%d releases=%d completions=%d", models.Load(), sourceCalls.Load(), store.claims.Load(), store.releases.Load(), store.completions.Load())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}

			server.draftReader = &draftAccessReader{identity: &draftIdentityResolver{users: draftUserFunc(func(ctx context.Context) (*userpb.GetUserInfoResponse, error) {
				identities.Add(1)
				md, _ := metadata.FromOutgoingContext(ctx)
				if values := md.Get("authorization"); len(values) != 1 || values[0] != "Bearer person-token" {
					t.Error("person status token changed")
				}
				if identityDenied.Load() {
					return nil, status.Error(codes.Unauthenticated, "private expired user token")
				}
				return &userpb.GetUserInfoResponse{Id: facts.ActorId}, nil
			})}, store: drafts}
			server.ConfigureTaskTriggerStatus(source, store.TriggerInboxStore)
			server.triggerStatus.store = store
			client := revocationRecoveryStatusClient(t, server)
			statusCall := func() (*pb.GetTaskTriggerStatusResponse, error) {
				callCtx, stop := context.WithTimeout(context.Background(), time.Second)
				defer stop()
				callCtx = metadata.NewOutgoingContext(callCtx, metadata.Pairs("authorization", "Bearer person-token", "actor-id", "1"))
				return client.GetTaskTriggerStatus(callCtx, &pb.GetTaskTriggerStatusRequest{MessageId: facts.MessageId})
			}
			result, err := statusCall()
			if result != nil || status.Code(err) != codes.PermissionDenied || strings.Contains(err.Error(), "private") || store.statusReads.Load() != 0 || sourceCalls.Load() != wantReads+1 {
				t.Fatalf("revoked status exposed/read result: %v %v", result, err)
			}
			// A bad person credential is a separate status-only boundary; the
			// background worker never consumes it as its authorization input.
			revoked.Store(false)
			identityDenied.Store(true)
			result, err = statusCall()
			if result != nil || status.Code(err) != codes.Unauthenticated || strings.Contains(err.Error(), "private") || store.statusReads.Load() != 0 || sourceCalls.Load() != wantReads+1 {
				t.Fatalf("invalid person token reached source/status: %v %v", result, err)
			}
			identityDenied.Store(false)
			mock.ExpectQuery(regexp.QuoteMeta(selectTaskTriggerStatus)).WithArgs(facts.MessageId).WillReturnRows(sqlmock.NewRows([]string{"message_id", "status", "result_run_id"}).AddRow(facts.MessageId, TriggerInboxQueued, nil))
			result, err = statusCall()
			if err != nil || result.GetStatus() != TriggerInboxQueued || result.GetRunId() != 0 || result.GetMessageId() != facts.MessageId || result.GetTeamId() != facts.TeamId || result.GetGroupId() != facts.GroupId || store.statusReads.Load() != 1 || sourceCalls.Load() != wantReads+2 || identities.Load() != 3 || models.Load() != wantModels || store.completions.Load() != 0 {
				t.Fatalf("restored read reset execution or changed persisted scope: %v %v", result, err)
			}
		})
	}
}

var _ TriggerExecutionStore = (*revocationRecoveryStore)(nil)
var _ triggerTaskResultStore = (*revocationRecoveryStore)(nil)
var _ taskTriggerStatusLoader = (*revocationRecoveryStore)(nil)
