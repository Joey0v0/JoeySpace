package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type createTeamResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data *createTeamData `json:"data,omitempty"`
}

type createTeamData struct {
	TeamID int64 `json:"team_id,string"`
}

func createTeamHandler(client pb.UserClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		headers := r.Header.Values("Authorization")
		if len(headers) != 1 {
			httpx.WriteJson(w, http.StatusUnauthorized, createTeamResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		parts := strings.Fields(headers[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			httpx.WriteJson(w, http.StatusUnauthorized, createTeamResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}

		var req struct {
			Name string `json:"name"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			httpx.WriteJson(w, http.StatusBadRequest, createTeamResponse{Code: errcode.ErrBadRequest, Msg: "invalid team request"})
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			httpx.WriteJson(w, http.StatusBadRequest, createTeamResponse{Code: errcode.ErrBadRequest, Msg: "invalid team request"})
			return
		}
		name := strings.TrimSpace(req.Name)
		if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 64 {
			httpx.WriteJson(w, http.StatusBadRequest, createTeamResponse{Code: errcode.ErrBadRequest, Msg: "invalid team name"})
			return
		}

		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer "+parts[1]))
		result, err := client.CreateTeam(ctx, &pb.CreateTeamRequest{Name: name})
		if err != nil {
			httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "team service error"
			switch status.Code(err) {
			case codes.InvalidArgument:
				httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid team name"
			case codes.Unauthenticated:
				httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
			case codes.PermissionDenied:
				httpStatus, code, message = http.StatusForbidden, errcode.ErrUserBanned, "user is disabled"
			case codes.NotFound:
				httpStatus, code, message = http.StatusNotFound, errcode.ErrUserNotFound, "user not found"
			case codes.Unavailable:
				httpStatus, message = http.StatusServiceUnavailable, "team service unavailable"
			case codes.DeadlineExceeded:
				httpStatus, message = http.StatusGatewayTimeout, "team service timeout"
			}
			httpx.WriteJson(w, httpStatus, createTeamResponse{Code: code, Msg: message})
			return
		}
		httpx.WriteJson(w, http.StatusOK, createTeamResponse{Code: errcode.Success, Msg: "success", Data: &createTeamData{TeamID: result.GetTeamId()}})
	}
}
