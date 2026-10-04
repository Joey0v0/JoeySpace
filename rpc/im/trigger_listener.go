package main

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"gorm.io/gorm"
)

type imTriggerConfig struct {
	ListenOn     string
	AgentDNSName string
	Files        rpcauth.CertificateFiles
}

func loadIMTriggerConfig(getenv func(string) string) (imTriggerConfig, error) {
	if getenv == nil {
		return imTriggerConfig{}, errors.New("IM trigger environment reader is required")
	}
	c := imTriggerConfig{
		ListenOn: getenv("IM_TRIGGER_LISTEN_ON"), AgentDNSName: getenv("IM_TRIGGER_AGENT_DNS_NAME"),
		Files: rpcauth.CertificateFiles{CertFile: getenv("IM_TRIGGER_TLS_CERT_FILE"),
			KeyFile: getenv("IM_TRIGGER_TLS_KEY_FILE"), CAFile: getenv("IM_TRIGGER_TLS_CA_FILE")},
	}
	return c, validateIMTriggerConfig(c, false)
}

func loadIMTriggerTeamConfig(c imTriggerConfig, getenv func(string) string) (triggerTeamClientConfig, error) {
	if c.disabled() {
		return triggerTeamClientConfig{}, nil
	}
	teams, err := loadTriggerTeamClientConfig(getenv)
	if err != nil {
		return teams, err
	}
	if teams.Addr == "" {
		return teams, errors.New("IM trigger listener requires complete IM_TRIGGER_USER configuration")
	}
	return teams, nil
}

func (c imTriggerConfig) disabled() bool {
	return c.ListenOn == "" && c.AgentDNSName == "" && c.Files == (rpcauth.CertificateFiles{})
}

func validateIMTriggerConfig(c imTriggerConfig, allowZeroPort bool) error {
	if c.disabled() {
		return nil
	}
	if _, err := imTriggerPort(c.ListenOn, allowZeroPort); err != nil {
		return errors.New("invalid IM_TRIGGER_LISTEN_ON")
	}
	if !imTriggerDNS(c.AgentDNSName) {
		return errors.New("invalid IM_TRIGGER_AGENT_DNS_NAME")
	}
	for _, path := range []string{c.Files.CertFile, c.Files.KeyFile, c.Files.CAFile} {
		if path == "" || strings.TrimSpace(path) != path {
			return errors.New("complete IM trigger TLS configuration is required")
		}
	}
	return nil
}

func imTriggerDNS(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func imTriggerPort(addr string, allowZero bool) (int, error) {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil || strings.IndexFunc(addr, unicode.IsSpace) >= 0 ||
		(host != "" && net.ParseIP(host) == nil && !imTriggerDNS(host)) {
		return 0, errors.New("invalid IM trigger address")
	}
	for _, c := range portText {
		if c < '0' || c > '9' {
			return 0, errors.New("invalid IM trigger port")
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 || port == 0 && !allowZero {
		return 0, errors.New("invalid IM trigger port")
	}
	return port, nil
}

// Reject sharing ordinary IM or bot ports, including wildcard/loopback aliases.
func validateIMTriggerStartup(c imTriggerConfig, ordinaryAddr, botAddr string) error {
	if c.disabled() {
		return nil
	}
	if err := validateIMTriggerConfig(c, false); err != nil {
		return err
	}
	triggerPort, _ := imTriggerPort(c.ListenOn, false)
	for i, addr := range []string{ordinaryAddr, botAddr} {
		if i == 1 && addr == "" {
			continue
		}
		port, err := imTriggerPort(addr, false)
		if err != nil {
			return errors.New("invalid existing IM listen address")
		}
		if triggerPort == port {
			return errors.New("IM trigger listener must use a separate port")
		}
	}
	return nil
}

type imTriggerRuntime struct {
	server   *grpc.Server
	listener net.Listener
	stopOnce sync.Once
}

func newIMTriggerRuntime(c imTriggerConfig, db *gorm.DB, teams triggerTeamEligibility) (*imTriggerRuntime, error) {
	if err := validateIMTriggerConfig(c, true); err != nil {
		return nil, err
	}
	if c.disabled() {
		return nil, nil
	}
	if db == nil || teams == nil {
		return nil, errors.New("IM trigger listener requires a prepared database and User eligibility client")
	}
	if client, ok := teams.(*triggerTeamClient); ok && (client == nil || client.rpc == nil) {
		return nil, errors.New("IM trigger listener requires a prepared User eligibility client")
	}
	creds, err := rpcauth.NewServiceServerCredentials(c.Files, c.AgentDNSName)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", c.ListenOn)
	if err != nil {
		return nil, err
	}
	server := grpc.NewServer(grpc.Creds(creds), grpc.MaxRecvMsgSize(4096))
	pb.RegisterIMTriggerServer(server, &triggerContextServer{db: db, teams: teams, agentDNSName: c.AgentDNSName})
	return &imTriggerRuntime{server: server, listener: listener}, nil
}

func (r *imTriggerRuntime) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		r.server.Stop()
		_ = r.listener.Close()
	})
}

// Wait only for ordinary completion or a dedicated listener failure. On Linux,
// go-zero Start can keep waiting for a shutdown notification after Serve exits.
func waitIMTriggerServers(startOrdinary func(), ordinaryReady <-chan *grpc.Server, triggerFailed <-chan struct{}) bool {
	ordinaryDone := make(chan any, 1)
	go func() {
		// Re-panic in the caller so its runtime/database cleanup executes.
		defer func() { ordinaryDone <- recover() }()
		startOrdinary()
	}()
	select {
	case failure := <-ordinaryDone:
		if failure != nil {
			panic(failure)
		}
		return false
	case <-triggerFailed:
		// The registration callback publishes this server before starting Trigger.
		// zrpc.Stop only closes logging; stop the actual grpc.Server instead.
		(<-ordinaryReady).Stop()
		return true
	}
}
