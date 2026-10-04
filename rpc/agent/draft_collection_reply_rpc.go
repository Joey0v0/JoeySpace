package agent

import (
	"context"

	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func (r *draftReplier) collectionEnabled() bool {
	return r != nil && r.collectionStore != nil && r.itemBot != nil
}

// attemptCollection publishes only the immutable intent for this committed
// item. It never calls Task or the old run-level IM method.
func (r *draftReplier) attemptCollection(ctx context.Context, token string, collection taskDraftCollection, index int32) (draftReplyRecord, error) {
	if !r.collectionEnabled() {
		return draftReplyRecord{}, status.Error(codes.Unavailable, "item bot reply is not configured")
	}
	if _, err := collectionReplyIntent(collection, index); err != nil {
		return draftReplyRecord{}, err
	}
	replyCtx, cancel := context.WithTimeout(ctx, draftReplyTimeout)
	defer cancel()
	record, err := r.collectionStore.prepareCollectionReply(replyCtx, collection, index)
	if err != nil {
		return draftReplyRecord{}, err
	}
	// Do not let an unknown/corrupt store result become pending or accepted.
	if !record.matchesCollection(collection, index) {
		return draftReplyRecord{}, status.Error(codes.Unavailable, "stored item reply is invalid")
	}
	if replyCtx.Err() != nil {
		return record, status.FromContextError(replyCtx.Err()).Err()
	}
	if record.Accepted {
		return record, nil
	}
	botCtx, stop := context.WithTimeout(replyCtx, draftBotTimeout)
	botCtx = metadata.NewOutgoingContext(botCtx, metadata.Pairs("authorization", "Bearer "+token))
	itemIndex := record.ItemIndex
	result, err := r.itemBot.PostTaskCreatedCardItem(botCtx, &impb.PostTaskCreatedCardItemRequest{
		RunId: record.RunID, ItemIndex: &itemIndex, TeamId: record.TeamID, GroupId: record.GroupID, Content: record.Content,
	})
	botContextErr := botCtx.Err()
	stop()
	if botContextErr != nil {
		return record, status.FromContextError(botContextErr).Err()
	}
	if err != nil {
		switch status.Code(err) {
		case codes.Unauthenticated, codes.PermissionDenied, codes.NotFound, codes.FailedPrecondition, codes.AlreadyExists:
			return record, status.Error(status.Code(err), "item bot reply rejected; reload before retrying")
		default:
			return record, status.Error(codes.Unavailable, "item bot reply result uncertain; reload then retry the same item")
		}
	}
	if result == nil || !result.GetAccepted() || result.GetMsgId() != record.MsgID {
		return record, status.Error(codes.Unavailable, "item bot reply returned no matching acceptance")
	}
	if err := r.collectionStore.acceptCollectionReply(replyCtx, record); err != nil {
		return record, err
	}
	record.Accepted = true
	return record, nil
}

func applyDraftCollectionReply(item *pb.TaskDraftCollectionItem, record draftReplyRecord, found bool) {
	item.ReplyStatus, item.ReplyMsgId = "not_started", ""
	if found {
		item.ReplyStatus, item.ReplyMsgId = "pending", record.MsgID
		if record.Accepted {
			item.ReplyStatus = "accepted"
		}
	}
}

// A nil target loads every successful item's reply; a concrete target only
// loads that item. Reading never prepares an intent or contacts IM/Task.
func (s *Server) taskDraftCollectionResponseWithReplies(ctx context.Context, collection taskDraftCollection, target *int32) (*pb.GetTaskDraftCollectionResponse, error) {
	response := s.taskDraftCollectionResponse(collection)
	if !s.replier.collectionEnabled() {
		return response, nil
	}
	for i, run := range collection.Items {
		index := int32(i)
		if (target != nil && *target != index) || run.Status != draftSucceeded {
			continue
		}
		record, found, err := s.replier.collectionStore.loadCollectionReply(ctx, collection, index)
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		if err != nil {
			return nil, err
		}
		if (found && !record.matchesCollection(collection, index)) || (!found && record != (draftReplyRecord{})) {
			return nil, status.Error(codes.Unavailable, "stored item reply is invalid")
		}
		applyDraftCollectionReply(response.Items[i], record, found)
	}
	return response, nil
}

func taskDraftCollectionItemResponse(collection *pb.GetTaskDraftCollectionResponse, index int32) *pb.GetTaskDraftItemResponse {
	return &pb.GetTaskDraftItemResponse{RunId: collection.RunId, TeamId: collection.TeamId, GroupId: collection.GroupId, ItemCount: collection.ItemCount, Item: collection.Items[index]}
}

// RetryTaskReplyItem authorizes the original initiator and current group on
// every call, including locally accepted replays. It only sends the fixed card.
func (s *Server) RetryTaskReplyItem(ctx context.Context, req *pb.GetTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
	token, err := loginToken(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil || req.GetRunId() <= 0 || req.ItemIndex == nil || req.GetItemIndex() < 0 || req.GetItemIndex() >= maxGeneratedTaskDrafts {
		return nil, status.Error(codes.InvalidArgument, "positive run ID and explicit item index 0..4 are required")
	}
	if s == nil || s.draftReader == nil || !s.replier.collectionEnabled() {
		return nil, status.Error(codes.Unavailable, "item bot reply is not configured")
	}
	retryCtx, cancel := context.WithTimeout(ctx, draftReplyRequestTimeout)
	defer cancel()
	collection, err := s.draftReader.loadCollection(retryCtx, token, req.GetRunId())
	if err != nil {
		return nil, err
	}
	index := req.GetItemIndex()
	if int(index) >= len(collection.Items) {
		return nil, status.Error(codes.NotFound, "draft item not found")
	}
	record, err := s.replier.attemptCollection(retryCtx, token, collection, index)
	if err != nil {
		return nil, err
	}
	if !record.matchesCollection(collection, index) || !record.Accepted {
		return nil, status.Error(codes.Unavailable, "item reply acceptance was not saved")
	}
	response := s.taskDraftCollectionResponse(collection)
	applyDraftCollectionReply(response.Items[index], record, true)
	return taskDraftCollectionItemResponse(response, index), nil
}
