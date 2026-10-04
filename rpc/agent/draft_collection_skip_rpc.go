package agent

import (
	"context"

	"github.com/yjydist/go-im/rpc/agent/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) SkipTaskDraftItem(ctx context.Context, req *pb.SkipTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
	token, err := loginToken(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil || !validCollectionEditIdentity(req.GetRunId(), req.ItemIndex, req.GetExpectedRevision()) {
		return nil, status.Error(codes.InvalidArgument, "draft item identity and revision are required")
	}
	if s == nil || s.draftReader == nil {
		return nil, status.Error(codes.Unavailable, "draft skipping is not configured")
	}
	skipCtx, cancel := context.WithTimeout(ctx, draftReadTimeout)
	defer cancel()
	collection, err := s.draftReader.skipCollectionItem(skipCtx, token, req.GetRunId(), req.GetItemIndex(), req.GetExpectedRevision())
	if skipCtx.Err() != nil {
		return nil, status.FromContextError(skipCtx.Err()).Err()
	}
	if err != nil {
		return nil, err
	}
	response := s.taskDraftCollectionResponse(collection)
	return &pb.GetTaskDraftItemResponse{RunId: response.RunId, TeamId: response.TeamId, GroupId: response.GroupId, ItemCount: response.ItemCount, Item: response.Items[req.GetItemIndex()]}, nil
}
