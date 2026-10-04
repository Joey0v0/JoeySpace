package agent

import (
	"context"
	"time"

	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

const (
	draftReplyTimeout        = 5 * time.Second
	draftBotTimeout          = 4 * time.Second
	draftReplyRequestTimeout = 18 * time.Second
)

type draftBotClient interface {
	PostTaskCreatedCard(context.Context, *impb.PostTaskCreatedCardRequest, ...grpc.CallOption) (*impb.PostTaskCreatedCardResponse, error)
}

type draftReplier struct {
	store           draftReplyStore
	bot             draftBotClient
	collectionStore draftCollectionReplyStore
	itemBot         draftItemBotClient
}

func (s *Server) ConfigureDraftReplies(db *gorm.DB, bot impb.IMBotClient) {
	if bot != nil {
		store := &draftStore{db: db}
		s.replier = &draftReplier{store: store, bot: bot, collectionStore: store, itemBot: bot}
	}
}

// attempt never calls Task. It saves an immutable intent before IM, then only
// advances acceptance; failures cannot undo the already committed task result.
func (r *draftReplier) attempt(ctx context.Context, token string, run taskDraftRun) (draftReplyRecord, error) {
	if r == nil || r.store == nil || r.bot == nil {
		return draftReplyRecord{}, status.Error(codes.Unavailable, "bot reply is not configured")
	}
	replyCtx, cancel := context.WithTimeout(ctx, draftReplyTimeout)
	defer cancel()
	record, err := r.store.prepareReply(replyCtx, run)
	if err != nil {
		return draftReplyRecord{}, err
	}
	if record.Accepted {
		return record, nil
	}
	botCtx, stop := context.WithTimeout(replyCtx, draftBotTimeout)
	botCtx = metadata.NewOutgoingContext(botCtx, metadata.Pairs("authorization", "Bearer "+token))
	result, err := r.bot.PostTaskCreatedCard(botCtx, &impb.PostTaskCreatedCardRequest{
		RunId: record.RunID, TeamId: record.TeamID, GroupId: record.GroupID, Content: record.Content,
	})
	stop()
	if err != nil {
		switch status.Code(err) {
		case codes.Unauthenticated, codes.PermissionDenied, codes.NotFound, codes.FailedPrecondition, codes.AlreadyExists:
			return record, status.Error(status.Code(err), "bot reply rejected; reload before retrying")
		default:
			return record, status.Error(codes.Unavailable, "bot reply result uncertain; reload then retry the same run")
		}
	}
	if result == nil || !result.GetAccepted() || result.GetMsgId() != record.MsgID {
		return record, status.Error(codes.Unavailable, "bot reply returned no matching acceptance")
	}
	if err := r.store.acceptReply(replyCtx, record); err != nil {
		return record, err
	}
	record.Accepted = true
	return record, nil
}

func applyDraftReply(response *pb.GetTaskDraftResponse, record draftReplyRecord, found bool) {
	response.ReplyStatus = "not_started"
	if found {
		response.ReplyMsgId, response.ReplyStatus = record.MsgID, "pending"
		if record.Accepted {
			response.ReplyStatus = "accepted"
		}
	}
}

// Reading has no posting side effect. It also never fabricates an acceptance
// when storage is unavailable. Caller must first authorize the run.
func (s *Server) draftResponseWithReply(ctx context.Context, run taskDraftRun) (*pb.GetTaskDraftResponse, error) {
	response := taskDraftRPCResponse(run)
	if s.replier == nil {
		response.ReplyStatus = "disabled"
		return response, nil
	}
	if run.Status != draftSucceeded {
		applyDraftReply(response, draftReplyRecord{}, false)
		return response, nil
	}
	record, found, err := s.replier.store.loadReply(ctx, run)
	if err != nil {
		return nil, err
	}
	applyDraftReply(response, record, found)
	return response, nil
}

// RetryTaskReply accepts only run ID plus the current original Token. Current
// initiator and group checks precede every retry, including accepted replays.
func (s *Server) RetryTaskReply(ctx context.Context, req *pb.GetTaskDraftRequest) (*pb.GetTaskDraftResponse, error) {
	token, err := loginToken(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetRunId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid run ID")
	}
	if s == nil || s.draftReader == nil || s.replier == nil {
		return nil, status.Error(codes.Unavailable, "bot reply is not configured")
	}
	retryCtx, cancel := context.WithTimeout(ctx, draftReplyRequestTimeout)
	defer cancel()
	run, err := s.draftReader.load(retryCtx, token, req.GetRunId())
	if err != nil {
		return nil, err
	}
	record, err := s.replier.attempt(retryCtx, token, run)
	if err != nil {
		return nil, err
	}
	response := taskDraftRPCResponse(run)
	applyDraftReply(response, record, true)
	return response, nil
}
