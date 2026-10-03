// Package rpcauth provides credentials for the dedicated Agent-to-IM connection.
package rpcauth

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"

	"google.golang.org/grpc/credentials"
)

// CertificateFiles contains private deployment paths, not certificate contents.
type CertificateFiles struct {
	CertFile string
	KeyFile  string
	CAFile   string
}

// NewAgentClientCredentials verifies IM's server name and presents Agent's cert.
func NewAgentClientCredentials(files CertificateFiles, imServerName string) (credentials.TransportCredentials, error) {
	if strings.TrimSpace(imServerName) == "" {
		return nil, errors.New("IM TLS server name is required")
	}
	cert, roots, err := loadCertificateFiles(files)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		RootCAs:      roots,
		ServerName:   imServerName,
	}), nil
}

// NewIMServerCredentials requires a trusted client cert with the exact Agent DNS
// identity. A trusted CA alone must not authorize other services or wildcards.
func NewIMServerCredentials(files CertificateFiles, agentDNSName string) (credentials.TransportCredentials, error) {
	if strings.TrimSpace(agentDNSName) == "" || strings.Contains(agentDNSName, "*") {
		return nil, errors.New("exact Agent TLS identity is required")
	}
	cert, roots, err := loadCertificateFiles(files)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientCAs:    roots,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
				return errors.New("verified Agent certificate is required")
			}
			for _, name := range state.PeerCertificates[0].DNSNames {
				if name == agentDNSName {
					return nil
				}
			}
			return errors.New("RPC client certificate is not the allowed Agent identity")
		},
	}), nil
}

func loadCertificateFiles(files CertificateFiles) (tls.Certificate, *x509.CertPool, error) {
	if strings.TrimSpace(files.CertFile) == "" || strings.TrimSpace(files.KeyFile) == "" || strings.TrimSpace(files.CAFile) == "" {
		return tls.Certificate{}, nil, errors.New("RPC certificate, private key and CA paths are required")
	}
	cert, err := tls.LoadX509KeyPair(files.CertFile, files.KeyFile)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("cannot load RPC certificate and private key: %w", err)
	}
	caPEM, err := os.ReadFile(files.CAFile)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("cannot read RPC CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return tls.Certificate{}, nil, errors.New("RPC CA file contains no certificates")
	}
	return cert, roots, nil
}
