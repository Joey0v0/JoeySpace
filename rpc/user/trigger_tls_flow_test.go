package main

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const triggerFlowActor int64 = 9007199254740993

func triggerFlowClient(t *testing.T, addr string, files rpcauth.CertificateFiles, name string) pb.UserTriggerClient {
	t.Helper()
	creds, e := rpcauth.NewServiceClientCredentials(files, name)
	if e != nil {
		t.Fatal(e)
	}
	conn, e := grpc.NewClient(addr, grpc.WithTransportCredentials(creds), grpc.WithDisableRetry())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { conn.Close() })
	return pb.NewUserTriggerClient(conn)
}

func TestTriggerMembershipOverProductionTLSListenerTracksCurrentUserState(t *testing.T) {
	users, mock := newTestUserServer(t)
	serverFiles, imFiles, _ := triggerTestCertificates(t)
	runtime, e := newUserTriggerRuntime(userTriggerConfig{ListenOn: "127.0.0.1:0", IMDNSName: "im.go-im.internal", Files: serverFiles}, users)
	if e != nil {
		t.Fatal(e)
	}
	go runtime.server.Serve(runtime.listener)
	t.Cleanup(runtime.Stop)
	services := runtime.server.GetServiceInfo()
	if len(services) != 1 {
		t.Fatalf("unexpected services: %v", services)
	}
	if _, ok := services["user.UserTrigger"]; !ok {
		t.Fatal("missing restricted service")
	}
	client := triggerFlowClient(t, runtime.listener.Addr().String(), imFiles, "user.go-im.internal")
	for _, tc := range []struct {
		name    string
		rows    *sqlmock.Rows
		dbError error
		code    codes.Code
	}{
		{"active member", sqlmock.NewRows([]string{"status"}).AddRow(1), nil, codes.OK},
		{"missing membership", sqlmock.NewRows([]string{"status"}), nil, codes.PermissionDenied},
		{"leaving membership excluded by active predicate", sqlmock.NewRows([]string{"status"}), nil, codes.PermissionDenied},
		{"left membership excluded by active predicate", sqlmock.NewRows([]string{"status"}), nil, codes.PermissionDenied},
		{"disabled user", sqlmock.NewRows([]string{"status"}).AddRow(0), nil, codes.PermissionDenied},
		{"restored membership", sqlmock.NewRows([]string{"status"}).AddRow(1), nil, codes.OK},
		{"database failure", nil, errors.New("sensitive db row credentials"), codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := mock.ExpectQuery(regexp.QuoteMeta(triggerTeamQuery)).WithArgs(int64(200), triggerFlowActor, model.TeamMembershipActive, 2)
			if tc.dbError != nil {
				q.WillReturnError(tc.dbError)
			} else {
				q.WillReturnRows(tc.rows)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			// Arbitrary metadata never changes the checked scope. No login JWT is required.
			ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("actor-id", "99", "team-id", "999", "service", "agent.go-im.internal"))
			r, err := client.CheckTriggerTeamMember(ctx, &pb.CheckTriggerTeamMemberRequest{ActorId: triggerFlowActor, TeamId: 200})
			if status.Code(err) != tc.code {
				t.Fatalf("code %v wanted %v: %v", status.Code(err), tc.code, err)
			}
			if tc.code == codes.OK {
				if r == nil || r.GetActorId() != triggerFlowActor || r.GetTeamId() != 200 {
					t.Fatalf("changed checked scope %+v", r)
				}
				if r.ProtoReflect().Descriptor().Fields().Len() != 2 {
					t.Fatal("background check exposes more than the checked actor and team")
				}
			} else if r != nil {
				t.Fatalf("failed check returned data %+v", r)
			}
			if err != nil && strings.Contains(err.Error(), "sensitive") {
				t.Fatal("database detail leaked")
			}
		})
	}
}

func TestTriggerTLSListenerRejectsOtherServiceWrongServerAndPlaintext(t *testing.T) {
	users, _ := newTestUserServer(t)
	serverFiles, imFiles, agentFiles := triggerTestCertificates(t)
	runtime, e := newUserTriggerRuntime(userTriggerConfig{ListenOn: "127.0.0.1:0", IMDNSName: "im.go-im.internal", Files: serverFiles}, users)
	if e != nil {
		t.Fatal(e)
	}
	go runtime.server.Serve(runtime.listener)
	t.Cleanup(runtime.Stop)
	addr := runtime.listener.Addr().String()
	for _, tc := range []struct {
		name       string
		files      rpcauth.CertificateFiles
		serverName string
	}{
		{"same CA Agent cannot impersonate IM", agentFiles, "user.go-im.internal"},
		{"wrong server name", imFiles, "other.go-im.internal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := triggerFlowClient(t, addr, tc.files, tc.serverName)
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			if r, err := client.CheckTriggerTeamMember(ctx, &pb.CheckTriggerTeamMemberRequest{ActorId: triggerFlowActor, TeamId: 200}); err == nil || r != nil {
				t.Fatal("untrusted TLS peer succeeded")
			}
		})
	}
	conn, e := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDisableRetry())
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if r, err := pb.NewUserTriggerClient(conn).CheckTriggerTeamMember(ctx, &pb.CheckTriggerTeamMemberRequest{ActorId: triggerFlowActor, TeamId: 200}); err == nil || r != nil {
		t.Fatal("plaintext succeeded")
	}
}

func TestOrdinaryUserPortHasNoTriggerMethodAndPlainRegistrationFailsClosed(t *testing.T) {
	users, _ := newTestUserServer(t)
	for _, misregistered := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary registration", true: "accidental plain trigger registration"}[misregistered], func(t *testing.T) {
			listener, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			server := grpc.NewServer()
			if misregistered {
				pb.RegisterUserTriggerServer(server, &triggerTeamServer{db: users.db, imDNSName: "im.go-im.internal"})
			} else {
				pb.RegisterUserServer(server, users)
				if _, ok := server.GetServiceInfo()["user.UserTrigger"]; ok {
					t.Fatal("trigger leaked into ordinary port")
				}
			}
			go server.Serve(listener)
			defer server.Stop()
			conn, e := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
			if e != nil {
				t.Fatal(e)
			}
			defer conn.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", metadata.ValueFromIncomingContext(teamContext(t), "authorization")[0], "service", "im.go-im.internal"))
			r, err := pb.NewUserTriggerClient(conn).CheckTriggerTeamMember(ctx, &pb.CheckTriggerTeamMemberRequest{ActorId: triggerFlowActor, TeamId: 200})
			want := codes.Unimplemented
			if misregistered {
				want = codes.Unauthenticated
			}
			if r != nil || status.Code(err) != want {
				t.Fatalf("unguarded port response: %+v %v", r, err)
			}
		})
	}
}
