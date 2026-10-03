package main

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Code int        `json:"code"`
	Msg  string     `json:"msg"`
	Data *loginData `json:"data,omitempty"`
}

type loginData struct {
	Token string `json:"token"`
}

func loginHandler(client pb.UserClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil || req.Username == "" || req.Password == "" {
			httpx.WriteJson(w, http.StatusBadRequest, loginResponse{Code: errcode.ErrBadRequest, Msg: "username and password are required"})
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			httpx.WriteJson(w, http.StatusBadRequest, loginResponse{Code: errcode.ErrBadRequest, Msg: "invalid login request"})
			return
		}

		result, err := client.Login(r.Context(), &pb.LoginRequest{Username: req.Username, Password: req.Password})
		if err != nil {
			httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "user service error"
			switch status.Code(err) {
			case codes.InvalidArgument:
				httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid login request"
			case codes.Unauthenticated:
				httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid username or password"
			case codes.PermissionDenied:
				httpStatus, code, message = http.StatusForbidden, errcode.ErrUserBanned, "user is disabled"
			case codes.Unavailable:
				httpStatus, message = http.StatusServiceUnavailable, "user service unavailable"
			case codes.DeadlineExceeded:
				httpStatus, message = http.StatusGatewayTimeout, "user service timeout"
			}
			httpx.WriteJson(w, httpStatus, loginResponse{Code: code, Msg: message})
			return
		}
		httpx.WriteJson(w, http.StatusOK, loginResponse{
			Code: errcode.Success, Msg: "success",
			Data: &loginData{Token: result.GetToken()},
		})
	}
}
