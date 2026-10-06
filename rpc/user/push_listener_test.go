package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func pushListenerEnv() map[string]string {
	return map[string]string{"USER_PUSH_LISTEN_ON": "127.0.0.1:9007", "USER_PUSH_TLS_CERT_FILE": "user.pem", "USER_PUSH_TLS_KEY_FILE": "user.key", "USER_PUSH_TLS_CA_FILE": "ca.pem"}
}

func TestUserPushListenerConfigAndPortIsolation(t *testing.T) {
	if c, err := loadUserPushConfig(func(string) string { return "" }); err != nil || !c.disabled() {
		t.Fatalf("disabled: %+v %v", c, err)
	}
	if _, err := loadUserPushConfig(nil); err == nil {
		t.Fatal("nil environment accepted")
	}
	env := pushListenerEnv()
	c, err := loadUserPushConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	for key := range env {
		partial := pushListenerEnv()
		partial[key] = ""
		if _, err := loadUserPushConfig(func(k string) string { return partial[k] }); err == nil {
			t.Fatalf("missing %s accepted", key)
		}
	}
	for _, addr := range []string{"127.0.0.1:0", "localhost", "*:9007", "localhost:65536", "localhost:9007 "} {
		bad := pushListenerEnv()
		bad["USER_PUSH_LISTEN_ON"] = addr
		if _, err := loadUserPushConfig(func(k string) string { return bad[k] }); err == nil {
			t.Fatalf("address %q accepted", addr)
		}
	}
	for _, tc := range []struct {
		profile           bool
		ordinary, trigger string
	}{
		{false, "127.0.0.1:9002", ""}, {true, "127.0.0.1:9007", ""}, {true, "127.0.0.1:9002", "0.0.0.0:9007"},
	} {
		if err := validateUserPushStartup(c, tc.profile, tc.ordinary, tc.trigger); err == nil {
			t.Fatalf("accepted conflicting startup %+v", tc)
		}
	}
	if err := validateUserPushStartup(c, true, "127.0.0.1:9002", "127.0.0.1:9005"); err != nil {
		t.Fatal(err)
	}
	if err := validateUserPushStartup(userPushConfig{}, false, "invalid", "invalid"); err != nil {
		t.Fatal("disabled listener changed startup:", err)
	}
}

func pushListenerCertificates(t *testing.T) (rpcauth.CertificateFiles, rpcauth.CertificateFiles, rpcauth.CertificateFiles) {
	t.Helper()
	dir := t.TempDir()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "User Push test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
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
		cert := &x509.Certificate{SerialNumber: big.NewInt(serial), DNSNames: []string{name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		der, err := x509.CreateCertificate(rand.Reader, cert, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		privateDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		files := rpcauth.CertificateFiles{CertFile: filepath.Join(dir, name+".pem"), KeyFile: filepath.Join(dir, name+".key"), CAFile: caPath}
		if err := os.WriteFile(files.CertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(files.KeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), 0600); err != nil {
			t.Fatal(err)
		}
		return files
	}
	return issue("user.go-im.internal", x509.ExtKeyUsageServerAuth, 2), issue(pushClientDNSName, x509.ExtKeyUsageClientAuth, 3), issue("im.go-im.internal", x509.ExtKeyUsageClientAuth, 4)
}

func TestUserPushListenerOnlyServesPushCertificateAndService(t *testing.T) {
	users, mock := newTestUserServer(t)
	serverFiles, pushFiles, imFiles := pushListenerCertificates(t)
	r, err := newUserPushRuntime(userPushConfig{ListenOn: "127.0.0.1:0", Files: serverFiles}, users)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Stop)
	if services := r.server.GetServiceInfo(); len(services) != 1 || services["user.UserPush"].Metadata != "push.proto" {
		t.Fatalf("service leak: %v", services)
	}
	go func() { _ = r.server.Serve(r.listener) }()
	creds, err := rpcauth.NewServiceClientCredentials(pushFiles, "user.go-im.internal")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(r.listener.Addr().String(), grpc.WithTransportCredentials(creds), grpc.WithDisableRetry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	expectPushTeamQuery(mock, sqlmock.NewRows([]string{"status", "generation"}).AddRow(1, 7))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := pb.NewUserPushClient(conn).CheckPushTeamMember(ctx, &pb.CheckPushTeamMemberRequest{TeamId: 100, UserId: 42})
	if err != nil || got.GetGeneration() != 7 {
		t.Fatalf("Push call=%v %v", got, err)
	}
	if _, err := pb.NewUserClient(conn).GetUserInfo(ctx, &pb.GetUserInfoRequest{UserId: 1}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("ordinary User exposed: %v", err)
	}
	if _, err := pb.NewUserTriggerClient(conn).CheckTriggerTeamMember(ctx, &pb.CheckTriggerTeamMemberRequest{TeamId: 100, ActorId: 42}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("Agent trigger exposed: %v", err)
	}
	oversized := &pb.CheckPushTeamMemberRequest{TeamId: 100, UserId: 42}
	oversized.ProtoReflect().SetUnknown(bytes.Repeat([]byte{0x18, 0x01}, 2050))
	if _, err := pb.NewUserPushClient(conn).CheckPushTeamMember(ctx, oversized); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("oversized request bypassed limit: %v", err)
	}
	for name, files := range map[string]rpcauth.CertificateFiles{"IM": imFiles} {
		otherCreds, err := rpcauth.NewServiceClientCredentials(files, "user.go-im.internal")
		if err != nil {
			t.Fatal(err)
		}
		otherConn, err := grpc.NewClient(r.listener.Addr().String(), grpc.WithTransportCredentials(otherCreds))
		if err != nil {
			t.Fatal(err)
		}
		badCtx, badCancel := context.WithTimeout(context.Background(), time.Second)
		_, err = pb.NewUserPushClient(otherConn).CheckPushTeamMember(badCtx, &pb.CheckPushTeamMemberRequest{TeamId: 100, UserId: 42})
		badCancel()
		_ = otherConn.Close()
		if status.Code(err) != codes.Unavailable && status.Code(err) != codes.DeadlineExceeded {
			t.Fatalf("%s certificate accepted: %v", name, err)
		}
	}
	plain, err := grpc.NewClient(r.listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	plainCtx, plainCancel := context.WithTimeout(context.Background(), time.Second)
	_, err = pb.NewUserPushClient(plain).CheckPushTeamMember(plainCtx, &pb.CheckPushTeamMemberRequest{TeamId: 100, UserId: 42})
	plainCancel()
	_ = plain.Close()
	if status.Code(err) != codes.Unavailable && status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("plaintext accepted: %v", err)
	}
}

func TestUserPushRuntimeRejectsBadPreparationAndStops(t *testing.T) {
	users, _ := newTestUserServer(t)
	serverFiles, _, _ := pushListenerCertificates(t)
	if r, err := newUserPushRuntime(userPushConfig{}, users); r != nil || err != nil {
		t.Fatalf("disabled runtime=%v %v", r, err)
	}
	if _, err := newUserPushRuntime(userPushConfig{ListenOn: "127.0.0.1:0", Files: serverFiles}, nil); err == nil {
		t.Fatal("nil db accepted")
	}
	badFiles := serverFiles
	badFiles.KeyFile = "missing.key"
	if _, err := newUserPushRuntime(userPushConfig{ListenOn: "127.0.0.1:0", Files: badFiles}, users); err == nil {
		t.Fatal("bad cert accepted")
	}
	r, err := newUserPushRuntime(userPushConfig{ListenOn: "127.0.0.1:0", Files: serverFiles}, users)
	if err != nil {
		t.Fatal(err)
	}
	addr := r.listener.Addr().String()
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); r.Stop() }()
	}
	wg.Wait()
	r.Stop()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatal("Stop left port open")
	}
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	if _, err := newUserPushRuntime(userPushConfig{ListenOn: occupied.Addr().String(), Files: serverFiles}, users); err == nil {
		t.Fatal("occupied port accepted")
	}
}

func TestUserPushAndTriggerListenersStayIsolatedWhenBothRun(t *testing.T) {
	users, mock := newTestUserServer(t)
	serverFiles, pushFiles, imFiles := pushListenerCertificates(t)
	triggerRuntime, err := newUserTriggerRuntime(userTriggerConfig{ListenOn: "127.0.0.1:0", IMDNSName: "im.go-im.internal", Files: serverFiles}, users)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(triggerRuntime.Stop)
	pushRuntime, err := newUserPushRuntime(userPushConfig{ListenOn: "127.0.0.1:0", Files: serverFiles}, users)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pushRuntime.Stop)
	if len(triggerRuntime.server.GetServiceInfo()) != 1 || len(pushRuntime.server.GetServiceInfo()) != 1 {
		t.Fatal("private listeners registered extra services")
	}
	go func() { _ = triggerRuntime.server.Serve(triggerRuntime.listener) }()
	go func() { _ = pushRuntime.server.Serve(pushRuntime.listener) }()
	imCreds, err := rpcauth.NewServiceClientCredentials(imFiles, "user.go-im.internal")
	if err != nil {
		t.Fatal(err)
	}
	imConn, err := grpc.NewClient(triggerRuntime.listener.Addr().String(), grpc.WithTransportCredentials(imCreds))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = imConn.Close() })
	pushCreds, err := rpcauth.NewServiceClientCredentials(pushFiles, "user.go-im.internal")
	if err != nil {
		t.Fatal(err)
	}
	pushConn, err := grpc.NewClient(pushRuntime.listener.Addr().String(), grpc.WithTransportCredentials(pushCreds))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pushConn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	mock.ExpectQuery(regexp.QuoteMeta(triggerTeamQuery)).WithArgs(int64(100), int64(42), model.TeamMembershipActive, 2).
		WillReturnRows(sqlmock.NewRows([]string{"status", "generation"}).AddRow(1, 7))
	if result, err := pb.NewUserTriggerClient(imConn).CheckTriggerTeamMember(ctx, &pb.CheckTriggerTeamMemberRequest{ActorId: 42, TeamId: 100}); err != nil || result.GetGeneration() != 7 {
		t.Fatalf("Trigger call=%v %v", result, err)
	}
	expectPushTeamQuery(mock, sqlmock.NewRows([]string{"status", "generation"}).AddRow(1, 7))
	if result, err := pb.NewUserPushClient(pushConn).CheckPushTeamMember(ctx, &pb.CheckPushTeamMemberRequest{TeamId: 100, UserId: 42}); err != nil || result.GetGeneration() != 7 {
		t.Fatalf("Push call=%v %v", result, err)
	}
	if _, err := pb.NewUserPushClient(imConn).CheckPushTeamMember(ctx, &pb.CheckPushTeamMemberRequest{TeamId: 100, UserId: 42}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("Trigger listener exposed Push: %v", err)
	}
	if _, err := pb.NewUserTriggerClient(pushConn).CheckTriggerTeamMember(ctx, &pb.CheckTriggerTeamMemberRequest{ActorId: 42, TeamId: 100}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("Push listener exposed Trigger: %v", err)
	}
}

func TestUserPrivateListenerFailureStopsOrdinaryServer(t *testing.T) {
	ordinary := grpc.NewServer()
	ready := make(chan *grpc.Server, 1)
	failed := make(chan struct{}, 2)
	finished := make(chan bool, 1)
	entered := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	go func() {
		finished <- waitUserPrivateServers(func() { ready <- ordinary; close(entered); <-release }, ready, failed)
	}()
	// The callback has published the server; either private listener may fail.
	<-entered
	failed <- struct{}{}
	select {
	case stopped := <-finished:
		if !stopped {
			t.Fatal("failure reported normal completion")
		}
	case <-time.After(time.Second):
		t.Fatal("failure did not stop ordinary User RPC")
	}
	// Stop is idempotent; existing Trigger tests cover a real ordinary port.
	ordinary.Stop()
}
