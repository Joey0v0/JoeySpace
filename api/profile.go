package main

import (
	"net/http"
	"strings"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"google.golang.org/grpc/metadata"
)

func getMyInfoHandler(client pb.UserClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 本人查询不接收 user_id；身份只能由已验证的登录 Token 确定。
		if r.URL.RawQuery != "" {
			httpx.WriteJson(w, http.StatusBadRequest, response{Code: errcode.ErrBadRequest, Msg: "profile query takes no query parameters"})
			return
		}
		headers := r.Header.Values("Authorization")
		if len(headers) != 1 {
			httpx.WriteJson(w, http.StatusUnauthorized, response{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		parts := strings.Fields(headers[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			httpx.WriteJson(w, http.StatusUnauthorized, response{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		// 把凭证放在 RPC metadata 中，RPC 验签成功后才能访问用户数据。
		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer "+parts[1]))
		user, err := client.GetMyInfo(ctx, &pb.GetMyInfoRequest{})
		writeUserResult(w, r, user, err)
	}
}
