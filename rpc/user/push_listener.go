package main

import (
	"errors"
	"net"
	"strings"
	"sync"

	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
)

type userPushConfig struct {
	ListenOn string
	Files    rpcauth.CertificateFiles
}

func loadUserPushConfig(getenv func(string) string) (userPushConfig, error) {
	if getenv == nil {
		return userPushConfig{}, errors.New("User Push environment reader required")
	}
	c := userPushConfig{ListenOn: getenv("USER_PUSH_LISTEN_ON"), Files: rpcauth.CertificateFiles{
		CertFile: getenv("USER_PUSH_TLS_CERT_FILE"), KeyFile: getenv("USER_PUSH_TLS_KEY_FILE"), CAFile: getenv("USER_PUSH_TLS_CA_FILE"),
	}}
	return c, validateUserPushConfig(c, false)
}

func (c userPushConfig) disabled() bool {
	return c.ListenOn == "" && c.Files == (rpcauth.CertificateFiles{})
}

func validateUserPushConfig(c userPushConfig, allowZeroPort bool) error {
	if c.disabled() {
		return nil
	}
	if _, err := userTriggerPort(c.ListenOn, allowZeroPort); err != nil {
		return errors.New("invalid USER_PUSH_LISTEN_ON")
	}
	for _, path := range []string{c.Files.CertFile, c.Files.KeyFile, c.Files.CAFile} {
		if path == "" || strings.TrimSpace(path) != path {
			return errors.New("complete User Push TLS configuration required")
		}
	}
	return nil
}

func validateUserPushStartup(c userPushConfig, profile bool, ordinaryAddr, triggerAddr string) error {
	if c.disabled() {
		return nil
	}
	if err := validateUserPushConfig(c, false); err != nil {
		return err
	}
	if !profile {
		return errors.New("User Push listener requires -profile")
	}
	pushPort, _ := userTriggerPort(c.ListenOn, false)
	ordinaryPort, err := userTriggerPort(ordinaryAddr, false)
	if err != nil {
		return errors.New("invalid ordinary User listen address")
	}
	if pushPort == ordinaryPort {
		return errors.New("User Push listener must use a separate port")
	}
	if triggerAddr != "" {
		triggerPort, err := userTriggerPort(triggerAddr, false)
		if err != nil {
			return errors.New("invalid User trigger listen address")
		}
		if pushPort == triggerPort {
			return errors.New("User Push and trigger listeners need separate ports")
		}
	}
	return nil
}

type userPushRuntime struct {
	server   *grpc.Server
	listener net.Listener
	stopOnce sync.Once
}

func newUserPushRuntime(c userPushConfig, users *userServer) (*userPushRuntime, error) {
	if err := validateUserPushConfig(c, true); err != nil {
		return nil, err
	}
	if c.disabled() {
		return nil, nil
	}
	if users == nil || users.db == nil {
		return nil, errors.New("User Push listener requires a prepared database")
	}
	creds, err := rpcauth.NewServiceServerCredentials(c.Files, pushClientDNSName)
	if err != nil {
		return nil, errors.New("cannot load User Push listener certificates")
	}
	listener, err := net.Listen("tcp", c.ListenOn)
	if err != nil {
		return nil, err
	}
	server := grpc.NewServer(grpc.Creds(creds), grpc.MaxRecvMsgSize(4096))
	pb.RegisterUserPushServer(server, &pushTeamServer{db: users.db})
	return &userPushRuntime{server: server, listener: listener}, nil
}

func (r *userPushRuntime) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() { r.server.Stop(); _ = r.listener.Close() })
}
