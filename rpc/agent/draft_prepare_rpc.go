package agent

import (
	"context"

	"github.com/bwmarrin/snowflake"
	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func (s *Server) ConfigureDraftPreparation(db *gorm.DB, users userpb.UserClient, im impb.IMClient, generator *EinoTaskDraftGenerator, node *snowflake.Node) {
	s.preparer = &draftPreparer{
		identity:  &draftIdentityResolver{users: users},
		messages:  NewContextReader(im, nil),
		generator: generator,
		store:     &draftStore{db: db},
		idNode:    node,
		assignees: &draftAssigneeResolver{users: users},
	}
}

func (s *Server) PrepareTaskDraft(ctx context.Context, req *pb.PrepareTaskDraftRequest) (*pb.PrepareTaskDraftResponse, error) {
	token, err := loginToken(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "draft request is required")
	}
	md, _ := metadata.FromIncomingContext(ctx)
	keys := md.Get("idempotency-key")
	if len(keys) != 1 || !validDraftRequestKey(keys[0]) {
		return nil, status.Error(codes.InvalidArgument, "invalid idempotency key")
	}
	if s == nil || s.preparer == nil {
		return nil, status.Error(codes.Unavailable, "draft preparation is not configured")
	}
	prepareCtx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	runID, err := s.preparer.prepareWithReference(prepareCtx, token, req.GetTeamId(), req.GetGroupId(), req.GetInstruction(), keys[0], req.InstructionReferenceUnixMs)
	if prepareCtx.Err() != nil {
		return nil, status.FromContextError(prepareCtx.Err()).Err()
	}
	if err != nil {
		return nil, err
	}
	return &pb.PrepareTaskDraftResponse{RunId: runID}, nil
}
