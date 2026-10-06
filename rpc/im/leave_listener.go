package main

import (
	"errors"
	"net"
	"strings"
	"sync"

	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"gorm.io/gorm"
)

const imLeaveUserDNSName = "user.go-im.internal"

type imLeaveConfig struct {
	ListenOn string
	Files    rpcauth.CertificateFiles
}

func loadIMLeaveConfig(getenv func(string) string) (imLeaveConfig, error) {
	if getenv == nil {
		return imLeaveConfig{}, errors.New("IM leave environment reader is required")
	}
	c := imLeaveConfig{ListenOn: getenv("IM_LEAVE_LISTEN_ON"), Files: rpcauth.CertificateFiles{
		CertFile: getenv("IM_LEAVE_TLS_CERT_FILE"), KeyFile: getenv("IM_LEAVE_TLS_KEY_FILE"), CAFile: getenv("IM_LEAVE_TLS_CA_FILE"),
	}}
	return c, validateIMLeaveConfig(c, false)
}

func (c imLeaveConfig) disabled() bool {
	return c.ListenOn == "" && c.Files == (rpcauth.CertificateFiles{})
}

func validateIMLeaveConfig(c imLeaveConfig, allowZeroPort bool) error {
	if c.disabled() {
		return nil
	}
	if _, err := imTriggerPort(c.ListenOn, allowZeroPort); err != nil {
		return errors.New("invalid IM_LEAVE_LISTEN_ON")
	}
	for _, path := range []string{c.Files.CertFile, c.Files.KeyFile, c.Files.CAFile} {
		if path == "" || strings.TrimSpace(path) != path {
			return errors.New("complete IM leave TLS configuration is required")
		}
	}
	return nil
}

func validateIMLeaveStartup(c imLeaveConfig, ordinaryAddr, botAddr, triggerAddr string) error {
	if c.disabled() {
		return nil
	}
	if err := validateIMLeaveConfig(c, false); err != nil {
		return err
	}
	leavePort, _ := imTriggerPort(c.ListenOn, false)
	for i, addr := range []string{ordinaryAddr, botAddr, triggerAddr} {
		if i > 0 && addr == "" {
			continue
		}
		port, err := imTriggerPort(addr, false)
		if err != nil {
			return errors.New("invalid existing IM listen address")
		}
		if leavePort == port {
			return errors.New("IM leave listener must use a separate port")
		}
	}
	return nil
}

type imLeaveRuntime struct {
	server   *grpc.Server
	listener net.Listener
	stopOnce sync.Once
}

func newIMLeaveRuntime(c imLeaveConfig, db *gorm.DB) (*imLeaveRuntime, error) {
	if err := validateIMLeaveConfig(c, true); err != nil {
		return nil, err
	}
	if c.disabled() {
		return nil, nil
	}
	if db == nil {
		return nil, errors.New("IM leave listener requires a prepared database")
	}
	creds, err := rpcauth.NewServiceServerCredentials(c.Files, imLeaveUserDNSName)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", c.ListenOn)
	if err != nil {
		return nil, err
	}
	server := grpc.NewServer(grpc.Creds(creds), grpc.MaxRecvMsgSize(4096))
	pb.RegisterIMLeaveServer(server, &imLeaveServer{db: db, userDNSName: imLeaveUserDNSName})
	return &imLeaveRuntime{server: server, listener: listener}, nil
}

func (r *imLeaveRuntime) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		r.server.Stop()
		_ = r.listener.Close()
	})
}
