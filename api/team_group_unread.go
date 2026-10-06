package main

import (
	"net/http"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/im/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
)

// Contract placeholders stay unavailable until the execution branch is integrated.
func getTeamGroupUnreadHandler(pb.IMClient) http.HandlerFunc        { return unavailableGroupRead }
func markTeamGroupMessagesReadHandler(pb.IMClient) http.HandlerFunc { return unavailableGroupRead }
func unavailableGroupRead(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJson(w, http.StatusServiceUnavailable, map[string]any{"code": errcode.ErrInternal, "msg": "group read is not enabled"})
}
