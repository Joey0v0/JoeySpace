package push

import (
	"context"
	"errors"
	"time"

	"github.com/yjydist/go-im/internal/rpcauth"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const imMentionServerName = "im.go-im.internal"

type MentionValidator interface {
	ValidateGroupMentionTargets(context.Context, int64, int64, int64, int64, []MentionMemberGeneration) error
}

type MentionMemberGeneration struct {
	UserID     int64
	Generation int64
}

type MentionClientConfig struct {
	Addr  string
	Files rpcauth.CertificateFiles
}

func LoadMentionClientConfig(getenv func(string) string) (MentionClientConfig, error) {
	if getenv == nil {
		return MentionClientConfig{}, errors.New("Push IM environment reader required")
	}
	c := MentionClientConfig{Addr: getenv("PUSH_IM_MENTION_RPC_ADDR"), Files: rpcauth.CertificateFiles{
		CertFile: getenv("PUSH_IM_MENTION_TLS_CERT_FILE"), KeyFile: getenv("PUSH_IM_MENTION_TLS_KEY_FILE"), CAFile: getenv("PUSH_IM_MENTION_TLS_CA_FILE"),
	}}
	_, err := validateMentionClientConfig(c)
	return c, err
}

func validateMentionClientConfig(c MentionClientConfig) (bool, error) {
	return validateTeamEligibilityClientConfig(TeamEligibilityClientConfig{Addr: c.Addr, Files: c.Files})
}

type MentionClient struct {
	rpc  impb.IMMentionClient
	conn *grpc.ClientConn
}

func NewMentionClient(c MentionClientConfig) (*MentionClient, error) {
	configured, err := validateMentionClientConfig(c)
	if err != nil || !configured {
		return nil, err
	}
	creds, err := rpcauth.NewServiceClientCredentials(c.Files, imMentionServerName)
	if err != nil {
		return nil, errors.New("cannot load Push IM mention certificates")
	}
	conn, err := grpc.NewClient(c.Addr, grpc.WithTransportCredentials(creds), grpc.WithDisableRetry(), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(4096)))
	if err != nil {
		return nil, errors.New("cannot configure Push IM mention connection")
	}
	return &MentionClient{rpc: impb.NewIMMentionClient(conn), conn: conn}, nil
}

func (c *MentionClient) ValidateGroupMentionTargets(ctx context.Context, groupID, teamID, senderID, senderGeneration int64, targets []MentionMemberGeneration) error {
	if ctx == nil || groupID <= 0 || teamID <= 0 || senderID <= 0 || senderGeneration <= 0 || len(targets) < 1 || len(targets) > 10 {
		return status.Error(codes.InvalidArgument, "invalid group mention request")
	}
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	if c == nil || c.rpc == nil {
		return status.Error(codes.Unavailable, "mention validator unavailable")
	}
	seen := make(map[int64]struct{}, len(targets))
	req := &impb.ValidateGroupMentionTargetsRequest{GroupId: groupID, Sender: &impb.MentionMemberGeneration{UserId: senderID, TeamGeneration: senderGeneration}}
	for _, target := range targets {
		if target.UserID <= 0 || target.Generation <= 0 {
			return status.Error(codes.InvalidArgument, "invalid mention target")
		}
		if _, exists := seen[target.UserID]; exists {
			return status.Error(codes.InvalidArgument, "duplicate mention target")
		}
		seen[target.UserID] = struct{}{}
		req.Targets = append(req.Targets, &impb.MentionMemberGeneration{UserId: target.UserID, TeamGeneration: target.Generation})
	}
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	callCtx = metadata.NewOutgoingContext(callCtx, metadata.MD{})
	response, err := c.rpc.ValidateGroupMentionTargets(callCtx, req)
	if callCtx.Err() != nil {
		return status.FromContextError(callCtx.Err()).Err()
	}
	if err != nil {
		if status.Code(err) == codes.PermissionDenied {
			return status.Error(codes.PermissionDenied, "mention targets are not current group members")
		}
		return status.Error(codes.Unavailable, "mention validator unavailable")
	}
	if response == nil || response.GetTeamId() != teamID {
		return status.Error(codes.Unavailable, "invalid mention validator response")
	}
	return nil
}

func (c *MentionClient) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}
