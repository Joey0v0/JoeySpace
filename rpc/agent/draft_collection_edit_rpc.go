package agent

import (
	"context"

	"github.com/yjydist/go-im/rpc/agent/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func validCollectionEditIdentity(runID int64, index *int32, revision int64) bool {
	return runID > 0 && index != nil && *index >= 0 && *index < maxGeneratedTaskDrafts && revision > 0
}

func (s *Server) EditTaskDraftItemText(ctx context.Context, req *pb.EditTaskDraftItemTextRequest) (*pb.GetTaskDraftItemResponse, error) {
	if req == nil || !validCollectionEditIdentity(req.GetRunId(), req.ItemIndex, req.GetExpectedRevision()) {
		return nil, status.Error(codes.InvalidArgument, "invalid draft item identity")
	}
	return s.editTaskDraftCollectionItem(ctx, req.GetRunId(), req.GetItemIndex(), req.GetExpectedRevision(), draftCollectionEdit{Kind: collectionEditText, Title: req.GetTitle(), Description: req.GetDescription()})
}

func (s *Server) SelectTaskDraftItemAssignee(ctx context.Context, req *pb.SelectTaskDraftItemAssigneeRequest) (*pb.GetTaskDraftItemResponse, error) {
	if req == nil || !validCollectionEditIdentity(req.GetRunId(), req.ItemIndex, req.GetExpectedRevision()) || req.AssigneeId == nil || req.GetAssigneeId() < 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid draft item assignee selection")
	}
	return s.editTaskDraftCollectionItem(ctx, req.GetRunId(), req.GetItemIndex(), req.GetExpectedRevision(), draftCollectionEdit{Kind: collectionEditAssignee, AssigneeID: req.GetAssigneeId()})
}

func (s *Server) EditTaskDraftItemDeadline(ctx context.Context, req *pb.EditTaskDraftItemDeadlineRequest) (*pb.GetTaskDraftItemResponse, error) {
	if req == nil || !validCollectionEditIdentity(req.GetRunId(), req.ItemIndex, req.GetExpectedRevision()) || req.DueAtUnixMs == nil || req.GetDueAtUnixMs() < 0 || req.GetDueAtUnixMs() > maxDraftDueAtUnixMs {
		return nil, status.Error(codes.InvalidArgument, "invalid draft item deadline")
	}
	return s.editTaskDraftCollectionItem(ctx, req.GetRunId(), req.GetItemIndex(), req.GetExpectedRevision(), draftCollectionEdit{Kind: collectionEditDeadline, DueAtUnixMs: req.GetDueAtUnixMs()})
}

func (s *Server) editTaskDraftCollectionItem(ctx context.Context, runID int64, index int32, revision int64, edit draftCollectionEdit) (*pb.GetTaskDraftItemResponse, error) {
	token, err := loginToken(ctx)
	if err != nil {
		return nil, err
	}
	if s == nil || s.draftReader == nil {
		return nil, status.Error(codes.Unavailable, "draft collection editing is not configured")
	}
	editCtx, cancel := context.WithTimeout(ctx, draftReadTimeout)
	defer cancel()
	collection, err := s.draftReader.editCollectionItem(editCtx, token, runID, index, revision, edit)
	if editCtx.Err() != nil {
		return nil, status.FromContextError(editCtx.Err()).Err()
	}
	if err != nil {
		return nil, err
	}
	response := s.taskDraftCollectionResponse(collection)
	return &pb.GetTaskDraftItemResponse{RunId: response.RunId, TeamId: response.TeamId, GroupId: response.GroupId, ItemCount: response.ItemCount, Item: response.Items[index]}, nil
}
