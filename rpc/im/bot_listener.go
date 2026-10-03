package main

import (
	"errors"
	"net"
	"strconv"
	"strings"

	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
)

type botReplyConfig struct {
	ListenOn     string
	BotCode      string
	AgentDNSName string
	Files        rpcauth.CertificateFiles
	Brokers      []string
	Topic        string
}

// All fields empty disables the optional listener. Partial configuration fails
// before listening; credentials never silently fall back to a plaintext server.
func loadBotReplyConfig(getenv func(string) string) (botReplyConfig, error) {
	c := botReplyConfig{ListenOn: getenv("IM_BOT_LISTEN_ON"), BotCode: getenv("IM_BOT_CODE"),
		AgentDNSName: getenv("IM_BOT_AGENT_DNS_NAME"), Topic: getenv("IM_BOT_KAFKA_TOPIC"),
		Files: rpcauth.CertificateFiles{CertFile: getenv("IM_BOT_TLS_CERT_FILE"), KeyFile: getenv("IM_BOT_TLS_KEY_FILE"), CAFile: getenv("IM_BOT_TLS_CA_FILE")}}
	brokers := getenv("IM_BOT_KAFKA_BROKERS")
	if c.ListenOn == "" && c.BotCode == "" && c.AgentDNSName == "" && c.Topic == "" && brokers == "" &&
		c.Files.CertFile == "" && c.Files.KeyFile == "" && c.Files.CAFile == "" {
		return c, nil
	}
	_, listenPort, listenErr := net.SplitHostPort(c.ListenOn)
	port, portErr := strconv.Atoi(listenPort)
	if listenErr != nil || portErr != nil || port < 1 || port > 65535 {
		return c, errors.New("invalid IM_BOT_LISTEN_ON")
	}
	if c.BotCode == "" || len(c.BotCode) > 32 || strings.TrimSpace(c.BotCode) != c.BotCode ||
		strings.TrimSpace(c.AgentDNSName) == "" || strings.TrimSpace(c.AgentDNSName) != c.AgentDNSName || strings.Contains(c.AgentDNSName, "*") ||
		c.Files.CertFile == "" || c.Files.KeyFile == "" || c.Files.CAFile == "" || strings.TrimSpace(c.Topic) == "" {
		return c, errors.New("complete IM bot profile, TLS and Kafka configuration is required")
	}
	for _, addr := range strings.Split(brokers, ",") {
		addr = strings.TrimSpace(addr)
		host, brokerPort, err := net.SplitHostPort(addr)
		port, portErr := strconv.Atoi(brokerPort)
		if err != nil || host == "" || portErr != nil || port < 1 || port > 65535 {
			return c, errors.New("invalid IM_BOT_KAFKA_BROKERS")
		}
		c.Brokers = append(c.Brokers, addr)
	}
	return c, nil
}

type botReplyRuntime struct {
	server   *grpc.Server
	listener net.Listener
	writer   *kafka.Writer
}

func newBotReplyRuntime(c botReplyConfig, im *imServer) (*botReplyRuntime, error) {
	if c.ListenOn == "" {
		return nil, nil
	}
	if im == nil || im.db == nil || im.jwtSecret == "" || im.teamClient == nil {
		return nil, errors.New("IM bot sending requires database, JWT and User RPC")
	}
	creds, err := rpcauth.NewIMServerCredentials(c.Files, c.AgentDNSName)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", c.ListenOn)
	if err != nil {
		return nil, err
	}
	writer := newBotKafkaWriter(c.Brokers, c.Topic)
	server := grpc.NewServer(grpc.Creds(creds), grpc.MaxRecvMsgSize(8192))
	pb.RegisterIMBotServer(server, &botReplyServer{im: im, botCode: c.BotCode, agentDNSName: c.AgentDNSName,
		publisher: &kafkaBotPublisher{store: &mysqlBotSendStore{db: im.db}, writer: writer}})
	return &botReplyRuntime{server: server, listener: listener, writer: writer}, nil
}

func (r *botReplyRuntime) Stop() {
	r.server.Stop()
	_ = r.listener.Close()
	_ = r.writer.Close()
}

func registerOrdinaryIM(server *grpc.Server, impl *imServer) { pb.RegisterIMServer(server, impl) }
