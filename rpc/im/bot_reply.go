package main

import (
	"context"
	"crypto/tls"
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
	index := int32(0)
	return s.postTaskCreatedCard(ctx, req.GetRunId(), &index, req.GetTeamId(), req.GetGroupId(), req.GetContent())
}

func (s *botReplyServer) PostTaskCreatedCardItem(ctx context.Context, req *pb.PostTaskCreatedCardItemRequest) (*pb.PostTaskCreatedCardResponse, error) {
	var index *int32
	if req != nil {
		index = req.ItemIndex
	}
	return s.postTaskCreatedCard(ctx, req.GetRunId(), index, req.GetTeamId(), req.GetGroupId(), req.GetContent())
}

// Both entry points authorize before publishing, including accepted replays.
// Only the legacy entry point supplies a default index.
func (s *botReplyServer) postTaskCreatedCard(ctx context.Context, runID int64, index *int32, teamID, groupID int64, content string) (*pb.PostTaskCreatedCardResponse, error) {
	if s == nil {
		return nil, status.Error(codes.Unauthenticated, "verified Agent TLS identity required")
	}

	if err := requireBotAgent(ctx, s.agentDNSName); err != nil {
		return nil, err
	}
	if s.im == nil || s.im.db == nil || s.im.jwtSecret == "" || s.publisher == nil {
		return nil, status.Error(codes.Unavailable, "bot sending is not enabled")
	}
	if index == nil || teamID <= 0 || groupID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid bot send scope")
	}
	expectedMsgID, err := model.BotTaskItemMsgID(runID, *index)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid bot send item identity")
	}
	itemIndex := *index
	if _, err := model.DecodeTaskCreatedCard(content); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid task creation card")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	actorID, _, err := s.im.authenticatedUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.im.CheckTeamGroupAccess(ctx, &pb.CheckTeamGroupAccessRequest{TeamId: teamID, GroupId: groupID}); err != nil {
		return nil, err
	}
	bot, err := lookupEnabledBot(ctx, s.im.db, s.botCode)
	if err != nil {
		return nil, err
	}
	msgID, err := s.publisher.Publish(ctx, botSendIntent{RunID: runID, ItemIndex: itemIndex, BotID: bot.ID, InitiatorID: actorID,
		TeamID: teamID, GroupID: groupID, Content: content})
	if err != nil {
		return nil, err
	}
	if msgID != expectedMsgID {
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
