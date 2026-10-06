package main

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/yjydist/go-im/internal/rpcauth"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const leaveIMServerName = "im.go-im.internal"

type teamLeaveIMCloser interface {
	CloseTeamGroupMemberships(context.Context, int64, int64, int64) error
}

type teamLeaveIMConfig struct {
	Addr  string
	Files rpcauth.CertificateFiles
}

func loadTeamLeaveIMConfig(getenv func(string) string) (teamLeaveIMConfig, error) {
	if getenv == nil {
		return teamLeaveIMConfig{}, errors.New("User leave environment reader required")
	}
	c := teamLeaveIMConfig{Addr: getenv("USER_LEAVE_IM_RPC_ADDR"), Files: rpcauth.CertificateFiles{
		CertFile: getenv("USER_LEAVE_IM_TLS_CERT_FILE"), KeyFile: getenv("USER_LEAVE_IM_TLS_KEY_FILE"), CAFile: getenv("USER_LEAVE_IM_TLS_CA_FILE"),
	}}
	_, err := validateTeamLeaveIMConfig(c)
	return c, err
}

func validateTeamLeaveIMConfig(c teamLeaveIMConfig) (bool, error) {
	values := []string{c.Addr, c.Files.CertFile, c.Files.KeyFile, c.Files.CAFile}
	configured := false
	for _, v := range values {
		configured = configured || v != ""
	}
	if !configured {
		return false, nil
	}
	for _, v := range values {
		if v == "" || strings.TrimSpace(v) != v {
			return true, errors.New("complete User leave IM RPC and TLS configuration required")
		}
	}
	host, portText, err := net.SplitHostPort(c.Addr)
	port, portErr := strconv.Atoi(portText)
	if err != nil || host == "" || portErr != nil || port < 1 || port > 65535 || strconv.Itoa(port) != portText ||
		strings.ContainsAny(host, "/\\?#@*") || strings.IndexFunc(c.Addr, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 ||
		(strings.HasPrefix(c.Addr, "[") && net.ParseIP(host) == nil) {
		return true, errors.New("invalid User leave IM RPC address")
	}
	return true, nil
}

type teamLeaveIMClient struct {
	rpc  impb.IMLeaveClient
	conn *grpc.ClientConn
}

func newTeamLeaveIMClient(c teamLeaveIMConfig) (*teamLeaveIMClient, error) {
	configured, err := validateTeamLeaveIMConfig(c)
	if err != nil || !configured {
		return nil, err
	}
	creds, err := rpcauth.NewServiceClientCredentials(c.Files, leaveIMServerName)
	if err != nil {
		return nil, errors.New("cannot load User leave IM RPC certificates")
	}
	conn, err := grpc.NewClient(c.Addr, grpc.WithTransportCredentials(creds), grpc.WithDisableRetry(), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(4096)))
	if err != nil {
		return nil, errors.New("cannot configure User leave IM RPC connection")
	}
	return &teamLeaveIMClient{rpc: impb.NewIMLeaveClient(conn), conn: conn}, nil
}

func (c *teamLeaveIMClient) CloseTeamGroupMemberships(ctx context.Context, teamID, userID, generation int64) error {
	if err := teamLeaveContextError(ctx); err != nil {
		return err
	}
	if teamID <= 0 || userID <= 0 || generation <= 0 {
		return status.Error(codes.InvalidArgument, "invalid team leave scope")
	}
	if c == nil || c.rpc == nil {
		return status.Error(codes.Unavailable, "IM leave service unavailable")
	}
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// Never forward the user's login token to the service-only channel.
	callCtx = metadata.NewOutgoingContext(callCtx, metadata.MD{})
	response, err := c.rpc.CloseTeamGroupMemberships(callCtx, &impb.CloseTeamGroupMembershipsRequest{TeamId: teamID, UserId: userID, Generation: generation})
	if callCtx.Err() != nil {
		return status.FromContextError(callCtx.Err()).Err()
	}
	if err != nil {
		return status.Error(codes.Unavailable, "IM leave cleanup unavailable")
	}
	if response == nil || response.GetClosedThroughGeneration() < generation {
		return status.Error(codes.Unavailable, "IM leave cleanup response invalid")
	}
	return nil
}

func (c *teamLeaveIMClient) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}
