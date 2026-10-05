package agent

import (
	"context"
	"math"
	"net"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

// Runs production result/receipt stores, publisher, consumer and Agent RPC.
// SQL and Kafka are substitutes; reconstructing objects is not a real crash.
func TestTriggerCompletedReplayAndACKRetryRecoverOriginalRunThroughOwnerRPC(t *testing.T) {
	drafts, mock := testDraftStore(t)
	row := executionFixtureRow()
	const runID int64 = math.MaxInt64
	scope := draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 9007199254740995}
	reference := row.ReceivedAt.UnixMilli()
	items := []taskDraft{collectionStoreDraft(), collectionStoreDraft()}
	items[0].Description = "保留待审草稿，不自动创建任务"
	items[1].Title = "检查压测"
	for i := range items {
		items[i].Deadline.InstructionReferenceUnixMs = reference
	}
	key := triggerNotificationKey(row.Event)
	fingerprint := draftCollectionFingerprint(scope.TeamID, scope.GroupID, "整理任务", &reference)
	mock.ExpectBegin()
	expectResultFlowLive(mock, row)
	expectResultFlowDrafts(mock, runID, scope, items, key, fingerprint)
	expectResultFlowLive(mock, row)
	expectResultFlowCompletion(mock, row, runID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if got, err := NewTriggerInboxStore(drafts.db).CompleteDraftCollection(context.Background(), *row.lease(), runID, scope, items, key, fingerprint); err != nil || got != runID {
		t.Fatalf("original collection did not commit: run=%d err=%v", got, err)
	}

	broker := publishedInboxFlowNotification(t)
	broker.commitFailed = true
	for replay := 0; replay < 2; replay++ {
		// Only the immutable receipt is verified; no draft INSERT or inbox
		// UPDATE is permitted once this source has its completed result.
		expectInboxFlowInsert(mock).WillReturnError(&mysql.MySQLError{Number: 1062})
		mock.ExpectQuery(regexp.QuoteMeta(selectTriggerInboxForUpdate)).WithArgs(row.Event.MessageID).
			WillReturnRows(sqlmock.NewRows([]string{"message_id", "action", "event_version", "status", "received_at", "result_run_id"}).
				AddRow(row.Event.MessageID, row.Event.Action, row.Event.Version, TriggerInboxCompleted, row.ReceivedAt, runID))
		mock.ExpectCommit()
		runInboxFlowConsumer(t, NewTriggerInboxStore(drafts.db), broker, mock)
		wantCommits := 1
		if replay == 0 {
			wantCommits = 2
		}
		if broker.fetches != 1 || broker.commits != wantCommits {
			t.Fatalf("ACK retry repeated receipt or advanced source: replay=%d fetches=%d commits=%d", replay, broker.fetches, broker.commits)
		}
		// Same exact publisher bytes/offset, new reader, consumer and store.
		broker = &inboxFlowBroker{message: broker.message}
	}

	// Reconstruct the ordinary RPC-facing Server after notification replay.
	// The authoritative source agrees with the publisher's saved Outbox facts.
	savedSource := &impb.ReadTaskTriggerContextResponse{MessageId: row.Event.MessageID, MsgId: "persisted-source", ActorId: scope.InitiatorID,
		TeamId: scope.TeamID, GroupId: scope.GroupID, Instruction: "整理任务", ReferenceTimeUnixMs: reference, RequestKey: key,
		Messages: []*impb.TeamGroupMessage{{Id: row.Event.MessageID, MsgId: "persisted-source", FromId: scope.InitiatorID,
			ContentType: 1, SenderType: 1, Content: "@AI 整理任务 整理任务", CreatedAtUnixMs: reference}}}
	var identities, sourceReads, groupChecks atomic.Int32
	source := &TriggerContextClient{rpc: triggerContextRPCFunc(func(ctx context.Context, req *impb.ReadTaskTriggerContextRequest) (*impb.ReadTaskTriggerContextResponse, error) {
		sourceReads.Add(1)
		if md, ok := metadata.FromOutgoingContext(ctx); !ok || len(md) != 0 || req.MessageId != row.Event.MessageID {
			t.Errorf("owner identity leaked to restricted source lookup: request=%v metadata=%v", req, md)
		}
		return proto.Clone(savedSource).(*impb.ReadTaskTriggerContextResponse), nil
	})}
	server := NewServer(nil)
	server.draftReader = &draftAccessReader{store: &draftStore{db: drafts.db},
		identity: &draftIdentityResolver{users: draftUserFunc(func(ctx context.Context) (*userpb.GetUserInfoResponse, error) {
			identities.Add(1)
			if md, _ := metadata.FromOutgoingContext(ctx); len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" {
				t.Errorf("owner token was changed: %v", md)
			}
			return &userpb.GetUserInfoResponse{Id: scope.InitiatorID}, nil
		})},
		im: draftAccessFunc(func(ctx context.Context, req *impb.CheckTeamGroupAccessRequest) error {
			groupChecks.Add(1)
			if req.TeamId != scope.TeamID || req.GroupId != scope.GroupID {
				t.Errorf("current group check used wrong scope: %v", req)
			}
			if md, _ := metadata.FromOutgoingContext(ctx); len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" {
				t.Errorf("ordinary group check lost owner token: %v", md)
			}
			return nil
		})}
	server.ConfigureTaskTriggerStatus(source, NewTriggerInboxStore(drafts.db))
	listener := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() { _ = listener.Close() })
	rpcServer := grpc.NewServer()
	pb.RegisterAgentServer(rpcServer, server)
	go func() { _ = rpcServer.Serve(listener) }()
	t.Cleanup(rpcServer.Stop)
	conn, err := grpc.NewClient("passthrough:///trigger-replay-recovery", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := pb.NewAgentClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer user-token", "actor-id", "1", "team-id", "2", "group-id", "3"))
	// Expectations for these reads are added only after consumer ACK checks.
	mock.ExpectQuery(regexp.QuoteMeta(selectTaskTriggerStatus)).WithArgs(row.Event.MessageID).
		WillReturnRows(taskTriggerStatusRows().AddRow(row.Event.MessageID, TriggerInboxCompleted, runID))
	collectionRowsForRun := func() *sqlmock.Rows {
		rows := sqlmock.NewRows(collectionColumns)
		for i, item := range items {
			values := collectionRowValues(i, len(items), item)
			values[0], values[4], values[5], values[6] = runID, scope.TeamID, scope.GroupID, scope.InitiatorID
			rows.AddRow(values...)
		}
		return rows
	}
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftCollectionForInitiator)).WithArgs(runID, scope.InitiatorID).WillReturnRows(collectionRowsForRun())
	response, err := client.GetTaskTriggerStatus(ctx, &pb.GetTaskTriggerStatusRequest{MessageId: row.Event.MessageID})
	if err != nil || response.GetStatus() != TriggerInboxCompleted || response.GetRunId() != runID || response.GetMessageId() != row.Event.MessageID ||
		response.GetTeamId() != scope.TeamID || response.GetGroupId() != scope.GroupID {
		t.Fatalf("replayed source lost original discoverable run: response=%v err=%v", response, err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftCollectionForInitiator)).WithArgs(runID, scope.InitiatorID).WillReturnRows(collectionRowsForRun())
	collection, err := client.GetTaskDraftCollection(ctx, &pb.GetTaskDraftRequest{RunId: response.RunId})
	if err != nil || collection.GetRunId() != runID || collection.GetItemCount() != 2 || len(collection.GetItems()) != 2 ||
		collection.GetTeamId() != scope.TeamID || collection.GetGroupId() != scope.GroupID {
		t.Fatalf("recovered run is not readable: collection=%v err=%v", collection, err)
	}
	for i, item := range collection.Items {
		if item.ItemIndex == nil || item.GetItemIndex() != int32(i) || item.GetStatus() != string(draftWaitingConfirmation) || item.GetTaskId() != 0 || item.GetReplyStatus() != "disabled" ||
			item.GetDraft().GetRevision() != 1 || item.GetDraft().GetTitle() != items[i].Title || item.GetDraft().GetDescription() != items[i].Description ||
			item.GetDraft().GetAssigneeId() != 0 || item.GetDraft().GetAssigneeResolution() != "none" || item.GetDraft().GetDueAtUnixMs() != 0 ||
			item.GetDraft().GetDeadline().GetInstructionReferenceUnixMs() != reference {
			t.Fatalf("replay changed original waiting item %d: %v", i, item)
		}
	}
	if identities.Load() != 3 || sourceReads.Load() != 1 || groupChecks.Load() != 2 {
		t.Fatalf("RPC bypassed current owner/source/group checks: identities=%d source=%d group=%d", identities.Load(), sourceReads.Load(), groupChecks.Load())
	}
}
