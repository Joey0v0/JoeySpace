package main

import (
	"context"
	"errors"
	"net"
	"regexp"
	"testing"
	"time"

	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestAuthorizeTeamGroupCreationOwnerOverRPC(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	const generation int64 = 9007199254740993
	expectTeamMembership(mock, true, 2, generation)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	pb.RegisterUserServer(server, s)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", metadata.ValueFromIncomingContext(teamContext(t), "authorization")[0]))
	result, err := pb.NewUserClient(conn).AuthorizeTeamGroupCreation(ctx, &pb.AuthorizeTeamGroupCreationRequest{TeamId: 100})
	if err != nil || result == nil || result.GetUserId() != 42 || result.GetGeneration() != generation {
		t.Fatalf("authorize team group creation RPC: %v, %v", result, err)
	}
}

func TestAuthorizeTeamGroupCreationRejectsInvalidInputAndToken(t *testing.T) {
	s, _ := newTestUserServer(t)
	for _, req := range []*pb.AuthorizeTeamGroupCreationRequest{nil, {TeamId: 0}, {TeamId: -1}} {
		result, err := s.AuthorizeTeamGroupCreation(teamContext(t), req)
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid request: %v, %v", result, err)
		}
	}
	result, err := s.AuthorizeTeamGroupCreation(context.Background(), &pb.AuthorizeTeamGroupCreationRequest{TeamId: 100})
	if result != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token: %v, %v", result, err)
	}
}

func TestAuthorizeTeamGroupCreationRequiresOwner(t *testing.T) {
	for _, role := range []int8{0, 1} {
		s, mock := newTestUserServer(t)
		expectTeamCreator(mock)
		expectTeamMembership(mock, true, role)
		result, err := s.AuthorizeTeamGroupCreation(teamContext(t), &pb.AuthorizeTeamGroupCreationRequest{TeamId: 100})
		if result != nil || status.Code(err) != codes.PermissionDenied {
			t.Fatalf("operator role %d: %v, %v", role, result, err)
		}
	}
	t.Run("non-member", func(t *testing.T) {
		s, mock := newTestUserServer(t)
		expectTeamCreator(mock)
		expectTeamMembership(mock, false, 0)
		result, err := s.AuthorizeTeamGroupCreation(teamContext(t), &pb.AuthorizeTeamGroupCreationRequest{TeamId: 100})
		if result != nil || status.Code(err) != codes.PermissionDenied {
			t.Fatalf("non-member: %v, %v", result, err)
		}
	})
}

func TestAuthorizeTeamGroupCreationDatabaseFailure(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	mock.ExpectQuery("^"+regexp.QuoteMeta(activeTeamMembershipQuery)+"$").WithArgs(int64(100), int64(42), int64(0), 1).
		WillReturnError(errors.New("database unavailable"))
	result, err := s.AuthorizeTeamGroupCreation(teamContext(t), &pb.AuthorizeTeamGroupCreationRequest{TeamId: 100})
	if result != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("database failure: %v, %v", result, err)
	}
}
