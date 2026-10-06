package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func leaveTestCertificates(t *testing.T) (rpcauth.CertificateFiles, rpcauth.CertificateFiles, rpcauth.CertificateFiles) {
	t.Helper()
	dir := t.TempDir()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err = x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600); err != nil {
		t.Fatal(err)
	}
	issue := func(name string, usage x509.ExtKeyUsage, serial int64) rpcauth.CertificateFiles {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(serial), DNSNames: []string{name},
			NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature,
			ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		private, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		files := rpcauth.CertificateFiles{CertFile: filepath.Join(dir, name+".pem"),
			KeyFile: filepath.Join(dir, name+".key"), CAFile: caPath}
		if err := os.WriteFile(files.CertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(files.KeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600); err != nil {
			t.Fatal(err)
		}
		return files
	}
	return issue("im.go-im.internal", x509.ExtKeyUsageServerAuth, 2),
		issue("user.go-im.internal", x509.ExtKeyUsageClientAuth, 3),
		issue("agent.go-im.internal", x509.ExtKeyUsageClientAuth, 4)
}

func TestIMLeaveHandlerRequiresServiceIdentityBeforeSQL(t *testing.T) {
	s, _ := testIMServer(t)
	handler := &imLeaveServer{db: s.db, userDNSName: "user.go-im.internal"}
	got, err := handler.CloseTeamGroupMemberships(context.Background(), &pb.CloseTeamGroupMembershipsRequest{TeamId: 200, UserId: 42, Generation: 3})
	if got != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("plaintext handler call = %+v, %v", got, err)
	}
	got, err = (*imLeaveServer)(nil).CloseTeamGroupMemberships(context.Background(), &pb.CloseTeamGroupMembershipsRequest{TeamId: 200, UserId: 42, Generation: 3})
	if got != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("nil handler call = %+v, %v", got, err)
	}
}

func TestIMLeaveActualMTLSClosesScopedMembershipsAndReplaysSafely(t *testing.T) {
	im, mock := testIMServer(t)
	imFiles, permittedFiles, otherFiles := leaveTestCertificates(t)
	const permittedName = "user.go-im.internal"
	serverCreds, err := rpcauth.NewServiceServerCredentials(imFiles, permittedName)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(serverCreds))
	pb.RegisterIMLeaveServer(server, &imLeaveServer{db: im.db, userDNSName: permittedName})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	connect := func(files rpcauth.CertificateFiles) *grpc.ClientConn {
		t.Helper()
		creds, err := rpcauth.NewServiceClientCredentials(files, "im.go-im.internal")
		if err != nil {
			t.Fatal(err)
		}
		conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(creds))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	client := pb.NewIMLeaveClient(connect(permittedFiles))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request := &pb.CloseTeamGroupMembershipsRequest{TeamId: 200, UserId: 42, Generation: 3}
	if result, err := client.CloseTeamGroupMemberships(ctx, &pb.CloseTeamGroupMembershipsRequest{TeamId: 200, UserId: 42}); result != nil || status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid generation = %+v, %v", result, err)
	}
	fenceTestClose(mock, 0, 3, 2)
	result, err := client.CloseTeamGroupMemberships(ctx, request)
	if err != nil || result.GetClosedThroughGeneration() != 3 {
		t.Fatalf("first close = %+v, %v", result, err)
	}
	mock.ExpectBegin()
	fenceTestLock(mock, 3)
	mock.ExpectCommit()
	result, err = client.CloseTeamGroupMemberships(ctx, request)
	if err != nil || result.GetClosedThroughGeneration() != 3 {
		t.Fatalf("replay = %+v, %v", result, err)
	}
	wrong := pb.NewIMLeaveClient(connect(otherFiles))
	wrongCtx, wrongCancel := context.WithTimeout(context.Background(), time.Second)
	defer wrongCancel()
	result, err = wrong.CloseTeamGroupMemberships(wrongCtx, request)
	if result != nil || err == nil {
		t.Fatalf("other service certificate accepted: %+v, %v", result, err)
	}
	plain, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plain.Close() })
	plainCtx, plainCancel := context.WithTimeout(context.Background(), time.Second)
	defer plainCancel()
	result, err = pb.NewIMLeaveClient(plain).CloseTeamGroupMemberships(plainCtx, request)
	if result != nil || err == nil {
		t.Fatalf("plaintext accepted: %+v, %v", result, err)
	}
}
