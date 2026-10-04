package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yjydist/go-im/internal/rpcauth"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
)

const triggerClientSourceID int64 = 9007199254741011

type triggerContextRPCFunc func(context.Context, *impb.ReadTaskTriggerContextRequest) (*impb.ReadTaskTriggerContextResponse, error)

func (f triggerContextRPCFunc) ReadTaskTriggerContext(ctx context.Context, req *impb.ReadTaskTriggerContextRequest, _ ...grpc.CallOption) (*impb.ReadTaskTriggerContextResponse, error) {
	return f(ctx, req)
}

func (f triggerContextRPCFunc) ResolveTaskTriggerMember(context.Context, *impb.ResolveTaskTriggerMemberRequest, ...grpc.CallOption) (*impb.ResolveTaskTriggerMemberResponse, error) {
	return nil, status.Error(codes.Unimplemented, "lookup is not used by source tests")
}

func triggerContextEnvironment(address, server string, files rpcauth.CertificateFiles) func(string) string {
	values := map[string]string{"AGENT_IM_TRIGGER_ADDR": address, "AGENT_IM_TRIGGER_SERVER_NAME": server,
		"AGENT_IM_TRIGGER_TLS_CERT_FILE": files.CertFile, "AGENT_IM_TRIGGER_TLS_KEY_FILE": files.KeyFile, "AGENT_IM_TRIGGER_TLS_CA_FILE": files.CAFile}
	return func(name string) string { return values[name] }
}

func validTriggerClientResponse(id int64) *impb.ReadTaskTriggerContextResponse {
	return &impb.ReadTaskTriggerContextResponse{MessageId: id, MsgId: "saved-source", ActorId: 9007199254740993,
		TeamId: 9007199254740995, GroupId: 9007199254740997, Instruction: "提取两项任务", ReferenceTimeUnixMs: 1791000000000,
		RequestKey: fmt.Sprintf("agent-trigger:tasks:%d", id), Messages: []*impb.TeamGroupMessage{
			{Id: id, MsgId: "saved-source", FromId: 9007199254740993, ContentType: 1, Content: "@AI 整理任务 提取两项任务", CreatedAtUnixMs: 1791000000000, SenderType: 1},
			{Id: id - 1, MsgId: "prior-bot", FromId: 42, ContentType: 4, Content: "Created a task", CreatedAtUnixMs: 1791000001000, SenderType: 2, InitiatorId: 9007199254740993},
		}}
}

func TestTriggerContextClientConfigurationIsOptionalAndNeverFallsBack(t *testing.T) {
	client, err := NewTriggerContextClient(func(string) string { return "" })
	if client != nil || err != nil || client.Close() != nil {
		t.Fatalf("disabled: %v %v", client, err)
	}
	if _, err := NewTriggerContextClient(nil); err == nil {
		t.Fatal("nil environment reader accepted")
	}
	for _, tc := range []struct {
		name, address, server string
		files                 rpcauth.CertificateFiles
	}{
		{"partial", "im:9006", "", rpcauth.CertificateFiles{}},
		{"URI", "http://im:9006", "im.internal", rpcauth.CertificateFiles{CertFile: "cert", KeyFile: "key", CAFile: "ca"}},
		{"resolver URI", "dns:///im:9006", "im.internal", rpcauth.CertificateFiles{CertFile: "cert", KeyFile: "key", CAFile: "ca"}},
		{"empty host", ":9006", "im.internal", rpcauth.CertificateFiles{CertFile: "cert", KeyFile: "key", CAFile: "ca"}},
		{"unicode space", "im\u2003rpc:9006", "im.internal", rpcauth.CertificateFiles{CertFile: "cert", KeyFile: "key", CAFile: "ca"}},
		{"space", "im:9006 ", "im.internal", rpcauth.CertificateFiles{CertFile: "cert", KeyFile: "key", CAFile: "ca"}},
		{"zero port", "im:0", "im.internal", rpcauth.CertificateFiles{CertFile: "cert", KeyFile: "key", CAFile: "ca"}},
		{"high port", "im:65536", "im.internal", rpcauth.CertificateFiles{CertFile: "cert", KeyFile: "key", CAFile: "ca"}},
		{"signed port", "im:+9006", "im.internal", rpcauth.CertificateFiles{CertFile: "cert", KeyFile: "key", CAFile: "ca"}},
		{"invalid bracket", "[im]:9006", "im.internal", rpcauth.CertificateFiles{CertFile: "cert", KeyFile: "key", CAFile: "ca"}},
		{"wildcard", "im:9006", "*.internal", rpcauth.CertificateFiles{CertFile: "cert", KeyFile: "key", CAFile: "ca"}},
		{"server whitespace", "im:9006", "im\t.internal", rpcauth.CertificateFiles{CertFile: "cert", KeyFile: "key", CAFile: "ca"}},
		{"server URI", "im:9006", "https://im.internal", rpcauth.CertificateFiles{CertFile: "cert", KeyFile: "key", CAFile: "ca"}},
		{"missing key", "im:9006", "im.internal", rpcauth.CertificateFiles{CertFile: "cert", CAFile: "ca"}},
		{"missing certificate", "im:9006", "im.internal", rpcauth.CertificateFiles{KeyFile: "key", CAFile: "ca"}},
		{"missing CA", "im:9006", "im.internal", rpcauth.CertificateFiles{CertFile: "cert", KeyFile: "key"}},
		{"bad private files", "im:9006", "im.internal", rpcauth.CertificateFiles{CertFile: "private-missing-cert", KeyFile: "private-missing-key", CAFile: "private-missing-ca"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewTriggerContextClient(triggerContextEnvironment(tc.address, tc.server, tc.files))
			if client != nil || err == nil || strings.Contains(err.Error(), "private-missing") {
				t.Fatalf("failed open or leaked a path: %v %v", client, err)
			}
		})
	}
}

func TestTriggerContextReadOnlySendsSourceIDAndStripsAllCallerMetadata(t *testing.T) {
	for _, id := range []int64{triggerClientSourceID, math.MaxInt64} {
		client := &TriggerContextClient{rpc: triggerContextRPCFunc(func(ctx context.Context, req *impb.ReadTaskTriggerContextRequest) (*impb.ReadTaskTriggerContextResponse, error) {
			md, ok := metadata.FromOutgoingContext(ctx)
			deadline, bounded := ctx.Deadline()
			if !ok || len(md) != 0 || !bounded || time.Until(deadline) > 5*time.Second || req.MessageId != id {
				t.Fatalf("request %v metadata %v", req, md)
			}
			return validTriggerClientResponse(req.MessageId), nil
		})}
		ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer secret-token", "actor-id", "1", "team-id", "2", "service", "user", "x-extra", "secret"))
		result, err := client.Read(ctx, id)
		if err != nil || result.MessageId != id || result.ActorId != 9007199254740993 || result.ReferenceTimeUnixMs != 1791000000000 || result.RequestKey != fmt.Sprintf("agent-trigger:tasks:%d", id) {
			t.Fatalf("%v %v", result, err)
		}
	}
}

func TestTriggerContextReadRejectsMalformedSuccessWithoutReturningAnyBody(t *testing.T) {
	for name, change := range map[string]func(*impb.ReadTaskTriggerContextResponse){
		"wrong source":             func(r *impb.ReadTaskTriggerContextResponse) { r.MessageId++ },
		"bad source msg ID":        func(r *impb.ReadTaskTriggerContextResponse) { r.MsgId = "" },
		"zero actor":               func(r *impb.ReadTaskTriggerContextResponse) { r.ActorId = 0 },
		"zero team":                func(r *impb.ReadTaskTriggerContextResponse) { r.TeamId = 0 },
		"negative group":           func(r *impb.ReadTaskTriggerContextResponse) { r.GroupId = -1 },
		"noncanonical instruction": func(r *impb.ReadTaskTriggerContextResponse) { r.Instruction = " " + r.Instruction },
		"invalid instruction UTF8": func(r *impb.ReadTaskTriggerContextResponse) { r.Instruction = "\xff" },
		"oversize instruction":     func(r *impb.ReadTaskTriggerContextResponse) { r.Instruction = strings.Repeat("中", 2001) },
		"zero reference":           func(r *impb.ReadTaskTriggerContextResponse) { r.ReferenceTimeUnixMs = 0 },
		"wrong reference":          func(r *impb.ReadTaskTriggerContextResponse) { r.ReferenceTimeUnixMs++ },
		"wrong key":                func(r *impb.ReadTaskTriggerContextResponse) { r.RequestKey += ":1" },
		"empty history":            func(r *impb.ReadTaskTriggerContextResponse) { r.Messages = nil },
		"oversize history": func(r *impb.ReadTaskTriggerContextResponse) {
			r.Messages = append(r.Messages, make([]*impb.TeamGroupMessage, 19)...)
		},
		"nil source":           func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[0] = nil },
		"nil history row":      func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[1] = nil },
		"missing first source": func(r *impb.ReadTaskTriggerContextResponse) { r.Messages = r.Messages[1:] },
		"future ID":            func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[1].Id = r.MessageId + 1 },
		"duplicate ID":         func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[1].Id = r.MessageId },
		"zero history ID":      func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[1].Id = 0 },
		"duplicate msg ID":     func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[1].MsgId = r.MsgId },
		"empty msg ID":         func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[1].MsgId = "" },
		"oversize msg ID":      func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[1].MsgId = strings.Repeat("x", 65) },
		"bad msg UTF8":         func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[1].MsgId = "\xff" },
		"msg ID newline":       func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[1].MsgId = "msg\n" },
		"source wrong msg ID":  func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[0].MsgId = "changed-source" },
		"source wrong actor":   func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[0].FromId++ },
		"source nontext":       func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[0].ContentType = 2 },
		"source bot": func(r *impb.ReadTaskTriggerContextResponse) {
			r.Messages[0].SenderType, r.Messages[0].InitiatorId = 2, 9
		},
		"source fake initiator":      func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[0].InitiatorId = 9 },
		"source wrong instruction":   func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[0].Content += " changed" },
		"source plain text":          func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[0].Content = r.Instruction },
		"source embedded command":    func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[0].Content = "quote " + r.Messages[0].Content },
		"source wrong timestamp":     func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[0].CreatedAtUnixMs++ },
		"zero from":                  func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[1].FromId = 0 },
		"zero time":                  func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[1].CreatedAtUnixMs = 0 },
		"legacy unnormalized sender": func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[1].SenderType = 0 },
		"unknown sender":             func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[1].SenderType = 3 },
		"bot has no initiator":       func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[1].InitiatorId = 0 },
		"negative initiator":         func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[1].InitiatorId = -1 },
		"user has initiator":         func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[1].SenderType = 1 },
		"bad content type":           func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[1].ContentType = 0 },
		"unknown content type":       func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[1].ContentType = 5 },
		"bad content UTF8":           func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[1].Content = "\xff" },
		"content too large":          func(r *impb.ReadTaskTriggerContextResponse) { r.Messages[1].Content = strings.Repeat("x", 65536) },
		"oversize protobuf": func(r *impb.ReadTaskTriggerContextResponse) {
			r.ProtoReflect().SetUnknown(protowire.AppendBytes(protowire.AppendTag(nil, 10, protowire.BytesType), bytes.Repeat([]byte("x"), maxTriggerContextBytes)))
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := validTriggerClientResponse(triggerClientSourceID)
			change(result)
			client := &TriggerContextClient{rpc: triggerContextRPCFunc(func(context.Context, *impb.ReadTaskTriggerContextRequest) (*impb.ReadTaskTriggerContextResponse, error) {
				return result, nil
			})}
			data, err := client.Read(context.Background(), triggerClientSourceID)
			if data != nil || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "提取") {
				t.Fatalf("accepted or leaked bad response: %v %v", data, err)
			}
		})
	}
	client := &TriggerContextClient{rpc: triggerContextRPCFunc(func(context.Context, *impb.ReadTaskTriggerContextRequest) (*impb.ReadTaskTriggerContextResponse, error) {
		return nil, nil
	})}
	if data, err := client.Read(context.Background(), triggerClientSourceID); data != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("nil success: %v %v", data, err)
	}
}

func TestTriggerContextReadAcceptsTwentyRowsTextByteBoundaryAndAllMessageTypes(t *testing.T) {
	response := validTriggerClientResponse(triggerClientSourceID)
	response.Messages = response.Messages[:1]
	for i := int64(1); i < 20; i++ {
		row := &impb.TeamGroupMessage{Id: triggerClientSourceID - i, MsgId: fmt.Sprintf("history-%d", i), FromId: 9, ContentType: int32(i%4 + 1),
			Content: strings.Repeat("x", 65535), CreatedAtUnixMs: response.ReferenceTimeUnixMs + i, SenderType: 1}
		response.Messages = append(response.Messages, row)
	}
	client := &TriggerContextClient{rpc: triggerContextRPCFunc(func(context.Context, *impb.ReadTaskTriggerContextRequest) (*impb.ReadTaskTriggerContextResponse, error) {
		return response, nil
	})}
	if data, err := client.Read(context.Background(), triggerClientSourceID); err != nil || len(data.Messages) != 20 {
		t.Fatalf("valid boundary rejected: %v", err)
	}
}

func TestTriggerContextReadPreservesExpectedCodesAndMasksInternalDetails(t *testing.T) {
	for _, code := range []codes.Code{codes.InvalidArgument, codes.NotFound, codes.PermissionDenied, codes.Unauthenticated, codes.Canceled, codes.DeadlineExceeded,
		codes.Unavailable, codes.Internal, codes.Unimplemented, codes.ResourceExhausted, codes.FailedPrecondition} {
		client := &TriggerContextClient{rpc: triggerContextRPCFunc(func(context.Context, *impb.ReadTaskTriggerContextRequest) (*impb.ReadTaskTriggerContextResponse, error) {
			return nil, status.Error(code, "private SQL/TLS/token/body details")
		})}
		data, err := client.Read(context.Background(), triggerClientSourceID)
		want := codes.Unavailable
		if code == codes.InvalidArgument || code == codes.NotFound || code == codes.PermissionDenied || code == codes.Unauthenticated || code == codes.Canceled || code == codes.DeadlineExceeded {
			want = code
		}
		if data != nil || status.Code(err) != want || strings.Contains(err.Error(), "private") {
			t.Fatalf("%s: %v", code, err)
		}
	}
}

func TestTriggerContextReadLocalGuardsCancelAndFiveSecondCap(t *testing.T) {
	client := &TriggerContextClient{rpc: triggerContextRPCFunc(func(context.Context, *impb.ReadTaskTriggerContextRequest) (*impb.ReadTaskTriggerContextResponse, error) {
		t.Fatal("invalid local request reached RPC")
		return nil, nil
	})}
	for _, id := range []int64{0, -1} {
		if _, err := client.Read(context.Background(), id); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
	if _, err := client.Read(nil, 1); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Read(ctx, 1); status.Code(err) != codes.Canceled {
		t.Fatal(err)
	}
	for _, client := range []*TriggerContextClient{nil, {}} {
		if _, err := client.Read(context.Background(), 1); status.Code(err) != codes.Unavailable {
			t.Fatal(err)
		}
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
	}
	bounded := &TriggerContextClient{rpc: triggerContextRPCFunc(func(ctx context.Context, req *impb.ReadTaskTriggerContextRequest) (*impb.ReadTaskTriggerContextResponse, error) {
		<-ctx.Done()
		return validTriggerClientResponse(req.MessageId), nil
	})}
	started := time.Now()
	if data, err := bounded.Read(context.Background(), triggerClientSourceID); data != nil || status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("late success: %v %v", data, err)
	}
	if elapsed := time.Since(started); elapsed < 4900*time.Millisecond || elapsed > 6*time.Second {
		t.Fatalf("five second cap: %v", elapsed)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started = time.Now()
	if _, err := bounded.Read(ctx, triggerClientSourceID); status.Code(err) != codes.DeadlineExceeded {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("caller deadline enlarged: %v", elapsed)
	}
}

type triggerContextTLSStub struct {
	impb.UnimplementedIMTriggerServer
	read triggerContextRPCFunc
}

func (s *triggerContextTLSStub) ReadTaskTriggerContext(ctx context.Context, req *impb.ReadTaskTriggerContextRequest) (*impb.ReadTaskTriggerContextResponse, error) {
	return s.read(ctx, req)
}

func startTriggerContextTLSStub(t *testing.T, files rpcauth.CertificateFiles, tlsEnabled bool, read triggerContextRPCFunc) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var options []grpc.ServerOption
	if tlsEnabled {
		creds, err := rpcauth.NewServiceServerCredentials(files, "agent.go-im.internal")
		if err != nil {
			_ = listener.Close()
			t.Fatal(err)
		}
		options = append(options, grpc.Creds(creds))
	}
	server := grpc.NewServer(options...)
	impb.RegisterIMTriggerServer(server, &triggerContextTLSStub{read: read})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	return listener.Addr().String()
}

func TestTriggerContextClientActualMTLSOnlyReadsSavedSourceAndCurrentDenialIsNotRetried(t *testing.T) {
	files := botClientCertificates(t)
	var calls atomic.Int32
	address := startTriggerContextTLSStub(t, files["im.go-im.internal"], true, func(ctx context.Context, req *impb.ReadTaskTriggerContextRequest) (*impb.ReadTaskTriggerContextResponse, error) {
		if err := rpcauth.RequireServiceIdentity(ctx, "agent.go-im.internal"); err != nil {
			return nil, err
		}
		md, _ := metadata.FromIncomingContext(ctx)
		for _, key := range []string{"authorization", "actor-id", "team-id", "service", "x-extra"} {
			if len(md.Get(key)) != 0 {
				t.Errorf("caller metadata forwarded: %s", key)
			}
		}
		if req.MessageId != triggerClientSourceID {
			t.Error("source ID changed")
		}
		switch calls.Add(1) {
		case 1:
			return validTriggerClientResponse(req.MessageId), nil
		case 2:
			return nil, status.Error(codes.PermissionDenied, "private revoked scope")
		default:
			return nil, status.Error(codes.Unavailable, "private unavailable storage")
		}
	})
	client, err := NewTriggerContextClient(triggerContextEnvironment(address, "im.go-im.internal", files["agent.go-im.internal"]))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer secret-token", "actor-id", "1", "team-id", "2", "service", "user", "x-extra", "secret"))
	data, err := client.Read(ctx, triggerClientSourceID)
	if err != nil || data.ReferenceTimeUnixMs != 1791000000000 || data.RequestKey != "agent-trigger:tasks:9007199254741011" {
		t.Fatalf("%v %v", data, err)
	}
	if data, err := client.Read(ctx, triggerClientSourceID); data != nil || status.Code(err) != codes.PermissionDenied || strings.Contains(err.Error(), "private") {
		t.Fatalf("%v %v", data, err)
	}
	if data, err := client.Read(ctx, triggerClientSourceID); data != nil || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private") {
		t.Fatalf("%v %v", data, err)
	}
	if calls.Load() != 3 {
		t.Fatalf("unexpected automatic RPC retry: %d", calls.Load())
	}
	if client.Close() != nil || client.Close() != nil {
		t.Fatal("repeated close failed")
	}
	if _, err := client.Read(context.Background(), triggerClientSourceID); status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Read(canceled, triggerClientSourceID); status.Code(err) != codes.Canceled {
		t.Fatal(err)
	}
}

func TestTriggerContextClientActualTLSRejectsOtherRoleWrongServerCAAndPlaintext(t *testing.T) {
	files, foreign := botClientCertificates(t), botClientCertificates(t)
	var calls atomic.Int32
	read := triggerContextRPCFunc(func(context.Context, *impb.ReadTaskTriggerContextRequest) (*impb.ReadTaskTriggerContextResponse, error) {
		calls.Add(1)
		return nil, errors.New("must not enter handler")
	})
	secure := startTriggerContextTLSStub(t, files["im.go-im.internal"], true, read)
	plain := startTriggerContextTLSStub(t, files["im.go-im.internal"], false, read)
	for _, mode := range []string{"other service", "wrong server name", "wrong CA", "plaintext"} {
		t.Run(mode, func(t *testing.T) {
			address, name, credentials := secure, "im.go-im.internal", files["agent.go-im.internal"]
			switch mode {
			case "other service":
				credentials = files["other.go-im.internal"]
			case "wrong server name":
				name = "other.go-im.internal"
			case "wrong CA":
				credentials.CAFile = foreign["agent.go-im.internal"].CAFile
			case "plaintext":
				address = plain
			}
			client, err := NewTriggerContextClient(triggerContextEnvironment(address, name, credentials))
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			data, err := client.Read(ctx, triggerClientSourceID)
			if data != nil || (status.Code(err) != codes.Unavailable && status.Code(err) != codes.DeadlineExceeded) || strings.Contains(err.Error(), "certificate") {
				t.Fatalf("%v %v", data, err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("failed TLS connection reached handler: %d", calls.Load())
	}
}

func TestTriggerContextClientActualTLSRejectsMalformedAndOversizeResponseAndRespectsCallerDeadline(t *testing.T) {
	files := botClientCertificates(t)
	var calls atomic.Int32
	address := startTriggerContextTLSStub(t, files["im.go-im.internal"], true, func(ctx context.Context, req *impb.ReadTaskTriggerContextRequest) (*impb.ReadTaskTriggerContextResponse, error) {
		call := calls.Add(1)
		response := validTriggerClientResponse(req.MessageId)
		if call == 1 {
			response.Messages[0].CreatedAtUnixMs++
			return response, nil
		}
		if call == 2 {
			response.ProtoReflect().SetUnknown(protowire.AppendBytes(protowire.AppendTag(nil, 10, protowire.BytesType), bytes.Repeat([]byte("x"), maxTriggerContextBytes+1)))
			return response, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	client, err := NewTriggerContextClient(triggerContextEnvironment(address, "im.go-im.internal", files["agent.go-im.internal"]))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for i := 0; i < 2; i++ {
		if data, err := client.Read(context.Background(), triggerClientSourceID); data != nil || status.Code(err) != codes.Unavailable {
			t.Fatalf("invalid wire response %d: %v %v", i, data, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := client.Read(ctx, triggerClientSourceID); status.Code(err) != codes.DeadlineExceeded {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("unexpected retries: %d", calls.Load())
	}
}

func TestTriggerContextReadConnectionClosedDuringCallDoesNotHideCallerCancellation(t *testing.T) {
	files := botClientCertificates(t)
	for _, cancelCaller := range []bool{false, true} {
		client, err := NewTriggerContextClient(triggerContextEnvironment("127.0.0.1:9006", "im.go-im.internal", files["agent.go-im.internal"]))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		client.rpc = triggerContextRPCFunc(func(context.Context, *impb.ReadTaskTriggerContextRequest) (*impb.ReadTaskTriggerContextResponse, error) {
			if cancelCaller {
				cancel()
			}
			if err := client.Close(); err != nil {
				t.Error(err)
			}
			return nil, status.Error(codes.Canceled, "private closing connection")
		})
		data, err := client.Read(ctx, triggerClientSourceID)
		cancel()
		want := codes.Unavailable
		if cancelCaller {
			want = codes.Canceled
		}
		if data != nil || status.Code(err) != want || strings.Contains(err.Error(), "private") {
			t.Fatalf("cancel caller %v: %v %v", cancelCaller, data, err)
		}
	}
}
