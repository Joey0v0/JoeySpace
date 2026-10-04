package agent

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/rpcauth"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const maxTriggerContextBytes = 2 * 1024 * 1024

// TriggerContextClient reads saved IM facts through the dedicated service channel.
// It never forwards a caller token or accepts a caller-supplied actor or scope.
type TriggerContextClient struct {
	rpc       impb.IMTriggerClient
	conn      *grpc.ClientConn
	closeOnce sync.Once
	closeErr  error
}

func NewTriggerContextClient(getenv func(string) string) (*TriggerContextClient, error) {
	if getenv == nil {
		return nil, errors.New("Agent trigger RPC environment reader is required")
	}
	names := []string{"AGENT_IM_TRIGGER_ADDR", "AGENT_IM_TRIGGER_SERVER_NAME", "AGENT_IM_TRIGGER_TLS_CERT_FILE", "AGENT_IM_TRIGGER_TLS_KEY_FILE", "AGENT_IM_TRIGGER_TLS_CA_FILE"}
	values := make([]string, len(names))
	configured := false
	for i, name := range names {
		values[i] = getenv(name)
		configured = configured || values[i] != ""
	}
	if !configured {
		return nil, nil
	}
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value {
			return nil, errors.New("complete Agent trigger RPC and TLS configuration is required")
		}
	}
	host, portText, err := net.SplitHostPort(values[0])
	port, portErr := strconv.Atoi(portText)
	if err != nil || host == "" || portErr != nil || port < 1 || port > 65535 || strconv.Itoa(port) != portText ||
		strings.ContainsAny(host, "/\\?#@") || strings.IndexFunc(values[0], func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 ||
		strings.HasPrefix(values[0], "[") && net.ParseIP(host) == nil {
		return nil, errors.New("invalid Agent trigger IM RPC address")
	}
	if strings.ContainsAny(values[1], "*/\\:?#@") || strings.IndexFunc(values[1], func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return nil, errors.New("exact IM trigger TLS server name required")
	}
	creds, err := rpcauth.NewServiceClientCredentials(rpcauth.CertificateFiles{CertFile: values[2], KeyFile: values[3], CAFile: values[4]}, values[1])
	if err != nil {
		return nil, errors.New("cannot load Agent trigger RPC certificates")
	}
	conn, err := grpc.NewClient(values[0], grpc.WithTransportCredentials(creds), grpc.WithDisableRetry(),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxTriggerContextBytes)))
	if err != nil {
		return nil, errors.New("cannot configure Agent trigger RPC connection")
	}
	return &TriggerContextClient{rpc: impb.NewIMTriggerClient(conn), conn: conn}, nil
}

func validTriggerContext(response *impb.ReadTaskTriggerContextResponse, messageID int64) bool {
	if response == nil || response.GetMessageId() != messageID || proto.Size(response) > maxTriggerContextBytes {
		return false
	}
	fact := model.AgentTriggerOutbox{MessageID: response.GetMessageId(), MsgID: response.GetMsgId(), ActorID: response.GetActorId(),
		TeamID: response.GetTeamId(), GroupID: response.GetGroupId(), Instruction: response.GetInstruction(), ReferenceTimeMS: response.GetReferenceTimeUnixMs(),
		Action: model.AgentTriggerAction, EventVersion: model.AgentTriggerVersion}
	if fact.Validate() != nil || response.GetRequestKey() != fact.RequestKey() || len(response.GetMessages()) < 1 || len(response.GetMessages()) > 20 {
		return false
	}
	seenMsgIDs := make(map[string]struct{}, len(response.Messages))
	var previous int64
	for i, message := range response.Messages {
		if message == nil || message.GetId() <= 0 || message.GetId() > messageID || i > 0 && message.GetId() >= previous ||
			message.GetFromId() <= 0 || message.GetCreatedAtUnixMs() <= 0 || message.GetContentType() < 1 || message.GetContentType() > 4 ||
			!utf8.ValidString(message.GetContent()) || len(message.GetContent()) > 65535 || message.GetMsgId() == "" || len(message.GetMsgId()) > 64 ||
			!utf8.ValidString(message.GetMsgId()) || strings.ContainsAny(message.GetMsgId(), "\x00\r\n") {
			return false
		}
		if _, duplicate := seenMsgIDs[message.GetMsgId()]; duplicate {
			return false
		}
		seenMsgIDs[message.GetMsgId()] = struct{}{}
		previous = message.GetId()
		switch message.GetSenderType() {
		case int32(model.MessageSenderUser):
			if message.GetInitiatorId() != 0 {
				return false
			}
		case int32(model.MessageSenderBot):
			if message.GetInitiatorId() <= 0 {
				return false
			}
		default:
			return false
		}
	}
	source := response.Messages[0]
	instruction, parsed := model.ParseAgentTaskCommand(source.GetContent())
	return source.GetId() == fact.MessageID && source.GetMsgId() == fact.MsgID && source.GetFromId() == fact.ActorID &&
		source.GetContentType() == 1 && source.GetSenderType() == int32(model.MessageSenderUser) && source.GetInitiatorId() == 0 &&
		source.GetCreatedAtUnixMs() == fact.ReferenceTimeMS && parsed && instruction == fact.Instruction
}

func safeTriggerContextError(err error) error {
	code := status.Code(err)
	if errors.Is(err, context.Canceled) {
		code = codes.Canceled
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = codes.DeadlineExceeded
	}
	switch code {
	case codes.InvalidArgument:
		return status.Error(code, "invalid trigger source ID")
	case codes.NotFound:
		return status.Error(code, "persisted trigger source not found")
	case codes.PermissionDenied:
		return status.Error(code, "current team and group membership required")
	case codes.Unauthenticated:
		return status.Error(code, "service authentication required")
	case codes.Canceled:
		return status.Error(code, "trigger context read canceled")
	case codes.DeadlineExceeded:
		return status.Error(code, "trigger context read timed out")
	default:
		return status.Error(codes.Unavailable, "trigger context service unavailable")
	}
}

func (c *TriggerContextClient) Read(ctx context.Context, messageID int64) (*impb.ReadTaskTriggerContextResponse, error) {
	if ctx == nil || messageID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "positive trigger source ID and request context required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if c == nil || c.rpc == nil || c.conn != nil && c.conn.GetState() == connectivity.Shutdown {
		return nil, status.Error(codes.Unavailable, "trigger context service unavailable")
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	callCtx = metadata.NewOutgoingContext(callCtx, metadata.MD{})
	response, err := c.rpc.ReadTaskTriggerContext(callCtx, &impb.ReadTaskTriggerContextRequest{MessageId: messageID})
	if contextErr := callCtx.Err(); contextErr != nil {
		return nil, status.FromContextError(contextErr).Err()
	}
	if c.conn != nil && c.conn.GetState() == connectivity.Shutdown {
		return nil, status.Error(codes.Unavailable, "trigger context service unavailable")
	}
	if err != nil {
		return nil, safeTriggerContextError(err)
	}
	valid := validTriggerContext(response, messageID)
	if contextErr := callCtx.Err(); contextErr != nil {
		return nil, status.FromContextError(contextErr).Err()
	}
	if !valid {
		return nil, status.Error(codes.Unavailable, "invalid persisted trigger context")
	}
	return response, nil
}

func (c *TriggerContextClient) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	c.closeOnce.Do(func() { c.closeErr = c.conn.Close() })
	return c.closeErr
}
