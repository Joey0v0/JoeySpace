package agent

import (
	"errors"
	"net"
	"strconv"
	"strings"

	"github.com/yjydist/go-im/internal/rpcauth"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
)

// NewBotReplyClient creates a separate, reusable connection. An entirely empty
// configuration disables posting; partial configuration must never use plaintext.
// NewClient is lazy: certificates load here, peer verification happens on RPC.
func NewBotReplyClient(getenv func(string) string) (impb.IMBotClient, func(), error) {
	names := []string{"AGENT_IM_BOT_ADDR", "AGENT_IM_BOT_SERVER_NAME", "AGENT_IM_BOT_TLS_CERT_FILE", "AGENT_IM_BOT_TLS_KEY_FILE", "AGENT_IM_BOT_TLS_CA_FILE"}
	values := make([]string, len(names))
	configured := false
	for i, name := range names {
		values[i] = getenv(name)
		configured = configured || values[i] != ""
	}
	if !configured {
		return nil, func() {}, nil
	}
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value {
			return nil, nil, errors.New("complete Agent bot RPC configuration is required")
		}
	}
	host, port, err := net.SplitHostPort(values[0])
	portNumber, parseErr := strconv.Atoi(port)
	if err != nil || parseErr != nil || host == "" || strings.ContainsAny(host, " \t\r\n/") || portNumber < 1 || portNumber > 65535 {
		return nil, nil, errors.New("invalid AGENT_IM_BOT_ADDR")
	}
	if strings.ContainsAny(values[1], "* /:\t\r\n") {
		return nil, nil, errors.New("exact IM bot TLS server name is required")
	}
	creds, err := rpcauth.NewAgentClientCredentials(rpcauth.CertificateFiles{
		CertFile: values[2], KeyFile: values[3], CAFile: values[4],
	}, values[1])
	if err != nil {
		return nil, nil, errors.New("cannot load Agent bot RPC certificates")
	}
	conn, err := grpc.NewClient(values[0], grpc.WithTransportCredentials(creds), grpc.WithDisableRetry())
	if err != nil {
		return nil, nil, errors.New("cannot configure Agent bot RPC connection")
	}
	return impb.NewIMBotClient(conn), func() { _ = conn.Close() }, nil
}
