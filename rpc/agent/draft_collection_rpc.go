package agent

import (
	"context"

	"github.com/yjydist/go-im/rpc/agent/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) GetTaskDraftCollection(ctx context.Context, req *pb.GetTaskDraftRequest) (*pb.GetTaskDraftCollectionResponse, error) {
	token, err := loginToken(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetRunId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid run ID")
	}
	if s == nil || s.draftReader == nil {
		return nil, status.Error(codes.Unavailable, "draft access is not configured")
	}
	readCtx, cancel := context.WithTimeout(ctx, draftReadTimeout)
	defer cancel()
	collection, err := s.draftReader.loadCollection(readCtx, token, req.GetRunId())
	if readCtx.Err() != nil {
		return nil, status.FromContextError(readCtx.Err()).Err()
	}
	if err != nil {
		return nil, err
	}
	return s.taskDraftCollectionResponse(collection), nil
}

func (s *Server) GetTaskDraftItem(ctx context.Context, req *pb.GetTaskDraftItemRequest) (*pb.GetTaskDraftItemResponse, error) {
	if req == nil || req.ItemIndex == nil || req.GetItemIndex() < 0 || req.GetItemIndex() >= maxGeneratedTaskDrafts {
		return nil, status.Error(codes.InvalidArgument, "draft item index is required and must be 0..4")
	}
	collection, err := s.GetTaskDraftCollection(ctx, &pb.GetTaskDraftRequest{RunId: req.GetRunId()})
	if err != nil {
		return nil, err
	}
	if req.GetItemIndex() >= collection.GetItemCount() {
		return nil, status.Error(codes.NotFound, "draft item not found")
	}
	return &pb.GetTaskDraftItemResponse{RunId: collection.RunId, TeamId: collection.TeamId, GroupId: collection.GroupId, ItemCount: collection.ItemCount, Item: collection.Items[req.GetItemIndex()]}, nil
}

func (s *Server) taskDraftCollectionResponse(collection taskDraftCollection) *pb.GetTaskDraftCollectionResponse {
	replyStatus := "disabled"
	if s.replier != nil {
		replyStatus = "not_started"
	}
	response := &pb.GetTaskDraftCollectionResponse{RunId: collection.ID, TeamId: collection.Scope.TeamID, GroupId: collection.Scope.GroupID, ItemCount: int32(len(collection.Items)), Items: make([]*pb.TaskDraftCollectionItem, 0, len(collection.Items))}
	for i, run := range collection.Items {
		index := int32(i)
		response.Items = append(response.Items, &pb.TaskDraftCollectionItem{ItemIndex: &index, Status: string(run.Status), Draft: taskDraftRPCResponse(run).Draft, TaskId: run.TaskID, ReplyStatus: replyStatus})
	}
	return response
}
