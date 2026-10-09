package main

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/rest/httpx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type response struct {
	Code int       `json:"code"`
	Msg  string    `json:"msg"`
	Data *userInfo `json:"data,omitempty"`
}

type userInfo struct {
	ID       int64  `json:"id,string"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
}

type userInfoGetter interface {
	GetUserInfo(context.Context, *pb.GetUserInfoRequest, ...grpc.CallOption) (*pb.GetUserInfoResponse, error)
}

func getUserInfoHandler(client userInfoGetter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 这里只校验请求格式。查询哪个用户、用户是否存在，由 RPC 处理。
		query, err := url.ParseQuery(r.URL.RawQuery)
		ids := query["user_id"]
		var id int64
		if err == nil && len(ids) == 1 {
			id, err = strconv.ParseInt(ids[0], 10, 64)
		}
		if len(ids) != 1 || err != nil || id <= 0 {
			httpx.WriteJson(w, http.StatusBadRequest, response{
				Code: errcode.ErrBadRequest, Msg: "user_id must be a single positive integer",
			})
			return
		}

		// HTTP 请求取消时，RPC 也会收到取消信号；RPC 等待上限在配置中设为 2 秒。
		user, err := client.GetUserInfo(r.Context(), &pb.GetUserInfoRequest{UserId: id})
		writeUserResult(w, r, user, err)
	}
}

// 演示查询与真实本人查询共用 HTTP 响应和 RPC 错误映射。
func writeUserResult(w http.ResponseWriter, r *http.Request, user *pb.GetUserInfoResponse, err error) {
	if err != nil {
		logx.WithContext(r.Context()).Errorf("GetUserInfo RPC failed: %v", err)
		httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "user service error"
		switch status.Code(err) {
		case codes.Unauthenticated:
			httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
		case codes.PermissionDenied:
			httpStatus, code, message = http.StatusForbidden, errcode.ErrUserBanned, "user is disabled"
		case codes.InvalidArgument:
			httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid user_id"
		case codes.NotFound:
			httpStatus, code, message = http.StatusNotFound, errcode.ErrUserNotFound, "user not found"
		case codes.Unavailable:
			httpStatus, message = http.StatusServiceUnavailable, "user service unavailable"
		case codes.DeadlineExceeded:
			httpStatus, message = http.StatusGatewayTimeout, "user service timeout"
		}
		// 内部地址、连接错误等详情留在服务端日志，不直接作为 HTTP 响应。
		httpx.WriteJson(w, httpStatus, response{Code: code, Msg: message})
		return
	}

	httpx.WriteJson(w, http.StatusOK, response{
		Code: errcode.Success, Msg: "success",
		Data: &userInfo{ID: user.GetId(), Username: user.GetUsername(), Nickname: user.GetNickname()},
	})
}
