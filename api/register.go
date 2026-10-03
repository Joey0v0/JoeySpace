package main

import (
	"encoding/json"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type registerRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Nickname string `json:"nickname"`
}

func registerHandler(client pb.UserClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req registerRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			httpx.WriteJson(w, http.StatusBadRequest, response{Code: errcode.ErrBadRequest, Msg: "invalid register request"})
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			httpx.WriteJson(w, http.StatusBadRequest, response{Code: errcode.ErrBadRequest, Msg: "invalid register request"})
			return
		}
		if n := utf8.RuneCountInString(req.Username); n < 3 || n > 32 {
			httpx.WriteJson(w, http.StatusBadRequest, response{Code: errcode.ErrBadRequest, Msg: "invalid username"})
			return
		}
		if n := utf8.RuneCountInString(req.Password); n < 6 || n > 64 || len(req.Password) > 72 {
			httpx.WriteJson(w, http.StatusBadRequest, response{Code: errcode.ErrBadRequest, Msg: "invalid password"})
			return
		}
		if utf8.RuneCountInString(req.Nickname) > 64 {
			httpx.WriteJson(w, http.StatusBadRequest, response{Code: errcode.ErrBadRequest, Msg: "invalid nickname"})
			return
		}

		_, err := client.Register(r.Context(), &pb.RegisterRequest{
			Username: req.Username, Password: req.Password, Nickname: req.Nickname,
		})
		if err != nil {
			httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "user service error"
			switch status.Code(err) {
			case codes.InvalidArgument:
				httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid register request"
			case codes.AlreadyExists:
				httpStatus, code, message = http.StatusConflict, errcode.ErrUserExist, "username already exists"
			case codes.Unavailable:
				httpStatus, message = http.StatusServiceUnavailable, "user service unavailable"
			case codes.DeadlineExceeded:
				httpStatus, message = http.StatusGatewayTimeout, "user service timeout"
			}
			httpx.WriteJson(w, httpStatus, response{Code: code, Msg: message})
			return
		}
		httpx.WriteJson(w, http.StatusOK, response{Code: errcode.Success, Msg: "success"})
	}
}
