package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestTriggerMemberLookupOverProductionTLSListenerKeepsScopeAndCurrentQualification(t *testing.T) {
	users, mock := newTestUserServer(t)
	files, imFiles, _ := triggerTestCertificates(t)
	runtime, err := newUserTriggerRuntime(userTriggerConfig{ListenOn: "127.0.0.1:0", IMDNSName: "im.go-im.internal", Files: files}, users)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = runtime.server.Serve(runtime.listener) }()
	t.Cleanup(runtime.Stop)
	services := runtime.server.GetServiceInfo()
	if len(services) != 1 || len(services["user.UserTrigger"].Methods) != 2 {
		t.Fatalf("unexpected services: %#v", services)
	}
	client := triggerFlowClient(t, runtime.listener.Addr().String(), imFiles, "user.go-im.internal")
	for _, scenario := range []string{"unique", "ambiguous", "empty", "inactive candidates excluded", "truncated", "revoked during lookup", "actor leaving before lookup", "actor left before lookup", "actor leaving during lookup", "actor left during lookup"} {
		t.Run(scenario, func(t *testing.T) {
			req := &pb.ResolveTriggerTeamMemberRequest{ActorId: triggerFlowActor, TeamId: 200, Name: "张三"}
			beforeDenied := strings.HasSuffix(scenario, "before lookup")
			initialMembership := sqlmock.NewRows([]string{"status", "generation"})
			if !beforeDenied {
				initialMembership.AddRow(1, 1)
			}
			mock.ExpectQuery(regexp.QuoteMeta(triggerTeamQuery)).WithArgs(req.TeamId, req.ActorId, model.TeamMembershipActive, 2).WillReturnRows(initialMembership)
			if beforeDenied {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				result, err := client.ResolveTriggerTeamMember(ctx, req)
				if result != nil || status.Code(err) != codes.PermissionDenied {
					t.Fatalf("non-active actor reached lookup: %#v %v", result, err)
				}
				return
			}
			rows := sqlmock.NewRows([]string{"id", "username", "nickname"})
			switch scenario {
			case "unique":
				rows.AddRow(77, "zhangsan", "张三")
			case "ambiguous":
				rows.AddRow(77, "张三", "A").AddRow(78, "B", "张三")
			case "truncated":
				for n := 0; n < 21; n++ {
					rows.AddRow(100+n, fmt.Sprintf("member%d", n), "张三")
				}
			}
			mock.ExpectQuery(regexp.QuoteMeta(resolveTriggerMemberQuery)).WithArgs(req.TeamId, model.TeamMembershipActive, req.Name, req.Name, req.TeamId, req.ActorId, model.TeamMembershipActive).WillReturnRows(rows).RowsWillBeClosed()
			finalMembership := sqlmock.NewRows([]string{"status", "generation"})
			afterDenied := strings.HasSuffix(scenario, "during lookup")
			if !afterDenied {
				finalMembership.AddRow(1, 1)
			}
			mock.ExpectQuery(regexp.QuoteMeta(triggerTeamQuery)).WithArgs(req.TeamId, req.ActorId, model.TeamMembershipActive, 2).WillReturnRows(finalMembership)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer fake", "actor-id", "99", "team-id", "999"))
			result, err := client.ResolveTriggerTeamMember(ctx, req)
			if afterDenied {
				if result != nil || status.Code(err) != codes.PermissionDenied {
					t.Fatalf("revoked caller became no-match: %#v %v", result, err)
				}
				return
			}
			if err != nil || result.GetActorId() != req.ActorId || result.GetTeamId() != req.TeamId || result.GetName() != req.Name {
				t.Fatalf("scope changed: %#v %v", result, err)
			}
			want := map[string]int{"unique": 1, "ambiguous": 2, "empty": 0, "inactive candidates excluded": 0, "truncated": 20}[scenario]
			if len(result.GetCandidates()) != want || result.GetTruncated() != (scenario == "truncated") {
				t.Fatalf("candidate shape wrong: %#v", result)
			}
			if result.ProtoReflect().Descriptor().Fields().Len() != 5 || (&pb.TriggerTeamMemberCandidate{}).ProtoReflect().Descriptor().Fields().Len() != 3 {
				t.Fatal("lookup exposes unplanned profile or credential fields")
			}
		})
	}
}

func TestTriggerMemberLookupOverTLSRejectsWrongServiceBeforeDatabase(t *testing.T) {
	users, _ := newTestUserServer(t)
	files, _, otherFiles := triggerTestCertificates(t)
	runtime, err := newUserTriggerRuntime(userTriggerConfig{ListenOn: "127.0.0.1:0", IMDNSName: "im.go-im.internal", Files: files}, users)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = runtime.server.Serve(runtime.listener) }()
	t.Cleanup(runtime.Stop)
	client := triggerFlowClient(t, runtime.listener.Addr().String(), otherFiles, "user.go-im.internal")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	result, err := client.ResolveTriggerTeamMember(ctx, &pb.ResolveTriggerTeamMemberRequest{ActorId: triggerFlowActor, TeamId: 200, Name: "张三"})
	if result != nil || err == nil {
		t.Fatalf("wrong service certificate accepted: %#v %v", result, err)
	}
}
