package rpcauth

import (
	"crypto/tls"
	"errors"
	"strings"
)

func validNotificationServiceName(name string) bool {
	return name != "" && strings.TrimSpace(name) == name && !strings.ContainsAny(name, "* /\\\t\r\n:@")
}

// VerifyNotificationPeer requires both normal certificate verification and the exact service SAN.
func VerifyNotificationPeer(state *tls.ConnectionState, serviceName string) error {
	if !validNotificationServiceName(serviceName) || state == nil || len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
		return errors.New("verified notification service certificate required")
	}
	for _, name := range state.PeerCertificates[0].DNSNames {
		if name == serviceName {
			return nil
		}
	}
	return errors.New("notification service certificate identity rejected")
}

func NewNotificationServerTLSConfig(files CertificateFiles, pushDNSName string) (*tls.Config, error) {
	if !validNotificationServiceName(pushDNSName) {
		return nil, errors.New("exact notification Push identity required")
	}
	cert, roots, err := loadCertificateFiles(files)
	if err != nil {
		return nil, errors.New("cannot load notification TLS credentials")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, ClientCAs: roots,
		ClientAuth: tls.RequireAndVerifyClientCert, VerifyConnection: func(state tls.ConnectionState) error {
			return VerifyNotificationPeer(&state, pushDNSName)
		}}, nil
}

func NewNotificationClientTLSConfig(files CertificateFiles, wsDNSName string) (*tls.Config, error) {
	if !validNotificationServiceName(wsDNSName) {
		return nil, errors.New("exact notification WS identity required")
	}
	cert, roots, err := loadCertificateFiles(files)
	if err != nil {
		return nil, errors.New("cannot load notification TLS credentials")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, RootCAs: roots, ServerName: wsDNSName,
		VerifyConnection: func(state tls.ConnectionState) error { return VerifyNotificationPeer(&state, wsDNSName) }}, nil
}
