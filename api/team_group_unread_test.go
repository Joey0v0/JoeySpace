package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/im/pb"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type groupUnreadHTTPClient struct {
	pb.IMClient
	get  func(context.Context, *pb.GetTeamGroupUnreadRequest) (*pb.GetTeamGroupUnreadResponse, error)
	mark func(context.Context, *pb.MarkTeamGroupMessagesReadRequest) (*pb.MarkTeamGroupMessagesReadResponse, error)
}

func (c groupUnreadHTTPClient) GetTeamGroupUnread(ctx context.Context, req *pb.GetTeamGroupUnreadRequest, _ ...grpc.CallOption) (*pb.GetTeamGroupUnreadResponse, error) {
	return c.get(ctx, req)
}

func (c groupUnreadHTTPClient) MarkTeamGroupMessagesRead(ctx context.Context, req *pb.MarkTeamGroupMessagesReadRequest, _ ...grpc.CallOption) (*pb.MarkTeamGroupMessagesReadResponse, error) {
	return c.mark(ctx, req)
}

func groupUnreadHTTPRequest(method, team, group, query, body string, headers ...string) *http.Request {
	r := httptest.NewRequest(method, "/api/v1/teams/200/groups/300/unread"+query, strings.NewReader(body))
	r = pathvar.WithVars(r, map[string]string{"team_id": team, "group_id": group})
	for _, header := range headers {
		r.Header.Add("Authorization", header)
	}
	return r
}

func groupUnreadHTTPHandler(method string, client pb.IMClient) http.HandlerFunc {
	if method == http.MethodGet {
		return getTeamGroupUnreadHandler(client)
	}
	return markTeamGroupMessagesReadHandler(client)
}

func assertGroupUnreadHTTPError(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus, wantCode int) {
	t.Helper()
	var response struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != wantStatus || response.Code != wantCode || len(response.Data) != 0 ||
		strings.Contains(recorder.Body.String(), "private-db") || strings.Contains(recorder.Body.String(), "original-token") {
		t.Fatalf("unsafe or wrong response: %d %s; want status=%d code=%d", recorder.Code, recorder.Body.String(), wantStatus, wantCode)
	}
}

func assertGroupUnreadRPCContext(t *testing.T, ctx context.Context) {
	t.Helper()
	md, _ := metadata.FromOutgoingContext(ctx)
	if len(md) != 1 || !slices.Equal(md.Get("authorization"), []string{"Bearer original-token"}) {
		t.Fatalf("extra or changed RPC metadata: %v", md)
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 3*time.Second {
		t.Fatalf("missing or excessive RPC deadline: %v, %v", deadline, ok)
	}
}

func TestTeamGroupUnreadHTTPForwardsScopeAndKeepsLargeValuesAsStrings(t *testing.T) {
	client := groupUnreadHTTPClient{
		get: func(ctx context.Context, req *pb.GetTeamGroupUnreadRequest) (*pb.GetTeamGroupUnreadResponse, error) {
			assertGroupUnreadRPCContext(t, ctx)
			if req.TeamId != 9007199254740993 || req.GroupId != 9223372036854775807 {
				t.Fatalf("changed scope: %v", req)
			}
			return &pb.GetTeamGroupUnreadResponse{TeamId: req.TeamId, GroupId: req.GroupId, UnreadCount: 9223372036854775807}, nil
		},
		mark: func(ctx context.Context, req *pb.MarkTeamGroupMessagesReadRequest) (*pb.MarkTeamGroupMessagesReadResponse, error) {
			assertGroupUnreadRPCContext(t, ctx)
			if req.TeamId != 9007199254740993 || req.GroupId != 9223372036854775807 || !slices.Equal(req.MessageIds, []int64{1, 9007199254740997, 9223372036854775807}) {
				t.Fatalf("changed read scope/IDs: %v", req)
			}
			// The HTTP guard checks set equality; it does not rely on RPC ordering.
			return &pb.MarkTeamGroupMessagesReadResponse{TeamId: req.TeamId, GroupId: req.GroupId, UnreadCount: 0, MessageIds: []int64{9223372036854775807, 1, 9007199254740997}}, nil
		},
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			body := ""
			if method == http.MethodPost {
				body = `{"message_ids":["9223372036854775807","9007199254740997","1","001","9007199254740997"]}`
			}
			r := groupUnreadHTTPRequest(method, "9007199254740993", "9223372036854775807", "", body, "bearer original-token")
			r.Header.Set("X-User-ID", "another-user")
			r.Header.Set("Idempotency-Key", "not-forwarded")
			r = r.WithContext(metadata.NewOutgoingContext(r.Context(), metadata.Pairs("user-id", "another-user", "authorization", "Bearer not-forwarded")))
			w := httptest.NewRecorder()
			groupUnreadHTTPHandler(method, client)(w, r)
			var response struct {
				Code int                        `json:"code"`
				Data map[string]json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			wantCount := `"9223372036854775807"`
			if method == http.MethodPost {
				wantCount = `"0"`
			}
			if w.Code != http.StatusOK || response.Code != 0 || string(response.Data["team_id"]) != `"9007199254740993"` ||
				string(response.Data["group_id"]) != `"9223372036854775807"` || string(response.Data["unread_count"]) != wantCount {
				t.Fatalf("large values lost string precision: %d %s", w.Code, w.Body.String())
			}
			if method == http.MethodGet {
				if len(response.Data) != 3 {
					t.Fatalf("GET disclosed read IDs: %s", w.Body.String())
				}
			} else if string(response.Data["message_ids"]) != `["1","9007199254740997","9223372036854775807"]` {
				t.Fatalf("incorrect normalized IDs: %s", w.Body.String())
			}
		})
	}
}

func TestTeamGroupUnreadHTTPRejectsInvalidScopeBeforeRPC(t *testing.T) {
	client := groupUnreadHTTPClient{
		get: func(context.Context, *pb.GetTeamGroupUnreadRequest) (*pb.GetTeamGroupUnreadResponse, error) {
			t.Fatal("invalid scope reached Get RPC")
			return nil, nil
		},
		mark: func(context.Context, *pb.MarkTeamGroupMessagesReadRequest) (*pb.MarkTeamGroupMessagesReadResponse, error) {
			t.Fatal("invalid scope reached Mark RPC")
			return nil, nil
		},
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, tc := range []struct {
			name, team, group, query string
			headers                  []string
			wantStatus, wantCode     int
		}{
			{"missing Bearer", "200", "300", "", nil, 401, errcode.ErrUnAuth},
			{"duplicate Bearer", "200", "300", "", []string{"Bearer a", "Bearer b"}, 401, errcode.ErrUnAuth},
			{"combined Bearer", "200", "300", "", []string{"Bearer a, Bearer b"}, 401, errcode.ErrUnAuth},
			{"wrong scheme", "200", "300", "", []string{"Basic token"}, 401, errcode.ErrUnAuth},
			{"empty Bearer", "200", "300", "", []string{"Bearer"}, 401, errcode.ErrUnAuth},
			{"zero team", "0", "300", "", []string{"Bearer original-token"}, 400, errcode.ErrBadRequest},
			{"signed team", "+200", "300", "", []string{"Bearer original-token"}, 400, errcode.ErrBadRequest},
			{"overflow team", "9223372036854775808", "300", "", []string{"Bearer original-token"}, 400, errcode.ErrBadRequest},
			{"missing group", "200", "", "", []string{"Bearer original-token"}, 400, errcode.ErrBadRequest},
			{"zero group", "200", "0", "", []string{"Bearer original-token"}, 400, errcode.ErrBadRequest},
			{"negative group", "200", "-1", "", []string{"Bearer original-token"}, 400, errcode.ErrBadRequest},
			{"group whitespace", "200", " 300", "", []string{"Bearer original-token"}, 400, errcode.ErrBadRequest},
			{"overflow group", "200", "9223372036854775808", "", []string{"Bearer original-token"}, 400, errcode.ErrBadRequest},
			{"self-reported user", "200", "300", "?user_id=400", []string{"Bearer original-token"}, 400, errcode.ErrBadRequest},
			{"unknown query", "200", "300", "?limit=20", []string{"Bearer original-token"}, 400, errcode.ErrBadRequest},
			{"empty query marker", "200", "300", "?", []string{"Bearer original-token"}, 400, errcode.ErrBadRequest},
		} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				w := httptest.NewRecorder()
				groupUnreadHTTPHandler(method, client)(w, groupUnreadHTTPRequest(method, tc.team, tc.group, tc.query, `{"message_ids":["1"]}`, tc.headers...))
				assertGroupUnreadHTTPError(t, w, tc.wantStatus, tc.wantCode)
			})
		}
	}
}

func TestTeamGroupReadHTTPRejectsInvalidBodyBeforeRPC(t *testing.T) {
	client := groupUnreadHTTPClient{mark: func(context.Context, *pb.MarkTeamGroupMessagesReadRequest) (*pb.MarkTeamGroupMessagesReadResponse, error) {
		t.Fatal("invalid body reached Mark RPC")
		return nil, nil
	}}
	for _, tc := range []struct{ name, body string }{
		{"missing", ""}, {"null body", "null"}, {"missing IDs", `{}`}, {"null list", `{"message_ids":null}`},
		{"empty list", `{"message_ids":[]}`}, {"numeric ID", `{"message_ids":[9007199254740993]}`},
		{"null ID", `{"message_ids":[null]}`}, {"zero ID", `{"message_ids":["0"]}`},
		{"negative ID", `{"message_ids":["-1"]}`}, {"signed ID", `{"message_ids":["+1"]}`},
		{"empty ID", `{"message_ids":[""]}`}, {"whitespace ID", `{"message_ids":[" 1"]}`},
		{"overflow ID", `{"message_ids":["9223372036854775808"]}`}, {"exponent ID", `{"message_ids":["1e3"]}`},
		{"self-reported user", `{"message_ids":["1"],"user_id":"400"}`},
		{"self-reported team", `{"message_ids":["1"],"team_id":"201"}`},
		{"multiple JSON values", `{"message_ids":["1"]}{"message_ids":["2"]}`},
		{"trailing malformed data", `{"message_ids":["1"]}garbage`},
		{"over raw limit despite duplicates", `{"message_ids":[` + strings.Repeat(`"1",`, 100) + `"1"]}`},
		{"oversized leading whitespace", strings.Repeat(" ", 32*1024) + `{"message_ids":["1"]}`},
		{"oversized trailing whitespace", `{"message_ids":["1"]}` + strings.Repeat(" ", 32*1024)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := groupUnreadHTTPRequest(http.MethodPost, "200", "300", "", tc.body, "Bearer original-token")
			r.ContentLength = -1 // Limits must cover streaming bodies too.
			w := httptest.NewRecorder()
			markTeamGroupMessagesReadHandler(client)(w, r)
			assertGroupUnreadHTTPError(t, w, 400, errcode.ErrBadRequest)
		})
	}
}

func TestTeamGroupUnreadHTTPMapsRPCFailuresWithoutData(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, tc := range []struct {
			code       codes.Code
			wantStatus int
			wantCode   int
		}{
			{codes.InvalidArgument, 400, errcode.ErrBadRequest},
			{codes.Unauthenticated, 401, errcode.ErrUnAuth},
			{codes.PermissionDenied, 403, errcode.ErrForbidden},
			{codes.NotFound, 404, errcode.ErrGroupNotFound},
			{codes.Unavailable, 503, errcode.ErrInternal},
			{codes.DeadlineExceeded, 504, errcode.ErrInternal},
			{codes.Internal, 502, errcode.ErrInternal},
			{codes.Canceled, 502, errcode.ErrInternal},
			{codes.Unimplemented, 502, errcode.ErrInternal},
			{codes.Unknown, 502, errcode.ErrInternal},
		} {
			t.Run(method+"/"+tc.code.String(), func(t *testing.T) {
				err := status.Error(tc.code, "private-db original-token")
				if tc.code == codes.Unknown {
					err = errors.New("private-db original-token")
				}
				client := groupUnreadHTTPClient{
					get: func(context.Context, *pb.GetTeamGroupUnreadRequest) (*pb.GetTeamGroupUnreadResponse, error) {
						return &pb.GetTeamGroupUnreadResponse{TeamId: 200, GroupId: 300, UnreadCount: 7}, err
					},
					mark: func(context.Context, *pb.MarkTeamGroupMessagesReadRequest) (*pb.MarkTeamGroupMessagesReadResponse, error) {
						return &pb.MarkTeamGroupMessagesReadResponse{TeamId: 200, GroupId: 300, UnreadCount: 7, MessageIds: []int64{1}}, err
					},
				}
				w := httptest.NewRecorder()
				groupUnreadHTTPHandler(method, client)(w, groupUnreadHTTPRequest(method, "200", "300", "", `{"message_ids":["1"]}`, "Bearer original-token"))
				assertGroupUnreadHTTPError(t, w, tc.wantStatus, tc.wantCode)
			})
		}
		t.Run(method+"/missing client", func(t *testing.T) {
			w := httptest.NewRecorder()
			groupUnreadHTTPHandler(method, nil)(w, groupUnreadHTTPRequest(method, "200", "300", "", `{"message_ids":["1"]}`, "Bearer original-token"))
			assertGroupUnreadHTTPError(t, w, 503, errcode.ErrInternal)
		})
	}
}

func TestTeamGroupUnreadHTTPRejectsInvalidRPCResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result *pb.GetTeamGroupUnreadResponse
	}{
		{"nil", nil},
		{"wrong team", &pb.GetTeamGroupUnreadResponse{TeamId: 201, GroupId: 300}},
		{"wrong group", &pb.GetTeamGroupUnreadResponse{TeamId: 200, GroupId: 301}},
		{"negative count", &pb.GetTeamGroupUnreadResponse{TeamId: 200, GroupId: 300, UnreadCount: -1}},
	} {
		t.Run("GET/"+tc.name, func(t *testing.T) {
			client := groupUnreadHTTPClient{get: func(context.Context, *pb.GetTeamGroupUnreadRequest) (*pb.GetTeamGroupUnreadResponse, error) {
				return tc.result, nil
			}}
			w := httptest.NewRecorder()
			getTeamGroupUnreadHandler(client)(w, groupUnreadHTTPRequest(http.MethodGet, "200", "300", "", "", "Bearer original-token"))
			assertGroupUnreadHTTPError(t, w, 502, errcode.ErrInternal)
		})
	}
	for _, tc := range []struct {
		name   string
		result *pb.MarkTeamGroupMessagesReadResponse
	}{
		{"nil", nil},
		{"wrong team", &pb.MarkTeamGroupMessagesReadResponse{TeamId: 201, GroupId: 300, MessageIds: []int64{1, 2}}},
		{"wrong group", &pb.MarkTeamGroupMessagesReadResponse{TeamId: 200, GroupId: 301, MessageIds: []int64{1, 2}}},
		{"negative count", &pb.MarkTeamGroupMessagesReadResponse{TeamId: 200, GroupId: 300, MessageIds: []int64{1, 2}, UnreadCount: -1}},
		{"missing IDs", &pb.MarkTeamGroupMessagesReadResponse{TeamId: 200, GroupId: 300}},
		{"missing one ID", &pb.MarkTeamGroupMessagesReadResponse{TeamId: 200, GroupId: 300, MessageIds: []int64{1}}},
		{"extra ID", &pb.MarkTeamGroupMessagesReadResponse{TeamId: 200, GroupId: 300, MessageIds: []int64{1, 2, 3}}},
		{"replaced ID", &pb.MarkTeamGroupMessagesReadResponse{TeamId: 200, GroupId: 300, MessageIds: []int64{1, 3}}},
		{"duplicate ID", &pb.MarkTeamGroupMessagesReadResponse{TeamId: 200, GroupId: 300, MessageIds: []int64{1, 1}}},
		{"negative ID", &pb.MarkTeamGroupMessagesReadResponse{TeamId: 200, GroupId: 300, MessageIds: []int64{1, -2}}},
		{"zero ID", &pb.MarkTeamGroupMessagesReadResponse{TeamId: 200, GroupId: 300, MessageIds: []int64{1, 0}}},
	} {
		t.Run("POST/"+tc.name, func(t *testing.T) {
			client := groupUnreadHTTPClient{mark: func(context.Context, *pb.MarkTeamGroupMessagesReadRequest) (*pb.MarkTeamGroupMessagesReadResponse, error) {
				return tc.result, nil
			}}
			w := httptest.NewRecorder()
			markTeamGroupMessagesReadHandler(client)(w, groupUnreadHTTPRequest(http.MethodPost, "200", "300", "", `{"message_ids":["2","1","1"]}`, "Bearer original-token"))
			assertGroupUnreadHTTPError(t, w, 502, errcode.ErrInternal)
		})
	}
}

func TestTeamGroupUnreadHTTPInheritsDeadlineAndCancellation(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method+"/short upstream deadline", func(t *testing.T) {
			parent, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			deadline, _ := parent.Deadline()
			check := func(ctx context.Context) {
				got, _ := ctx.Deadline()
				if !got.Equal(deadline) {
					t.Fatalf("upstream deadline was extended: %v; want %v", got, deadline)
				}
			}
			client := groupUnreadHTTPClient{
				get: func(ctx context.Context, req *pb.GetTeamGroupUnreadRequest) (*pb.GetTeamGroupUnreadResponse, error) {
					check(ctx)
					return &pb.GetTeamGroupUnreadResponse{TeamId: req.TeamId, GroupId: req.GroupId}, nil
				},
				mark: func(ctx context.Context, req *pb.MarkTeamGroupMessagesReadRequest) (*pb.MarkTeamGroupMessagesReadResponse, error) {
					check(ctx)
					return &pb.MarkTeamGroupMessagesReadResponse{TeamId: req.TeamId, GroupId: req.GroupId, MessageIds: req.MessageIds}, nil
				},
			}
			w := httptest.NewRecorder()
			r := groupUnreadHTTPRequest(method, "200", "300", "", `{"message_ids":["1"]}`, "Bearer original-token")
			// Route variables live in context too; preserve them when replacing
			// the parent with the cancellation/deadline fixture.
			r = pathvar.WithVars(r.WithContext(parent), pathvar.Vars(r))
			groupUnreadHTTPHandler(method, client)(w, r)
			if w.Code != 200 || parent.Err() != nil {
				t.Fatalf("bad short deadline call: %d %s, parent error %v", w.Code, w.Body.String(), parent.Err())
			}
		})
		for _, before := range []bool{false, true} {
			name := "cancel during RPC"
			if before {
				name = "already canceled"
			}
			t.Run(method+"/"+name, func(t *testing.T) {
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				if before {
					cancel()
				}
				started := make(chan struct{})
				wait := func(ctx context.Context) error {
					close(started)
					<-ctx.Done()
					return status.FromContextError(ctx.Err()).Err()
				}
				client := groupUnreadHTTPClient{
					get: func(ctx context.Context, _ *pb.GetTeamGroupUnreadRequest) (*pb.GetTeamGroupUnreadResponse, error) {
						return nil, wait(ctx)
					},
					mark: func(ctx context.Context, _ *pb.MarkTeamGroupMessagesReadRequest) (*pb.MarkTeamGroupMessagesReadResponse, error) {
						return nil, wait(ctx)
					},
				}
				w := httptest.NewRecorder()
				r := groupUnreadHTTPRequest(method, "200", "300", "", `{"message_ids":["1"]}`, "Bearer original-token")
				r = pathvar.WithVars(r.WithContext(parent), pathvar.Vars(r))
				done := make(chan struct{})
				go func() {
					groupUnreadHTTPHandler(method, client)(w, r)
					close(done)
				}()
				select {
				case <-started:
					cancel()
				case <-time.After(time.Second):
					t.Fatal("RPC did not start")
				}
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("HTTP cancellation did not reach RPC")
				}
				assertGroupUnreadHTTPError(t, w, 502, errcode.ErrInternal)
			})
		}
	}
}

type groupUnreadTCPServer struct {
	pb.UnimplementedIMServer
	calls chan groupUnreadTCPCall
}

type groupUnreadTCPCall struct {
	method         string
	team, group    int64
	ids            []int64
	metadata       metadata.MD
	deadlineBudget time.Duration
}

func (s *groupUnreadTCPServer) record(ctx context.Context, method string, team, group int64, ids []int64) {
	md, _ := metadata.FromIncomingContext(ctx)
	deadline, _ := ctx.Deadline()
	s.calls <- groupUnreadTCPCall{method: method, team: team, group: group, ids: slices.Clone(ids), metadata: md.Copy(), deadlineBudget: time.Until(deadline)}
}

func (s *groupUnreadTCPServer) GetTeamGroupUnread(ctx context.Context, req *pb.GetTeamGroupUnreadRequest) (*pb.GetTeamGroupUnreadResponse, error) {
	s.record(ctx, http.MethodGet, req.TeamId, req.GroupId, nil)
	return &pb.GetTeamGroupUnreadResponse{TeamId: req.TeamId, GroupId: req.GroupId, UnreadCount: 9007199254740999}, nil
}

func (s *groupUnreadTCPServer) MarkTeamGroupMessagesRead(ctx context.Context, req *pb.MarkTeamGroupMessagesReadRequest) (*pb.MarkTeamGroupMessagesReadResponse, error) {
	s.record(ctx, http.MethodPost, req.TeamId, req.GroupId, req.MessageIds)
	return &pb.MarkTeamGroupMessagesReadResponse{TeamId: req.TeamId, GroupId: req.GroupId, UnreadCount: 7, MessageIds: req.MessageIds}, nil
}

func TestTeamGroupUnreadHTTPOverTCPGRPC(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	im := &groupUnreadTCPServer{calls: make(chan groupUnreadTCPCall, 2)}
	rpcServer := grpc.NewServer()
	pb.RegisterIMServer(rpcServer, im)
	go func() { _ = rpcServer.Serve(listener) }()
	t.Cleanup(rpcServer.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := pb.NewIMClient(conn)
	mux := http.NewServeMux()
	for _, route := range []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"GET /api/v1/teams/{team_id}/groups/{group_id}/unread", getTeamGroupUnreadHandler(client)},
		{"POST /api/v1/teams/{team_id}/groups/{group_id}/read", markTeamGroupMessagesReadHandler(client)},
	} {
		mux.HandleFunc(route.pattern, func(w http.ResponseWriter, r *http.Request) {
			route.handler(w, pathvar.WithVars(r, map[string]string{"team_id": r.PathValue("team_id"), "group_id": r.PathValue("group_id")}))
		})
	}
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			endpoint, body := "unread", ""
			if method == http.MethodPost {
				endpoint, body = "read", `{"message_ids":["9007199254740997","9007199254740995","9007199254740997"]}`
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			r, err := http.NewRequestWithContext(ctx, method, httpServer.URL+"/api/v1/teams/9007199254740993/groups/9007199254740994/"+endpoint, strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Authorization", "Bearer original-token")
			r.Header.Set("X-User-ID", "400")
			r.Header.Set("Idempotency-Key", "not-forwarded")
			resp, err := http.DefaultClient.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			bytes, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Code int `json:"code"`
				Data struct {
					TeamID      string   `json:"team_id"`
					GroupID     string   `json:"group_id"`
					UnreadCount string   `json:"unread_count"`
					MessageIDs  []string `json:"message_ids"`
				} `json:"data"`
			}
			if err := json.Unmarshal(bytes, &result); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != 200 || result.Code != 0 || result.Data.TeamID != "9007199254740993" || result.Data.GroupID != "9007199254740994" {
				t.Fatalf("wrong HTTP/TCP round trip: %d %s", resp.StatusCode, bytes)
			}
			select {
			case call := <-im.calls:
				if call.method != method || call.team != 9007199254740993 || call.group != 9007199254740994 ||
					!slices.Equal(call.metadata.Get("authorization"), []string{"Bearer original-token"}) ||
					len(call.metadata.Get("idempotency-key")) != 0 || len(call.metadata.Get("x-user-id")) != 0 || call.deadlineBudget <= 0 || call.deadlineBudget > 3*time.Second {
					t.Fatalf("wrong TCP request: %+v", call)
				}
				if method == http.MethodGet {
					if result.Data.UnreadCount != "9007199254740999" || len(result.Data.MessageIDs) != 0 || len(call.ids) != 0 {
						t.Fatalf("GET changed count or performed Mark: %+v %s", call, bytes)
					}
				} else if result.Data.UnreadCount != "7" || !slices.Equal(result.Data.MessageIDs, []string{"9007199254740995", "9007199254740997"}) || !slices.Equal(call.ids, []int64{9007199254740995, 9007199254740997}) {
					t.Fatalf("POST changed explicit IDs: %+v %s", call, bytes)
				}
			default:
				t.Fatal("HTTP did not reach TCP IM service")
			}
		})
	}
}
