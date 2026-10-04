package main

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
)

type userTriggerConfig struct {
	ListenOn  string
	IMDNSName string
	Files     rpcauth.CertificateFiles
}

func loadUserTriggerConfig(getenv func(string) string) (userTriggerConfig, error) {
	c := userTriggerConfig{
		ListenOn: getenv("USER_TRIGGER_LISTEN_ON"), IMDNSName: getenv("USER_TRIGGER_IM_DNS_NAME"),
		Files: rpcauth.CertificateFiles{CertFile: getenv("USER_TRIGGER_TLS_CERT_FILE"),
			KeyFile: getenv("USER_TRIGGER_TLS_KEY_FILE"), CAFile: getenv("USER_TRIGGER_TLS_CA_FILE")},
	}
	return c, validateUserTriggerConfig(c, false)
}

func (c userTriggerConfig) disabled() bool {
	return c.ListenOn == "" && c.IMDNSName == "" && c.Files == (rpcauth.CertificateFiles{})
}

func validateUserTriggerConfig(c userTriggerConfig, allowZeroPort bool) error {
	if c.disabled() {
		return nil
	}
	if _, err := userTriggerPort(c.ListenOn, allowZeroPort); err != nil {
		return errors.New("invalid USER_TRIGGER_LISTEN_ON")
	}
	if !userTriggerDNS(c.IMDNSName) {
		return errors.New("invalid USER_TRIGGER_IM_DNS_NAME")
	}
	for _, path := range []string{c.Files.CertFile, c.Files.KeyFile, c.Files.CAFile} {
		if path == "" || strings.TrimSpace(path) != path {
			return errors.New("complete User trigger TLS configuration is required")
		}
	}
	return nil
}

func userTriggerDNS(name string) bool {
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

func userTriggerPort(addr string, allowZero bool) (int, error) {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil || strings.IndexFunc(addr, unicode.IsSpace) >= 0 ||
		(host != "" && net.ParseIP(host) == nil && !userTriggerDNS(host)) {
		return 0, errors.New("invalid User trigger address")
	}
	for _, c := range portText {
		if c < '0' || c > '9' {
			return 0, errors.New("invalid User trigger port")
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 || port == 0 && !allowZero {
		return 0, errors.New("invalid User trigger port")
	}
	return port, nil
}

// Reject sharing the ordinary User port, including wildcard/loopback aliases.
func validateUserTriggerStartup(c userTriggerConfig, profile bool, ordinaryAddr string) error {
	if c.disabled() {
		return nil
	}
	if err := validateUserTriggerConfig(c, false); err != nil {
		return err
	}
	if !profile {
		return errors.New("User trigger listener requires -profile")
	}
	triggerPort, _ := userTriggerPort(c.ListenOn, false)
	ordinaryPort, err := userTriggerPort(ordinaryAddr, false)
	if err != nil {
		return errors.New("invalid ordinary User listen address")
	}
	if triggerPort == ordinaryPort {
		return errors.New("User trigger listener must use a separate port")
	}
	return nil
}

type userTriggerRuntime struct {
	server   *grpc.Server
	listener net.Listener
	stopOnce sync.Once
}

func newUserTriggerRuntime(c userTriggerConfig, users *userServer) (*userTriggerRuntime, error) {
	if err := validateUserTriggerConfig(c, true); err != nil {
		return nil, err
	}
	if c.disabled() {
		return nil, nil
	}
	if users == nil || users.db == nil {
		return nil, errors.New("User trigger listener requires a prepared database")
	}
	creds, err := rpcauth.NewServiceServerCredentials(c.Files, c.IMDNSName)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", c.ListenOn)
	if err != nil {
		return nil, err
	}
	server := grpc.NewServer(grpc.Creds(creds), grpc.MaxRecvMsgSize(4096))
	pb.RegisterUserTriggerServer(server, &triggerTeamServer{db: users.db, imDNSName: c.IMDNSName})
	return &userTriggerRuntime{server: server, listener: listener}, nil
}

func (r *userTriggerRuntime) Stop() {
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
func waitUserTriggerServers(startOrdinary func(), ordinaryReady <-chan *grpc.Server, triggerFailed <-chan struct{}) bool {
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
