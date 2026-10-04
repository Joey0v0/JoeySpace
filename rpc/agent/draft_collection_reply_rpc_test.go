package agent

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type collectionReplyMemory struct {
	records                    map[int32]draftReplyRecord
	prepareErr, acceptErr      error
	prepareResult, loadResult  *draftReplyRecord
	loadErrors                 map[int32]error
	loadFound                  bool
	prepared, loaded, accepted []int32
}

func newCollectionReplyMemory() *collectionReplyMemory {
	return &collectionReplyMemory{records: make(map[int32]draftReplyRecord), loadErrors: make(map[int32]error)}
}

func (s *collectionReplyMemory) prepareCollectionReply(_ context.Context, c taskDraftCollection, index int32) (draftReplyRecord, error) {
	s.prepared = append(s.prepared, index)
	if s.prepareErr != nil {
		return draftReplyRecord{}, s.prepareErr
	}
	if s.prepareResult != nil {
		return *s.prepareResult, nil
	}
	if record, ok := s.records[index]; ok {
		if !record.matchesCollection(c, index) {
			return draftReplyRecord{}, status.Error(codes.AlreadyExists, "changed intent")
		}
		return record, nil
	}
	record, err := collectionReplyIntent(c, index)
	if err == nil {
		s.records[index] = record
	}
	return record, err
}

func (s *collectionReplyMemory) loadCollectionReply(_ context.Context, _ taskDraftCollection, index int32) (draftReplyRecord, bool, error) {
	s.loaded = append(s.loaded, index)
	if err := s.loadErrors[index]; err != nil {
		return draftReplyRecord{}, false, err
	}
	if s.loadResult != nil {
		return *s.loadResult, s.loadFound, nil
	}
	record, found := s.records[index]
	return record, found, nil
}

func (s *collectionReplyMemory) acceptCollectionReply(_ context.Context, record draftReplyRecord) error {
	s.accepted = append(s.accepted, record.ItemIndex)
	if s.acceptErr != nil {
		return s.acceptErr
	}
	record.Accepted = true
	s.records[record.ItemIndex] = record
	return nil
}

type draftItemBotFunc func(context.Context, *impb.PostTaskCreatedCardItemRequest) (*impb.PostTaskCreatedCardResponse, error)

func (f draftItemBotFunc) PostTaskCreatedCardItem(ctx context.Context, req *impb.PostTaskCreatedCardItemRequest, _ ...grpc.CallOption) (*impb.PostTaskCreatedCardResponse, error) {
	return f(ctx, req)
}

func collectionReplyServer(t *testing.T, drafts *collectionConfirmationMemoryStore, replies *collectionReplyMemory, bot draftItemBotFunc) *Server {
	t.Helper()
	s := NewServer(nil)
	s.draftReader = collectionConfirmationReader(t, drafts, nil)
	s.replier = &draftReplier{collectionStore: replies, itemBot: bot,
		bot: draftBotFunc(func(context.Context, *impb.PostTaskCreatedCardRequest) (*impb.PostTaskCreatedCardResponse, error) {
			t.Error("collection reply called legacy IM method")
			return nil, errors.New("legacy method forbidden")
		})}
	return s
}

func succeededCollectionReplyFixture(indices ...int32) taskDraftCollection {
	c := confirmationCollectionFixture()
	for _, index := range indices {
		for int(index) >= len(c.Items) {
			run := c.Items[0]
			run.Draft.Title += strconv.Itoa(len(c.Items))
			c.Items = append(c.Items, run)
		}
		c.Items[index].Status = draftSucceeded
		c.Items[index].TaskRequestKey = draftCollectionTaskRequestKey(c.ID, index)
		c.Items[index].TaskID = 9007199254740993
	}
	return c
}

func TestCollectionConfirmPostsIndependentCardsAndReadsReplayOnly(t *testing.T) {
	drafts := &collectionConfirmationMemoryStore{collection: confirmationCollectionFixture()}
	replies := newCollectionReplyMemory()
	tasks, bots := 0, 0
	bot := draftItemBotFunc(func(ctx context.Context, req *impb.PostTaskCreatedCardItemRequest) (*impb.PostTaskCreatedCardResponse, error) {
		bots++
		md, _ := metadata.FromOutgoingContext(ctx)
		index := req.GetItemIndex()
		record := replies.records[index]
		if req.ItemIndex == nil || req.GetRunId() != 9001 || req.GetTeamId() != 200 || req.GetGroupId() != 300 ||
			req.GetContent() != record.Content || !record.matchesCollection(drafts.snapshot(), index) || record.Accepted ||
			len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" || len(md.Get("idempotency-key")) != 0 {
			t.Errorf("item was not saved or scope/token changed: %v,%+v,%v", req, record, md)
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > draftBotTimeout {
			t.Error("unbounded item IM call")
		}
		return &impb.PostTaskCreatedCardResponse{MsgId: record.MsgID, Accepted: true}, nil
	})
	s := collectionReplyServer(t, drafts, replies, bot)
	s.confirmer = &draftConfirmer{store: drafts, tasks: draftTaskCreateFunc(func(context.Context, *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
		tasks++
		// Message identity must remain independent even if Task IDs coincide.
		return &taskpb.CreateTaskResponse{TaskId: 9007199254740993}, nil
	})}
	client := replyRPCClient(t, s)
	ctx, cancel := draftRPCContext()
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "idempotency-key", "caller-must-not-reach-IM")
	for _, index := range []int32{0, 1} {
		response, err := client.ConfirmTaskDraftItem(ctx, collectionConfirmRequest(drafts.snapshot(), index))
		id, _ := model.BotTaskItemMsgID(9001, index)
		if err != nil || response.GetItem().GetStatus() != "succeeded" || response.GetItem().GetTaskId() != 9007199254740993 ||
			response.GetItem().GetReplyStatus() != "accepted" || response.GetItem().GetReplyMsgId() != id || response.GetItem().GetDraft().GetRevision() != 1 {
			t.Fatalf("confirm %d: %v,%v", index, response, err)
		}
	}
	if tasks != 2 || bots != 2 || replies.records[0].MsgID == replies.records[1].MsgID {
		t.Fatalf("items overlapped: tasks=%d bots=%d records=%v", tasks, bots, replies.records)
	}
	// Failure reading another item must not affect this target's replay/read.
	replies.loadErrors[0] = status.Error(codes.Unavailable, "storage unavailable")
	index := int32(1)
	response, err := client.ConfirmTaskDraftItem(ctx, collectionConfirmRequest(drafts.snapshot(), index))
	if err != nil || response.GetItem().GetReplyStatus() != "accepted" || bots != 2 || tasks != 2 || len(replies.loaded) != 1 || replies.loaded[0] != 1 {
		t.Fatalf("confirmation replay posted/read other item: %v,%v", response, err)
	}
	if response, err := client.GetTaskDraftItem(ctx, &pb.GetTaskDraftItemRequest{RunId: 9001, ItemIndex: &index}); err != nil || response.GetItem().GetReplyStatus() != "accepted" || bots != 2 || tasks != 2 {
		t.Fatalf("item read side effect: %v,%v", response, err)
	}
	if response, err := client.GetTaskDraftCollection(ctx, &pb.GetTaskDraftRequest{RunId: 9001}); response != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("collection read hid storage failure: %v,%v", response, err)
	}
	response, err = client.ConfirmTaskDraftItem(ctx, collectionConfirmRequest(drafts.snapshot(), 0))
	if err != nil || response.GetItem().GetStatus() != "succeeded" || response.GetItem().GetTaskId() != 9007199254740993 || response.GetItem().GetReplyStatus() != "unknown" || response.GetItem().GetReplyMsgId() != "" || tasks != 2 || bots != 2 {
		t.Fatalf("reply read failure hid saved task: %v,%v", response, err)
	}
}

func TestCollectionConfirmationReplyFailuresKeepSavedTaskAndExplicitRetry(t *testing.T) {
	for _, failure := range []string{"IM result lost", "wrong IM ID", "not accepted", "intent save failed", "invalid intent", "acceptance save failed", "new method unsupported"} {
		t.Run(failure, func(t *testing.T) {
			drafts := &collectionConfirmationMemoryStore{collection: confirmationCollectionFixture()}
			replies := newCollectionReplyMemory()
			wantStatus, wantMsg := "pending", "bot-task:9001:1"
			if failure == "intent save failed" {
				replies.prepareErr = status.Error(codes.Unavailable, "private storage detail")
				wantStatus, wantMsg = "unknown", ""
			}
			if failure == "invalid intent" {
				wrong := draftReplyRecord{RunID: 9001, ItemIndex: 0, MsgID: "bot-task:9001", Accepted: true}
				replies.prepareResult = &wrong
				wantStatus, wantMsg = "unknown", ""
			}
			if failure == "acceptance save failed" {
				replies.acceptErr = status.Error(codes.Unavailable, "private save detail")
			}
			tasks, bots := 0, 0
			var firstCard string
			bot := draftItemBotFunc(func(_ context.Context, req *impb.PostTaskCreatedCardItemRequest) (*impb.PostTaskCreatedCardResponse, error) {
				bots++
				if firstCard == "" {
					firstCard = req.GetContent()
				} else if firstCard != req.GetContent() {
					t.Error("retry changed original card")
				}
				if bots == 1 {
					switch failure {
					case "IM result lost":
						return nil, status.Error(codes.DeadlineExceeded, "transport lost")
					case "wrong IM ID":
						return &impb.PostTaskCreatedCardResponse{MsgId: "bot-task:9001", Accepted: true}, nil
					case "not accepted":
						return &impb.PostTaskCreatedCardResponse{MsgId: "bot-task:9001:1"}, nil
					case "new method unsupported":
						return nil, status.Error(codes.Unimplemented, "old IM")
					}
				}
				return &impb.PostTaskCreatedCardResponse{MsgId: "bot-task:9001:1", Accepted: true}, nil
			})
			s := collectionReplyServer(t, drafts, replies, bot)
			s.confirmer = &draftConfirmer{store: drafts, tasks: draftTaskCreateFunc(func(context.Context, *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
				tasks++
				return &taskpb.CreateTaskResponse{TaskId: 7001}, nil
			})}
			client := replyRPCClient(t, s)
			ctx, cancel := draftRPCContext()
			defer cancel()
			req := collectionConfirmRequest(drafts.snapshot(), 1)
			response, err := client.ConfirmTaskDraftItem(ctx, req)
			if err != nil || response.GetItem().GetStatus() != "succeeded" || response.GetItem().GetTaskId() != 7001 || response.GetItem().GetDraft().GetRevision() != 1 || response.GetItem().GetReplyStatus() != wantStatus || response.GetItem().GetReplyMsgId() != wantMsg || tasks != 1 {
				t.Fatalf("saved Task lost: %v,%v", response, err)
			}
			beforeBots := bots
			if _, err := client.ConfirmTaskDraftItem(ctx, req); err != nil || bots != beforeBots || tasks != 1 {
				t.Fatalf("confirm replay resent or recreated: %v", err)
			}
			replies.prepareErr, replies.acceptErr, replies.prepareResult = nil, nil, nil
			// Reconstructed service retains only fixed records and task results.
			s = collectionReplyServer(t, drafts, replies, bot)
			s.draftReader.members = draftMemberFunc(func(context.Context, *userpb.CheckTeamMemberByIDRequest) (*userpb.CheckTeamMemberByIDResponse, error) {
				t.Error("reply retry checked target assignee")
				return nil, errors.New("not used")
			})
			client = replyRPCClient(t, s)
			index := int32(1)
			response, err = client.RetryTaskReplyItem(ctx, &pb.GetTaskDraftItemRequest{RunId: 9001, ItemIndex: &index})
			if err != nil || response.GetItem().GetReplyStatus() != "accepted" || response.GetItem().GetReplyMsgId() != "bot-task:9001:1" || response.GetItem().GetTaskId() != 7001 || tasks != 1 || bots != beforeBots+1 {
				t.Fatalf("explicit retry: %v,%v", response, err)
			}
			if _, err := client.RetryTaskReplyItem(ctx, &pb.GetTaskDraftItemRequest{RunId: 9001, ItemIndex: &index}); err != nil || bots != beforeBots+1 {
				t.Fatalf("accepted replay sent again: %v", err)
			}
		})
	}
}

func TestCollectionReplyReadsOnlySuccessAndNeverPublishes(t *testing.T) {
	c := succeededCollectionReplyFixture(0)
	creating, skipped := c.Items[1], c.Items[1]
	creating.Status, creating.TaskRequestKey = draftCreating, draftCollectionTaskRequestKey(c.ID, 2)
	skipped.Status = draftSkipped
	c.Items = append(c.Items, creating, skipped)
	drafts := &collectionConfirmationMemoryStore{collection: c}
	replies := newCollectionReplyMemory()
	bot := draftItemBotFunc(func(context.Context, *impb.PostTaskCreatedCardItemRequest) (*impb.PostTaskCreatedCardResponse, error) {
		t.Error("GET called IM")
		return nil, nil
	})
	client := replyRPCClient(t, collectionReplyServer(t, drafts, replies, bot))
	ctx, cancel := draftRPCContext()
	defer cancel()
	for _, accepted := range []bool{false, true} {
		record, _ := collectionReplyIntent(c, 0)
		record.Accepted = accepted
		replies.records[0] = record
		response, err := client.GetTaskDraftCollection(ctx, &pb.GetTaskDraftRequest{RunId: c.ID})
		want := "pending"
		if accepted {
			want = "accepted"
		}
		if err != nil || response.Items[0].ReplyStatus != want || response.Items[0].ReplyMsgId != "bot-task:9001" || response.Items[1].ReplyStatus != "not_started" || response.Items[2].ReplyStatus != "not_started" || response.Items[3].ReplyStatus != "disabled" || response.Items[3].ReplyMsgId != "" {
			t.Fatalf("read states: %v,%v", response, err)
		}
	}
	if len(replies.prepared) != 0 || len(replies.accepted) != 0 || len(replies.loaded) != 2 || replies.loaded[0] != 0 || replies.loaded[1] != 0 {
		t.Fatalf("GET side effects/wrong target: %+v", replies)
	}
	delete(replies.records, 0)
	response, err := client.GetTaskDraftCollection(ctx, &pb.GetTaskDraftRequest{RunId: c.ID})
	if err != nil || response.Items[0].ReplyStatus != "not_started" || response.Items[0].ReplyMsgId != "" {
		t.Fatalf("absent reply: %v,%v", response, err)
	}
	record, _ := collectionReplyIntent(c, 0)
	record.Accepted = true
	replies.loadResult, replies.loadFound = &record, false
	if response, err := client.GetTaskDraftCollection(ctx, &pb.GetTaskDraftRequest{RunId: c.ID}); response != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("unfound nonempty record trusted: %v,%v", response, err)
	}
}

func TestCollectionReplyRetryReauthorizesAcceptedAndRejectsNonSuccess(t *testing.T) {
	for _, name := range []string{"missing token", "other initiator", "group revoked", "waiting", "creating", "skipped", "missing index", "negative index", "index five", "outside count", "bad run", "legacy replier", "store only", "bot only"} {
		t.Run(name, func(t *testing.T) {
			c := succeededCollectionReplyFixture(0)
			drafts := &collectionConfirmationMemoryStore{collection: c}
			replies := newCollectionReplyMemory()
			record, _ := collectionReplyIntent(c, 0)
			record.Accepted = true
			replies.records[0] = record
			s := collectionReplyServer(t, drafts, replies, draftItemBotFunc(func(context.Context, *impb.PostTaskCreatedCardItemRequest) (*impb.PostTaskCreatedCardResponse, error) {
				t.Error("rejected retry reached IM")
				return nil, nil
			}))
			s.draftReader.members = draftMemberFunc(func(context.Context, *userpb.CheckTeamMemberByIDRequest) (*userpb.CheckTeamMemberByIDResponse, error) {
				t.Error("reply retry checked target member")
				return nil, nil
			})
			index := int32(0)
			req := &pb.GetTaskDraftItemRequest{RunId: c.ID, ItemIndex: &index}
			ctx, cancel := draftRPCContext()
			defer cancel()
			want := codes.InvalidArgument
			switch name {
			case "missing token":
				ctx, want = context.Background(), codes.Unauthenticated
			case "other initiator":
				s.draftReader.identity.users = draftUserFunc(func(context.Context) (*userpb.GetUserInfoResponse, error) {
					return &userpb.GetUserInfoResponse{Id: 401}, nil
				})
				want = codes.NotFound
			case "group revoked":
				s.draftReader.im = draftAccessFunc(func(context.Context, *impb.CheckTeamGroupAccessRequest) error {
					return status.Error(codes.PermissionDenied, "left group")
				})
				want = codes.PermissionDenied
			case "waiting", "creating", "skipped":
				run := &drafts.collection.Items[0]
				run.TaskID = 0
				run.TaskRequestKey = ""
				run.Status = draftWaitingConfirmation
				if name == "creating" {
					run.Status, run.TaskRequestKey = draftCreating, draftCollectionTaskRequestKey(c.ID, 0)
				} else if name == "skipped" {
					run.Status = draftSkipped
				}
				want = codes.FailedPrecondition
			case "missing index":
				req.ItemIndex = nil
			case "negative index":
				index = -1
			case "index five":
				index = 5
			case "outside count":
				index, want = 4, codes.NotFound
			case "bad run":
				req.RunId = 0
			case "legacy replier":
				s.replier = &draftReplier{}
				want = codes.Unavailable
			case "store only":
				s.replier.itemBot = nil
				want = codes.Unavailable
			case "bot only":
				s.replier.collectionStore = nil
				want = codes.Unavailable
			}
			response, err := replyRPCClient(t, s).RetryTaskReplyItem(ctx, req)
			if response != nil || status.Code(err) != want || len(replies.prepared) != 0 || len(replies.accepted) != 0 || len(replies.loaded) != 0 {
				t.Fatalf("rejected retry: %v,%v store=%+v", response, err, replies)
			}
		})
	}
}

func TestCollectionReplyRejectsUnknownRecordEvenWhenMarkedAccepted(t *testing.T) {
	c := succeededCollectionReplyFixture(1)
	for _, accepted := range []bool{false, true} {
		for _, change := range []string{"run", "index", "task", "team", "group", "initiator", "msg_id", "content", "missing"} {
			t.Run(strconv.FormatBool(accepted)+"/"+change, func(t *testing.T) {
				record, _ := collectionReplyIntent(c, 1)
				switch change {
				case "run":
					record.RunID++
				case "index":
					record.ItemIndex = 0
				case "task":
					record.TaskID++
				case "team":
					record.TeamID++
				case "group":
					record.GroupID++
				case "initiator":
					record.InitiatorID++
				case "msg_id":
					record.MsgID = "bot-task:9001"
				case "content":
					record.Content, _ = model.EncodeTaskCreatedCard(99, "换正文")
				case "missing":
					record = draftReplyRecord{}
				}
				record.Accepted = accepted
				replies := newCollectionReplyMemory()
				replies.prepareResult, replies.loadResult, replies.loadFound = &record, &record, true
				s := collectionReplyServer(t, &collectionConfirmationMemoryStore{collection: c}, replies, draftItemBotFunc(func(context.Context, *impb.PostTaskCreatedCardItemRequest) (*impb.PostTaskCreatedCardResponse, error) {
					t.Error("unknown record reached IM")
					return nil, nil
				}))
				if result, err := s.replier.attemptCollection(context.Background(), "user-token", c, 1); status.Code(err) != codes.Unavailable || result != (draftReplyRecord{}) || len(replies.accepted) != 0 {
					t.Fatalf("unknown preparation trusted: %+v,%v", result, err)
				}
				index := int32(1)
				ctx, cancel := draftRPCContext()
				defer cancel()
				if response, err := replyRPCClient(t, s).GetTaskDraftItem(ctx, &pb.GetTaskDraftItemRequest{RunId: c.ID, ItemIndex: &index}); response != nil || status.Code(err) != codes.Unavailable {
					t.Fatalf("unknown read trusted: %v,%v", response, err)
				}
			})
		}
	}
}

func TestCollectionReplyIndexFourAndBoundedAttempt(t *testing.T) {
	c := succeededCollectionReplyFixture(4)
	replies := newCollectionReplyMemory()
	bots := 0
	s := collectionReplyServer(t, &collectionConfirmationMemoryStore{collection: c}, replies, draftItemBotFunc(func(ctx context.Context, req *impb.PostTaskCreatedCardItemRequest) (*impb.PostTaskCreatedCardResponse, error) {
		bots++
		if req.ItemIndex == nil || req.GetItemIndex() != 4 || req.GetContent() != replies.records[4].Content {
			t.Errorf("wrong item: %v", req)
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > draftBotTimeout {
			t.Error("unbounded IM phase")
		}
		return &impb.PostTaskCreatedCardResponse{MsgId: "bot-task:9001:4", Accepted: true}, nil
	}))
	index := int32(4)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer user-token"))
	response, err := s.RetryTaskReplyItem(ctx, &pb.GetTaskDraftItemRequest{RunId: c.ID, ItemIndex: &index})
	if err != nil || response.GetItemCount() != 5 || response.GetItem().GetReplyMsgId() != "bot-task:9001:4" || response.GetItem().GetReplyStatus() != "accepted" || bots != 1 || len(replies.records) != 1 {
		t.Fatalf("index four: %v,%v", response, err)
	}
	// A canceled attempt may have a known pending intent, but cannot call IM
	// or mark it accepted merely because a store substitute ignores context.
	delete(replies.records, 4)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := s.replier.attemptCollection(canceled, "user-token", c, 4)
	if status.Code(err) != codes.Canceled || result.Accepted || !result.matchesCollection(c, 4) || bots != 1 {
		t.Fatalf("canceled attempt: %+v,%v", result, err)
	}
}

func TestCollectionReplyRejectsMissingAcceptanceAndNeverFallsBack(t *testing.T) {
	c := succeededCollectionReplyFixture(0)
	for _, response := range []*impb.PostTaskCreatedCardResponse{nil, {MsgId: "bot-task:9001", Accepted: false}, {MsgId: "bot-task:9001:1", Accepted: true}} {
		replies := newCollectionReplyMemory()
		s := collectionReplyServer(t, &collectionConfirmationMemoryStore{collection: c}, replies, draftItemBotFunc(func(context.Context, *impb.PostTaskCreatedCardItemRequest) (*impb.PostTaskCreatedCardResponse, error) {
			return response, nil
		}))
		if record, err := s.replier.attemptCollection(context.Background(), "user-token", c, 0); status.Code(err) != codes.Unavailable || record.Accepted || len(replies.accepted) != 0 || !record.matchesCollection(c, 0) {
			t.Fatalf("wrong acceptance trusted: %+v,%v", record, err)
		}
	}
	replies := newCollectionReplyMemory()
	s := collectionReplyServer(t, &collectionConfirmationMemoryStore{collection: c}, replies, draftItemBotFunc(func(context.Context, *impb.PostTaskCreatedCardItemRequest) (*impb.PostTaskCreatedCardResponse, error) {
		return nil, status.Error(codes.Unimplemented, "older IM")
	}))
	if record, err := s.replier.attemptCollection(context.Background(), "user-token", c, 0); status.Code(err) != codes.Unavailable || record.Accepted || len(replies.accepted) != 0 {
		t.Fatalf("unsupported item method: %+v,%v", record, err)
	}
}
