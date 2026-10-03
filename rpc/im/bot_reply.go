package main

import (
	"context"
	"crypto/tls"
	"strconv"
	"time"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type botReplyServer struct {
	pb.UnimplementedIMBotServer
	im           *imServer
	botCode      string
	agentDNSName string
	publisher    botCardPublisher
}

func (s *botReplyServer) PostTaskCreatedCard(ctx context.Context, req *pb.PostTaskCreatedCardRequest) (*pb.PostTaskCreatedCardResponse, error) {
	if err := requireBotAgent(ctx, s.agentDNSName); err != nil {
		return nil, err
	}
	if s.im == nil || s.im.db == nil || s.im.jwtSecret == "" || s.publisher == nil {
		return nil, status.Error(codes.Unavailable, "bot sending is not enabled")
	}
	if req.GetRunId() <= 0 || req.GetTeamId() <= 0 || req.GetGroupId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid bot send scope")
	}
	if _, err := model.DecodeTaskCreatedCard(req.GetContent()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid task creation card")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	actorID, _, err := s.im.authenticatedUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.im.CheckTeamGroupAccess(ctx, &pb.CheckTeamGroupAccessRequest{TeamId: req.GetTeamId(), GroupId: req.GetGroupId()}); err != nil {
		return nil, err
	}
	bot, err := lookupEnabledBot(ctx, s.im.db, s.botCode)
	if err != nil {
		return nil, err
	}
	msgID, err := s.publisher.Publish(ctx, botSendIntent{RunID: req.GetRunId(), BotID: bot.ID, InitiatorID: actorID,
		TeamID: req.GetTeamId(), GroupID: req.GetGroupId(), Content: req.GetContent()})
	if err != nil {
		return nil, err
	}
	if msgID != model.BotTaskMsgIDPrefix+strconv.FormatInt(req.GetRunId(), 10) {
		return nil, status.Error(codes.Internal, "invalid bot message result")
	}
	return &pb.PostTaskCreatedCardResponse{MsgId: msgID, Accepted: true}, nil
}

// The transport checks certificate signatures. The handler also refuses plain
// registration or user-controlled metadata masquerading as a service identity.
func requireBotAgent(ctx context.Context, expectedDNSName string) error {
	p, ok := peer.FromContext(ctx)
	if ok && expectedDNSName != "" {
		info, isTLS := p.AuthInfo.(credentials.TLSInfo)
		if isTLS && info.State.Version >= tls.VersionTLS13 && len(info.State.VerifiedChains) > 0 && len(info.State.PeerCertificates) > 0 {
			for _, name := range info.State.PeerCertificates[0].DNSNames {
				if name == expectedDNSName {
					return nil
				}
			}
		}
	}
	return status.Error(codes.Unauthenticated, "verified Agent TLS identity required")
}
