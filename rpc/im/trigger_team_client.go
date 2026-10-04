package main

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/yjydist/go-im/internal/rpcauth"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type triggerTeamClientConfig struct {
	Addr          string
	ServerDNSName string
	Files         rpcauth.CertificateFiles
}

func validateTriggerTeamClientConfig(c triggerTeamClientConfig) (bool, error) {
	values := []string{c.Addr, c.ServerDNSName, c.Files.CertFile, c.Files.KeyFile, c.Files.CAFile}
	configured := false
	for _, value := range values {
		configured = configured || value != ""
	}
	if !configured {
		return false, nil
	}
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value {
			return true, errors.New("complete IM trigger User RPC and TLS configuration is required")
		}
	}
	host, portText, err := net.SplitHostPort(c.Addr)
	port, portErr := strconv.Atoi(portText)
	if err != nil || host == "" || portErr != nil || port < 1 || port > 65535 || strconv.Itoa(port) != portText ||
		strings.ContainsAny(host, "/\\?#@") || strings.IndexFunc(c.Addr, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 ||
		(strings.HasPrefix(c.Addr, "[") && net.ParseIP(host) == nil) {
		return true, errors.New("invalid IM trigger User RPC address")
	}
	if strings.ContainsAny(c.ServerDNSName, "*/\\:?#@") || strings.IndexFunc(c.ServerDNSName, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return true, errors.New("exact User trigger TLS server name is required")
	}
	return true, nil
}

func loadTriggerTeamClientConfig(getenv func(string) string) (triggerTeamClientConfig, error) {
	if getenv == nil {
		return triggerTeamClientConfig{}, errors.New("IM trigger User RPC environment reader is required")
	}
	c := triggerTeamClientConfig{Addr: getenv("IM_TRIGGER_USER_RPC_ADDR"), ServerDNSName: getenv("IM_TRIGGER_USER_TLS_SERVER_NAME"),
		Files: rpcauth.CertificateFiles{CertFile: getenv("IM_TRIGGER_USER_TLS_CERT_FILE"), KeyFile: getenv("IM_TRIGGER_USER_TLS_KEY_FILE"), CAFile: getenv("IM_TRIGGER_USER_TLS_CA_FILE")}}
	_, err := validateTriggerTeamClientConfig(c)
	return c, err
}

type triggerTeamClient struct {
	rpc       userpb.UserTriggerClient
	conn      *grpc.ClientConn
	closeOnce sync.Once
	closeErr  error
}

func newTriggerTeamClient(c triggerTeamClientConfig) (*triggerTeamClient, error) {
	configured, err := validateTriggerTeamClientConfig(c)
	if err != nil || !configured {
		return nil, err
	}
	creds, err := rpcauth.NewServiceClientCredentials(c.Files, c.ServerDNSName)
	if err != nil {
		return nil, errors.New("cannot load IM trigger User RPC certificates")
	}
	conn, err := grpc.NewClient(c.Addr, grpc.WithTransportCredentials(creds), grpc.WithDisableRetry(),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(4096)))
	if err != nil {
		return nil, errors.New("cannot configure IM trigger User RPC connection")
	}
	return &triggerTeamClient{rpc: userpb.NewUserTriggerClient(conn), conn: conn}, nil
}

func (c *triggerTeamClient) Check(ctx context.Context, actorID, teamID int64) error {
	if ctx == nil || actorID <= 0 || teamID <= 0 {
		return status.Error(codes.InvalidArgument, "positive actor and team IDs and request context required")
	}
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	if c == nil || c.rpc == nil || c.conn != nil && c.conn.GetState() == connectivity.Shutdown {
		return status.Error(codes.Unavailable, "team eligibility service unavailable")
	}
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// This service channel must never forward a caller token or delegated identity.
	callCtx = metadata.NewOutgoingContext(callCtx, metadata.MD{})
	result, err := c.rpc.CheckTriggerTeamMember(callCtx, &userpb.CheckTriggerTeamMemberRequest{ActorId: actorID, TeamId: teamID})
	if contextErr := callCtx.Err(); contextErr != nil {
		return status.FromContextError(contextErr).Err()
	}
	if c.conn != nil && c.conn.GetState() == connectivity.Shutdown {
		return status.Error(codes.Unavailable, "team eligibility service unavailable")
	}
	if err != nil {
		code := status.Code(err)
		if errors.Is(err, context.Canceled) {
			code = codes.Canceled
		} else if errors.Is(err, context.DeadlineExceeded) {
			code = codes.DeadlineExceeded
		}
		switch code {
		case codes.PermissionDenied:
			return status.Error(code, "current team membership required")
		case codes.Unauthenticated:
			return status.Error(code, "service authentication required")
		case codes.Canceled:
			return status.Error(code, "team eligibility check canceled")
		case codes.DeadlineExceeded:
			return status.Error(code, "team eligibility check timed out")
		default:
			return status.Error(codes.Unavailable, "team eligibility service unavailable")
		}
	}
	if result == nil || result.GetActorId() != actorID || result.GetTeamId() != teamID {
		return status.Error(codes.Unavailable, "invalid team eligibility response")
	}
	return nil
}

func (c *triggerTeamClient) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	c.closeOnce.Do(func() { c.closeErr = c.conn.Close() })
	return c.closeErr
}
