package agent

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yjydist/go-im/rpc/agent/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const askTimeout = 20 * time.Second

// Answerer is the later Eino boundary. Its implementation must authorize all
// context reads using token before returning an answer.
type Answerer interface {
	Answer(context.Context, string, int64, int64, string) (string, error)
}

type Server struct {
	pb.UnimplementedAgentServer
	answerer    Answerer
	draftReader *draftAccessReader
	preparer    *draftPreparer
	confirmer   *draftConfirmer
	replier     *draftReplier
}

func NewServer(answerer Answerer) *Server {
	return &Server{answerer: answerer}
}

func (s *Server) Ask(ctx context.Context, req *pb.AskRequest) (*pb.AskResponse, error) {
	token, err := loginToken(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil || req.GetTeamId() <= 0 || req.GetGroupId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid team or group")
	}
	question := strings.TrimSpace(req.GetQuestion())
	if question == "" || utf8.RuneCountInString(question) > 2000 {
		return nil, status.Error(codes.InvalidArgument, "question must contain 1 to 2000 characters")
	}
	if s == nil || s.answerer == nil {
		return nil, status.Error(codes.Unavailable, "Agent answerer is not configured")
	}
	answerCtx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	answer, err := s.answerer.Answer(answerCtx, token, req.GetTeamId(), req.GetGroupId(), question)
	if answerCtx.Err() != nil {
		return nil, status.FromContextError(answerCtx.Err()).Err()
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(answer) == "" {
		return nil, status.Error(codes.Internal, "Agent returned an empty answer")
	}
	return &pb.AskResponse{Answer: answer}, nil
}

func loginToken(ctx context.Context) (string, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get("authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return "", status.Error(codes.Unauthenticated, "login required")
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", status.Error(codes.Unauthenticated, "login required")
	}
	return token, nil
}
