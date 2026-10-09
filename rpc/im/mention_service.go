package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"

	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

const imMentionPushDNSName = "push.go-im.internal"

type imMentionConfig struct {
	ListenOn string
	Files    rpcauth.CertificateFiles
}

func loadIMMentionConfig(getenv func(string) string) (imMentionConfig, error) {
	if getenv == nil {
		return imMentionConfig{}, errors.New("IM mention environment reader required")
	}
	c := imMentionConfig{ListenOn: getenv("IM_MENTION_LISTEN_ON"), Files: rpcauth.CertificateFiles{
		CertFile: getenv("IM_MENTION_TLS_CERT_FILE"), KeyFile: getenv("IM_MENTION_TLS_KEY_FILE"), CAFile: getenv("IM_MENTION_TLS_CA_FILE"),
	}}
	return c, validateIMMentionConfig(c, false)
}

func (c imMentionConfig) disabled() bool {
	return c.ListenOn == "" && c.Files == (rpcauth.CertificateFiles{})
}

func validateIMMentionConfig(c imMentionConfig, allowZeroPort bool) error {
	if c.disabled() {
		return nil
	}
	if _, err := imTriggerPort(c.ListenOn, allowZeroPort); err != nil {
		return errors.New("invalid IM_MENTION_LISTEN_ON")
	}
	for _, path := range []string{c.Files.CertFile, c.Files.KeyFile, c.Files.CAFile} {
		if path == "" || strings.TrimSpace(path) != path {
			return errors.New("complete IM mention TLS configuration required")
		}
	}
	return nil
}

func validateIMMentionStartup(c imMentionConfig, ordinaryAddr, botAddr, triggerAddr, leaveAddr string) error {
	if c.disabled() {
		return nil
	}
	if err := validateIMMentionConfig(c, false); err != nil {
		return err
	}
	mentionPort, _ := imTriggerPort(c.ListenOn, false)
	for _, addr := range []string{ordinaryAddr, botAddr, triggerAddr, leaveAddr} {
		if addr == "" {
			continue
		}
		port, err := imTriggerPort(addr, false)
		if err != nil || port == mentionPort {
			return errors.New("IM mention listener requires a separate port")
		}
	}
	return nil
}

type imMentionRuntime struct {
	server   *grpc.Server
	listener net.Listener
	stopOnce sync.Once
}

func newIMMentionRuntime(c imMentionConfig, db *gorm.DB) (*imMentionRuntime, error) {
	if err := validateIMMentionConfig(c, true); err != nil {
		return nil, err
	}
	if c.disabled() {
		return nil, nil
	}
	if db == nil {
		return nil, errors.New("IM mention listener requires a database")
	}
	creds, err := rpcauth.NewServiceServerCredentials(c.Files, imMentionPushDNSName)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", c.ListenOn)
	if err != nil {
		return nil, err
	}
	server := grpc.NewServer(grpc.Creds(creds), grpc.MaxRecvMsgSize(4096))
	pb.RegisterIMMentionServer(server, &imMentionServer{db: db})
	return &imMentionRuntime{server: server, listener: listener}, nil
}

func (r *imMentionRuntime) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() { r.server.Stop(); _ = r.listener.Close() })
}

type imMentionServer struct {
	pb.UnimplementedIMMentionServer
	db *gorm.DB
}

func (s *imMentionServer) ValidateGroupMentionTargets(ctx context.Context, req *pb.ValidateGroupMentionTargetsRequest) (*pb.ValidateGroupMentionTargetsResponse, error) {
	if ctx == nil {
		return nil, status.Error(codes.Unauthenticated, "verified Push TLS identity required")
	}
	if err := rpcauth.RequireServiceIdentity(ctx, imMentionPushDNSName); err != nil {
		return nil, err
	}
	if s == nil || s.db == nil {
		return nil, status.Error(codes.Unavailable, "mention validation unavailable")
	}
	if req.GetGroupId() <= 0 || req.GetSender() == nil || req.GetSender().GetUserId() <= 0 || req.GetSender().GetTeamGeneration() <= 0 || len(req.GetTargets()) == 0 || len(req.GetTargets()) > 10 {
		return nil, status.Error(codes.InvalidArgument, "invalid mention scope")
	}
	seen := map[int64]bool{req.GetSender().GetUserId(): true}
	for _, target := range req.GetTargets() {
		if target == nil || target.GetUserId() <= 0 || target.GetTeamGeneration() <= 0 || seen[target.GetUserId()] {
			return nil, status.Error(codes.InvalidArgument, "invalid mention targets")
		}
		seen[target.GetUserId()] = true
	}
	var group struct{ TeamID *int64 }
	err := s.db.WithContext(ctx).Table("groups").Select("team_id").Where("id = ?", req.GetGroupId()).Take(&group).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || err == nil && (group.TeamID == nil || *group.TeamID <= 0) {
		return nil, status.Error(codes.NotFound, "team group not found")
	}
	if err != nil {
		return nil, groupUnreadDBError(ctx)
	}
	for _, member := range append([]*pb.MentionMemberGeneration{req.GetSender()}, req.GetTargets()...) {
		if err := checkTeamGroupReadGeneration(ctx, s.db, req.GetGroupId(), *group.TeamID, member.GetUserId(), member.GetTeamGeneration()); err != nil {
			return nil, err
		}
	}
	return &pb.ValidateGroupMentionTargetsResponse{TeamId: *group.TeamID}, nil
}
