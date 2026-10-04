package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yjydist/go-im/internal/rpcauth"
)

// Local ephemeral certificates only; never production credentials.
func triggerTestCertificates(t *testing.T) (rpcauth.CertificateFiles, rpcauth.CertificateFiles, rpcauth.CertificateFiles) {
	t.Helper()
	dir := t.TempDir()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "trigger test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, e := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	ca, e = x509.ParseCertificate(der)
	if e != nil {
		t.Fatal(e)
	}
	caPath := filepath.Join(dir, "ca.pem")
	if e := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); e != nil {
		t.Fatal(e)
	}
	issue := func(name string, purpose x509.ExtKeyUsage, serial int64) rpcauth.CertificateFiles {
		t.Helper()
		k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		c := &x509.Certificate{SerialNumber: big.NewInt(serial), DNSNames: []string{name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{purpose}}
		b, e := x509.CreateCertificate(rand.Reader, c, ca, &k.PublicKey, key)
		if e != nil {
			t.Fatal(e)
		}
		kb, e := x509.MarshalPKCS8PrivateKey(k)
		if e != nil {
			t.Fatal(e)
		}
		f := rpcauth.CertificateFiles{CertFile: filepath.Join(dir, name+".pem"), KeyFile: filepath.Join(dir, name+".key"), CAFile: caPath}
		if e := os.WriteFile(f.CertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: b}), 0600); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(f.KeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb}), 0600); e != nil {
			t.Fatal(e)
		}
		return f
	}
	return issue("user.go-im.internal", x509.ExtKeyUsageServerAuth, 2), issue("im.go-im.internal", x509.ExtKeyUsageClientAuth, 3), issue("agent.go-im.internal", x509.ExtKeyUsageClientAuth, 4)
}
