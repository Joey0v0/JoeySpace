package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yjydist/go-im/internal/rpcauth"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
)

type triggerTeamCheckFunc func(context.Context, *userpb.CheckTriggerTeamMemberRequest) (*userpb.CheckTriggerTeamMemberResponse, error)

func (f triggerTeamCheckFunc) CheckTriggerTeamMember(ctx context.Context, req *userpb.CheckTriggerTeamMemberRequest, _ ...grpc.CallOption) (*userpb.CheckTriggerTeamMemberResponse, error) {
	return f(ctx, req)
}

func validTriggerClientConfig() triggerTeamClientConfig {
	return triggerTeamClientConfig{Addr: "user-rpc:9004", ServerDNSName: "user.go-im.internal",
		Files: rpcauth.CertificateFiles{CertFile: "cert.pem", KeyFile: "key.pem", CAFile: "ca.pem"}}
}

func triggerClientEnvironment(c triggerTeamClientConfig) func(string) string {
	values := map[string]string{"IM_TRIGGER_USER_RPC_ADDR": c.Addr, "IM_TRIGGER_USER_TLS_SERVER_NAME": c.ServerDNSName,
		"IM_TRIGGER_USER_TLS_CERT_FILE": c.Files.CertFile, "IM_TRIGGER_USER_TLS_KEY_FILE": c.Files.KeyFile, "IM_TRIGGER_USER_TLS_CA_FILE": c.Files.CAFile}
	return func(name string) string { return values[name] }
}

func TestTriggerTeamClientConfigDisabledOrFailsClosed(t *testing.T) {
	c, err := loadTriggerTeamClientConfig(func(string) string { return "" })
	if err != nil || c != (triggerTeamClientConfig{}) {
		t.Fatalf("disabled config: %+v %v", c, err)
	}
	client, err := newTriggerTeamClient(c)
	if client != nil || err != nil || client.Close() != nil {
		t.Fatalf("disabled client: %v %v", client, err)
	}
	for name, mutate := range map[string]func(*triggerTeamClientConfig){
		"missing address":         func(c *triggerTeamClientConfig) { c.Addr = "" },
		"missing name":            func(c *triggerTeamClientConfig) { c.ServerDNSName = "" },
		"missing cert":            func(c *triggerTeamClientConfig) { c.Files.CertFile = "" },
		"missing key":             func(c *triggerTeamClientConfig) { c.Files.KeyFile = "" },
		"missing CA":              func(c *triggerTeamClientConfig) { c.Files.CAFile = "" },
		"URI":                     func(c *triggerTeamClientConfig) { c.Addr = "http://user:9004" },
		"resolver URI":            func(c *triggerTeamClientConfig) { c.Addr = "dns:///user:9004" },
		"blank host":              func(c *triggerTeamClientConfig) { c.Addr = ":9004" },
		"space host":              func(c *triggerTeamClientConfig) { c.Addr = "user rpc:9004" },
		"unicode space":           func(c *triggerTeamClientConfig) { c.Addr = "user\u2003rpc:9004" },
		"trailing space":          func(c *triggerTeamClientConfig) { c.Addr += " " },
		"port zero":               func(c *triggerTeamClientConfig) { c.Addr = "user:0" },
		"port high":               func(c *triggerTeamClientConfig) { c.Addr = "user:65536" },
		"port negative":           func(c *triggerTeamClientConfig) { c.Addr = "user:-1" },
		"port sign":               func(c *triggerTeamClientConfig) { c.Addr = "user:+9004" },
		"port text":               func(c *triggerTeamClientConfig) { c.Addr = "user:rpc" },
		"bad bracket host":        func(c *triggerTeamClientConfig) { c.Addr = "[user]:9004" },
		"wildcard DNS":            func(c *triggerTeamClientConfig) { c.ServerDNSName = "*.internal" },
		"blank DNS":               func(c *triggerTeamClientConfig) { c.ServerDNSName = " " },
		"internal DNS whitespace": func(c *triggerTeamClientConfig) { c.ServerDNSName = "user\t.internal" },
		"DNS URI":                 func(c *triggerTeamClientConfig) { c.ServerDNSName = "https://user.internal" },
		"blank path":              func(c *triggerTeamClientConfig) { c.Files.KeyFile = "\t" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := validTriggerClientConfig()
			mutate(&invalid)
			if _, err := loadTriggerTeamClientConfig(triggerClientEnvironment(invalid)); err == nil {
				t.Fatal("invalid environment accepted")
			}
			if client, err := newTriggerTeamClient(invalid); err == nil || client != nil {
				t.Fatalf("direct constructor did not fail closed: %v %v", client, err)
			}
		})
	}
	for _, address := range []string{"127.0.0.1:9004", "[::1]:9004", "user-rpc:65535"} {
		c := validTriggerClientConfig()
		c.Addr = address
		if _, err := loadTriggerTeamClientConfig(triggerClientEnvironment(c)); err != nil {
			t.Fatalf("valid address %s: %v", address, err)
		}
	}
	c = validTriggerClientConfig()
	c.Files.CertFile = "private-nonexistent-certificate"
	if client, err := newTriggerTeamClient(c); err == nil || client != nil || strings.Contains(err.Error(), c.Files.CertFile) {
		t.Fatalf("certificate error was not safe: %v %v", client, err)
	}
}

func TestTriggerTeamCheckUsesOnlyIDsAndClearsEveryCallerMetadataKey(t *testing.T) {
	client := &triggerTeamClient{rpc: triggerTeamCheckFunc(func(ctx context.Context, req *userpb.CheckTriggerTeamMemberRequest) (*userpb.CheckTriggerTeamMemberResponse, error) {
		md, ok := metadata.FromOutgoingContext(ctx)
		deadline, bounded := ctx.Deadline()
		if !ok || len(md) != 0 || !bounded || time.Until(deadline) > 2*time.Second || req.ActorId != 9007199254740993 || req.TeamId != 9007199254740995 {
			t.Fatalf("request %v metadata %v deadline %v", req, md, deadline)
		}
		return &userpb.CheckTriggerTeamMemberResponse{ActorId: req.ActorId, TeamId: req.TeamId}, nil
	})}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer private-token", "actor-id", "1", "service", "agent", "cookie", "session", "x-extra", "secret"))
	if err := client.Check(ctx, 9007199254740993, 9007199254740995); err != nil {
		t.Fatal(err)
	}
}

func TestTriggerTeamCheckRejectsNilAndWrongEchoAndMasksServerErrors(t *testing.T) {
	for _, response := range []*userpb.CheckTriggerTeamMemberResponse{nil, {}, {ActorId: 7, TeamId: 10}, {ActorId: 9, TeamId: 8}} {
		client := &triggerTeamClient{rpc: triggerTeamCheckFunc(func(context.Context, *userpb.CheckTriggerTeamMemberRequest) (*userpb.CheckTriggerTeamMemberResponse, error) {
			return response, nil
		})}
		if err := client.Check(context.Background(), 9, 10); status.Code(err) != codes.Unavailable {
			t.Fatalf("response %v: %v", response, err)
		}
	}
	for _, code := range []codes.Code{codes.PermissionDenied, codes.Unauthenticated, codes.Canceled, codes.DeadlineExceeded, codes.Unavailable, codes.Internal, codes.NotFound, codes.InvalidArgument, codes.Unimplemented} {
		client := &triggerTeamClient{rpc: triggerTeamCheckFunc(func(context.Context, *userpb.CheckTriggerTeamMemberRequest) (*userpb.CheckTriggerTeamMemberResponse, error) {
			return nil, status.Error(code, "private DB/TLS/password details")
		})}
		err := client.Check(context.Background(), 9, 10)
		want := codes.Unavailable
		if code == codes.PermissionDenied || code == codes.Unauthenticated || code == codes.Canceled || code == codes.DeadlineExceeded {
			want = code
		}
		if status.Code(err) != want || strings.Contains(err.Error(), "private") {
			t.Fatalf("%s: %v", code, err)
		}
	}
}

func TestTriggerTeamCheckLocalGuardsAndCallerCancellationDoNotReachRPC(t *testing.T) {
	client := &triggerTeamClient{rpc: triggerTeamCheckFunc(func(context.Context, *userpb.CheckTriggerTeamMemberRequest) (*userpb.CheckTriggerTeamMemberResponse, error) {
		t.Fatal("local invalid request reached RPC")
		return nil, nil
	})}
	for _, ids := range [][2]int64{{0, 1}, {1, 0}, {-1, 1}, {1, -1}} {
		if err := client.Check(context.Background(), ids[0], ids[1]); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
	if err := client.Check(nil, 1, 2); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.Check(ctx, 1, 2); status.Code(err) != codes.Canceled {
		t.Fatal(err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := client.Check(ctx, 1, 2); status.Code(err) != codes.DeadlineExceeded {
		t.Fatal(err)
	}
	for _, client := range []*triggerTeamClient{nil, {}} {
		if err := client.Check(context.Background(), 1, 2); status.Code(err) != codes.Unavailable {
			t.Fatal(err)
		}
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTriggerTeamCheckEnforcesTwoSecondBudgetAndRejectsLateSuccess(t *testing.T) {
	client := &triggerTeamClient{rpc: triggerTeamCheckFunc(func(ctx context.Context, req *userpb.CheckTriggerTeamMemberRequest) (*userpb.CheckTriggerTeamMemberResponse, error) {
		<-ctx.Done()
		return &userpb.CheckTriggerTeamMemberResponse{ActorId: req.ActorId, TeamId: req.TeamId}, nil
	})}
	started := time.Now()
	if err := client.Check(context.Background(), 1, 2); status.Code(err) != codes.DeadlineExceeded {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 1900*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("budget: %v", elapsed)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started = time.Now()
	if err := client.Check(ctx, 1, 2); status.Code(err) != codes.DeadlineExceeded {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("caller deadline enlarged: %v", elapsed)
	}
}

// Ephemeral test credentials only; no production certificates are written.
func triggerClientCertificates(t *testing.T) map[string]rpcauth.CertificateFiles {
	t.Helper()
	dir := t.TempDir()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	files := map[string]rpcauth.CertificateFiles{}
	for i, name := range []string{"user.go-im.internal", "im.go-im.internal", "agent.go-im.internal"} {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		purpose := x509.ExtKeyUsageClientAuth
		if i == 0 {
			purpose = x509.ExtKeyUsageServerAuth
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(int64(i + 2)), DNSNames: []string{name}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter,
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{purpose}}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		private, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		f := rpcauth.CertificateFiles{CertFile: filepath.Join(dir, name+".pem"), KeyFile: filepath.Join(dir, name+".key"), CAFile: caPath}
		if err := os.WriteFile(f.CertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.KeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600); err != nil {
			t.Fatal(err)
		}
		files[name] = f
	}
	return files
}

type triggerClientRPCStub struct {
	userpb.UnimplementedUserTriggerServer
	check triggerTeamCheckFunc
}

func (s *triggerClientRPCStub) CheckTriggerTeamMember(ctx context.Context, req *userpb.CheckTriggerTeamMemberRequest) (*userpb.CheckTriggerTeamMemberResponse, error) {
	return s.check(ctx, req)
}

func startTriggerClientTestServer(t *testing.T, files rpcauth.CertificateFiles, tlsEnabled bool, check triggerTeamCheckFunc) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var options []grpc.ServerOption
	if tlsEnabled {
		creds, err := rpcauth.NewServiceServerCredentials(files, "im.go-im.internal")
		if err != nil {
			_ = listener.Close()
			t.Fatal(err)
		}
		options = append(options, grpc.Creds(creds))
	}
	server := grpc.NewServer(options...)
	userpb.RegisterUserTriggerServer(server, &triggerClientRPCStub{check: check})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	return listener.Addr().String()
}

func TestTriggerTeamClientActualMTLSPreservesIDsWithoutTokenAndDoesNotRetryDenied(t *testing.T) {
	files := triggerClientCertificates(t)
	var calls atomic.Int32
	address := startTriggerClientTestServer(t, files["user.go-im.internal"], true, func(ctx context.Context, req *userpb.CheckTriggerTeamMemberRequest) (*userpb.CheckTriggerTeamMemberResponse, error) {
		if err := rpcauth.RequireServiceIdentity(ctx, "im.go-im.internal"); err != nil {
			return nil, err
		}
		md, _ := metadata.FromIncomingContext(ctx)
		for _, key := range []string{"authorization", "actor-id", "service", "x-extra"} {
			if len(md.Get(key)) != 0 {
				t.Errorf("caller metadata forwarded: %s", key)
			}
		}
		if req.ActorId != 9007199254740993 || req.TeamId != 9007199254740995 {
			t.Error("large IDs changed")
		}
		if calls.Add(1) == 1 {
			return &userpb.CheckTriggerTeamMemberResponse{ActorId: req.ActorId, TeamId: req.TeamId}, nil
		}
		return nil, status.Error(codes.PermissionDenied, "private revoked user details")
	})
	client, err := newTriggerTeamClient(triggerTeamClientConfig{Addr: address, ServerDNSName: "user.go-im.internal", Files: files["im.go-im.internal"]})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer private-token", "actor-id", "1", "service", "agent", "x-extra", "private"))
	if err := client.Check(ctx, 9007199254740993, 9007199254740995); err != nil {
		t.Fatal(err)
	}
	if err := client.Check(ctx, 9007199254740993, 9007199254740995); status.Code(err) != codes.PermissionDenied || strings.Contains(err.Error(), "private") {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("unexpected retry count %d", calls.Load())
	}
	if client.Close() != nil || client.Close() != nil {
		t.Fatal("Close is not safe when repeated")
	}
	if err := client.Check(context.Background(), 1, 2); status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.Check(canceled, 1, 2); status.Code(err) != codes.Canceled {
		t.Fatalf("caller cancellation must precede the closed-connection guard: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("closed client made another RPC: %d", calls.Load())
	}
}

func TestTriggerTeamClientActualTLSRejectsWrongRoleServerNameCAAndPlaintext(t *testing.T) {
	files, foreign := triggerClientCertificates(t), triggerClientCertificates(t)
	var calls atomic.Int32
	check := triggerTeamCheckFunc(func(context.Context, *userpb.CheckTriggerTeamMemberRequest) (*userpb.CheckTriggerTeamMemberResponse, error) {
		calls.Add(1)
		return nil, errors.New("must not enter handler")
	})
	tlsAddress := startTriggerClientTestServer(t, files["user.go-im.internal"], true, check)
	plainAddress := startTriggerClientTestServer(t, files["user.go-im.internal"], false, check)
	for _, mode := range []string{"agent role", "wrong server", "wrong CA", "plaintext"} {
		t.Run(mode, func(t *testing.T) {
			cfg := triggerTeamClientConfig{Addr: tlsAddress, ServerDNSName: "user.go-im.internal", Files: files["im.go-im.internal"]}
			switch mode {
			case "agent role":
				cfg.Files = files["agent.go-im.internal"]
			case "wrong server":
				cfg.ServerDNSName = "other.go-im.internal"
			case "wrong CA":
				cfg.Files.CAFile = foreign["im.go-im.internal"].CAFile
			case "plaintext":
				cfg.Addr = plainAddress
			}
			client, err := newTriggerTeamClient(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			err = client.Check(ctx, 1, 2)
			if (status.Code(err) != codes.Unavailable && status.Code(err) != codes.DeadlineExceeded) || strings.Contains(err.Error(), "certificate") || strings.Contains(err.Error(), "private") {
				t.Fatal(err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("failed TLS check reached handler %d times", calls.Load())
	}
}

func TestTriggerTeamCheckMapsConnectionClosedDuringRPCWithoutMaskingCallerCancellation(t *testing.T) {
	files := triggerClientCertificates(t)
	for _, cancelCaller := range []bool{false, true} {
		client, err := newTriggerTeamClient(triggerTeamClientConfig{Addr: "127.0.0.1:9004", ServerDNSName: "user.go-im.internal", Files: files["im.go-im.internal"]})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		client.rpc = triggerTeamCheckFunc(func(context.Context, *userpb.CheckTriggerTeamMemberRequest) (*userpb.CheckTriggerTeamMemberResponse, error) {
			if cancelCaller {
				cancel()
			}
			if err := client.Close(); err != nil {
				t.Error(err)
			}
			return nil, status.Error(codes.Canceled, "private connection closing details")
		})
		err = client.Check(ctx, 1, 2)
		cancel()
		want := codes.Unavailable
		if cancelCaller {
			want = codes.Canceled
		}
		if status.Code(err) != want || strings.Contains(err.Error(), "private") {
			t.Fatalf("cancel caller %v: %v", cancelCaller, err)
		}
	}
}

func TestTriggerTeamClientActualTLSBoundsResponseAndCallerDeadline(t *testing.T) {
	files := triggerClientCertificates(t)
	var calls atomic.Int32
	address := startTriggerClientTestServer(t, files["user.go-im.internal"], true, func(ctx context.Context, req *userpb.CheckTriggerTeamMemberRequest) (*userpb.CheckTriggerTeamMemberResponse, error) {
		calls.Add(1)
		if req.ActorId == 2 {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		response := &userpb.CheckTriggerTeamMemberResponse{ActorId: req.ActorId, TeamId: req.TeamId}
		unknown := protowire.AppendTag(nil, 3, protowire.BytesType)
		response.ProtoReflect().SetUnknown(protowire.AppendBytes(unknown, bytes.Repeat([]byte("x"), 8192)))
		return response, nil
	})
	client, err := newTriggerTeamClient(triggerTeamClientConfig{Addr: address, ServerDNSName: "user.go-im.internal", Files: files["im.go-im.internal"]})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Check(context.Background(), 1, 2); status.Code(err) != codes.Unavailable {
		t.Fatalf("oversize response: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := client.Check(ctx, 2, 2); status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("deadline: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("unexpected retry count %d", calls.Load())
	}
}
