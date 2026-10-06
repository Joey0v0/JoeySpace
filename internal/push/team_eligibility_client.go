package push

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/yjydist/go-im/internal/rpcauth"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const userPushServerName = "user.go-im.internal"

type TeamEligibility interface {
	CheckCurrentTeamMember(context.Context, int64, int64) (int64, bool, error)
}

type TeamEligibilityClientConfig struct {
	Addr  string
	Files rpcauth.CertificateFiles
}

func LoadTeamEligibilityClientConfig(getenv func(string) string) (TeamEligibilityClientConfig, error) {
	if getenv == nil {
		return TeamEligibilityClientConfig{}, errors.New("Push User environment reader required")
	}
	c := TeamEligibilityClientConfig{Addr: getenv("PUSH_USER_RPC_ADDR"), Files: rpcauth.CertificateFiles{
		CertFile: getenv("PUSH_USER_TLS_CERT_FILE"), KeyFile: getenv("PUSH_USER_TLS_KEY_FILE"), CAFile: getenv("PUSH_USER_TLS_CA_FILE"),
	}}
	_, err := validateTeamEligibilityClientConfig(c)
	return c, err
}

func validateTeamEligibilityClientConfig(c TeamEligibilityClientConfig) (bool, error) {
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
			return true, errors.New("complete Push User RPC and TLS configuration required")
		}
	}
	host, portText, err := net.SplitHostPort(c.Addr)
	port, portErr := strconv.Atoi(portText)
	if err != nil || host == "" || portErr != nil || port < 1 || port > 65535 || strconv.Itoa(port) != portText ||
		strings.ContainsAny(host, "/\\?#@*") || strings.IndexFunc(c.Addr, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 ||
		(strings.HasPrefix(c.Addr, "[") && net.ParseIP(host) == nil) {
		return true, errors.New("invalid Push User RPC address")
	}
	return true, nil
}

type TeamEligibilityClient struct {
	rpc  userpb.UserPushClient
	conn *grpc.ClientConn
}

func NewTeamEligibilityClient(c TeamEligibilityClientConfig) (*TeamEligibilityClient, error) {
	configured, err := validateTeamEligibilityClientConfig(c)
	if err != nil || !configured {
		return nil, err
	}
	creds, err := rpcauth.NewServiceClientCredentials(c.Files, userPushServerName)
	if err != nil {
		return nil, errors.New("cannot load Push User RPC certificates")
	}
	conn, err := grpc.NewClient(c.Addr, grpc.WithTransportCredentials(creds), grpc.WithDisableRetry(), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(4096)))
	if err != nil {
		return nil, errors.New("cannot configure Push User RPC connection")
	}
	return &TeamEligibilityClient{rpc: userpb.NewUserPushClient(conn), conn: conn}, nil
}

func (c *TeamEligibilityClient) CheckCurrentTeamMember(ctx context.Context, teamID, userID int64) (int64, bool, error) {
	if ctx == nil || teamID <= 0 || userID <= 0 {
		return 0, false, status.Error(codes.InvalidArgument, "positive team and user IDs and context required")
	}
	if err := ctx.Err(); err != nil {
		return 0, false, status.FromContextError(err).Err()
	}
	if c == nil || c.rpc == nil {
		return 0, false, status.Error(codes.Unavailable, "team eligibility service unavailable")
	}
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	callCtx = metadata.NewOutgoingContext(callCtx, metadata.MD{})
	response, err := c.rpc.CheckPushTeamMember(callCtx, &userpb.CheckPushTeamMemberRequest{TeamId: teamID, UserId: userID})
	if callCtx.Err() != nil {
		return 0, false, status.FromContextError(callCtx.Err()).Err()
	}
	if err != nil {
		if status.Code(err) == codes.PermissionDenied {
			return 0, false, nil
		}
		return 0, false, status.Error(codes.Unavailable, "team eligibility service unavailable")
	}
	if response == nil || response.GetTeamId() != teamID || response.GetUserId() != userID || response.GetGeneration() <= 0 {
		return 0, false, status.Error(codes.Unavailable, "invalid team eligibility response")
	}
	return response.GetGeneration(), true, nil
}

func (c *TeamEligibilityClient) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}
