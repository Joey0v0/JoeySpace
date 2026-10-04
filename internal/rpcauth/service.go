package rpcauth

import (
	"context"
	"crypto/tls"
	"errors"
	"strings"
	"unicode"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func validServiceDNS(name string) bool {
	return name != "" && !strings.Contains(name, "*") && strings.IndexFunc(name, unicode.IsSpace) < 0
}

// NewServiceClientCredentials authenticates the peer name and presents this service's certificate.
func NewServiceClientCredentials(files CertificateFiles, serverDNSName string) (credentials.TransportCredentials, error) {
	if !validServiceDNS(serverDNSName) {
		return nil, errors.New("exact service TLS server name required")
	}
	cert, roots, err := loadCertificateFiles(files)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, RootCAs: roots, ServerName: serverDNSName}), nil
}

// NewServiceServerCredentials trusts only the named service, never every certificate of the CA.
func NewServiceServerCredentials(files CertificateFiles, clientDNSName string) (credentials.TransportCredentials, error) {
	if !validServiceDNS(clientDNSName) {
		return nil, errors.New("exact service TLS client identity required")
	}
	cert, roots, err := loadCertificateFiles(files)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert,
		VerifyConnection: func(s tls.ConnectionState) error {
			if !verifiedService(s, clientDNSName) {
				return errors.New("RPC certificate is not the allowed service identity")
			}
			return nil
		}}), nil
}

func verifiedService(s tls.ConnectionState, name string) bool {
	if !validServiceDNS(name) || s.Version < tls.VersionTLS13 || len(s.PeerCertificates) == 0 || len(s.VerifiedChains) == 0 {
		return false
	}
	leaf := s.PeerCertificates[0]
	if leaf == nil {
		return false
	}
	verified := false
	for _, chain := range s.VerifiedChains {
		if len(chain) > 0 && chain[0] != nil && chain[0].Equal(leaf) {
			verified = true
			break
		}
	}
	if !verified {
		return false
	}
	for _, dns := range leaf.DNSNames {
		if dns == name {
			return true
		}
	}
	return false
}

// RequireServiceIdentity also guards against accidental registration on plaintext ports.
// User metadata and a certificate CommonName cannot substitute for a verified DNS SAN.
func RequireServiceIdentity(ctx context.Context, name string) error {
	p, ok := peer.FromContext(ctx)
	if ok {
		if info, isTLS := p.AuthInfo.(credentials.TLSInfo); isTLS && verifiedService(info.State, name) {
			return nil
		}
	}
	return status.Error(codes.Unauthenticated, "verified service TLS identity required")
}
