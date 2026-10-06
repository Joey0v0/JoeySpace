package main

import (
	"context"
	"net"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// This exercises both real TCP transports and the production IM handler. User
// responses and SQL are controlled substitutes; this is not a MySQL lock test.
type groupGenerationWireUser struct {
	userpb.UnimplementedUserServer
	token      string
	generation int64
}

func (s *groupGenerationWireUser) authorize(ctx context.Context, teamID int64) error {
	md, _ := metadata.FromIncomingContext(ctx)
	header := md.Get("authorization")
	if teamID != 200 || len(header) != 1 || header[0] != s.token {
		return status.Error(codes.PermissionDenied, "incorrect forwarded authorization")
	}
	return nil
}

func (s *groupGenerationWireUser) CheckTeamMember(ctx context.Context, req *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
	if err := s.authorize(ctx, req.GetTeamId()); err != nil {
		return nil, err
	}
	return &userpb.CheckTeamMemberResponse{UserId: 42, Generation: s.generation}, nil
}

func (s *groupGenerationWireUser) AuthorizeTeamGroupCreation(ctx context.Context, req *userpb.AuthorizeTeamGroupCreationRequest) (*userpb.AuthorizeTeamGroupCreationResponse, error) {
	if err := s.authorize(ctx, req.GetTeamId()); err != nil {
		return nil, err
	}
	return &userpb.AuthorizeTeamGroupCreationResponse{UserId: 42, Generation: s.generation}, nil
}

func groupGenerationWireConnection(t *testing.T, register func(*grpc.Server)) *grpc.ClientConn {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	register(server)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestTeamGroupGenerationAcrossUserAndIMTCP(t *testing.T) {
	const closed int64 = 9007199254740993
	for _, operation := range []string{"join", "create"} {
		for _, newer := range []bool{false, true} {
			name := operation + "/closed"
			generation := closed
			if newer {
				name = operation + "/newer"
				generation++
			}
			t.Run(name, func(t *testing.T) {
				s, mock := teamGroupServer(t)
				token := "Bearer " + validIMToken(t)
				userConn := groupGenerationWireConnection(t, func(server *grpc.Server) {
					userpb.RegisterUserServer(server, &groupGenerationWireUser{token: token, generation: generation})
				})
				s.teamClient = userpb.NewUserClient(userConn)
				if operation == "join" {
					mock.ExpectQuery(regexp.QuoteMeta("SELECT `id` FROM `groups` WHERE id = ? AND team_id = ? LIMIT ?")).
						WithArgs(int64(300), int64(200), 1).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(300)))
				}
				mock.ExpectBegin()
				fenceTestLock(mock, closed)
				if !newer {
					mock.ExpectRollback()
				} else {
					if operation == "create" {
						mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `groups`")).
							WithArgs("Planning", int64(42), int64(200), "request-123", imPositiveID{}).WillReturnResult(sqlmock.NewResult(0, 1))
						mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `group_members`")).
							WithArgs(imPositiveID{}, int64(42), int8(2)).WillReturnResult(sqlmock.NewResult(1, 1))
					} else {
						mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `group_members`")).
							WithArgs(int64(300), int64(42), int8(0)).WillReturnResult(sqlmock.NewResult(1, 1))
					}
					mock.ExpectCommit()
				}
				imConn := groupGenerationWireConnection(t, func(server *grpc.Server) { pb.RegisterIMServer(server, s) })
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", token, "idempotency-key", "request-123"))
				client := pb.NewIMClient(imConn)
				var err error
				if operation == "join" {
					response, callErr := client.JoinTeamGroup(ctx, &pb.JoinTeamGroupRequest{TeamId: 200, GroupId: 300})
					err = callErr
					if newer && response == nil {
						t.Error("successful join returned no response")
					}
				} else {
					response, callErr := client.CreateTeamGroup(ctx, &pb.CreateTeamGroupRequest{TeamId: 200, Name: "Planning"})
					err = callErr
					if newer && response.GetGroupId() <= 0 {
						t.Error("successful creation returned no group ID")
					}
				}
				want := codes.PermissionDenied
				if newer {
					want = codes.OK
				}
				if status.Code(err) != want {
					t.Fatalf("generation %d: %v, want %v", generation, err, want)
				}
			})
		}
	}
}
