package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/agent/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type agentAsker interface {
	Ask(context.Context, *pb.AskRequest, ...grpc.CallOption) (*pb.AskResponse, error)
}

type askAgentResponse struct {
	Code int           `json:"code"`
	Msg  string        `json:"msg"`
	Data *askAgentData `json:"data,omitempty"`
}

type askAgentData struct {
	Answer string `json:"answer"`
}

func askAgentHandler(client agentAsker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		headers := r.Header.Values("Authorization")
		if len(headers) != 1 {
			httpx.WriteJson(w, http.StatusUnauthorized, askAgentResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		parts := strings.Fields(headers[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			httpx.WriteJson(w, http.StatusUnauthorized, askAgentResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		vars := pathvar.Vars(r)
		teamID, teamErr := strconv.ParseInt(vars["team_id"], 10, 64)
		groupID, groupErr := strconv.ParseInt(vars["group_id"], 10, 64)
		if teamErr != nil || groupErr != nil || teamID <= 0 || groupID <= 0 {
			httpx.WriteJson(w, http.StatusBadRequest, askAgentResponse{Code: errcode.ErrBadRequest, Msg: "invalid team or group ID"})
			return
		}
		var body struct {
			Question string `json:"question"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			httpx.WriteJson(w, http.StatusBadRequest, askAgentResponse{Code: errcode.ErrBadRequest, Msg: "invalid question request"})
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			httpx.WriteJson(w, http.StatusBadRequest, askAgentResponse{Code: errcode.ErrBadRequest, Msg: "invalid question request"})
			return
		}
		question := strings.TrimSpace(body.Question)
		if !utf8.ValidString(question) || question == "" || utf8.RuneCountInString(question) > 2000 {
			httpx.WriteJson(w, http.StatusBadRequest, askAgentResponse{Code: errcode.ErrBadRequest, Msg: "question must contain 1 to 2000 characters"})
			return
		}

		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer "+parts[1]))
		result, err := client.Ask(ctx, &pb.AskRequest{TeamId: teamID, GroupId: groupID, Question: question})
		if err != nil {
			httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "AI service error"
			switch status.Code(err) {
			case codes.InvalidArgument:
				httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid question request"
			case codes.Unauthenticated:
				httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
			case codes.PermissionDenied:
				httpStatus, code, message = http.StatusForbidden, errcode.ErrForbidden, "group and team membership required"
			case codes.NotFound:
				httpStatus, code, message = http.StatusNotFound, errcode.ErrGroupNotFound, "team group not found"
			case codes.Unavailable, codes.FailedPrecondition:
				httpStatus, message = http.StatusServiceUnavailable, "AI service unavailable"
			case codes.DeadlineExceeded:
				httpStatus, message = http.StatusGatewayTimeout, "AI answer timeout"
			}
			httpx.WriteJson(w, httpStatus, askAgentResponse{Code: code, Msg: message})
			return
		}
		if result == nil || strings.TrimSpace(result.GetAnswer()) == "" {
			httpx.WriteJson(w, http.StatusBadGateway, askAgentResponse{Code: errcode.ErrInternal, Msg: "AI service returned no answer"})
			return
		}
		httpx.WriteJson(w, http.StatusOK, askAgentResponse{Code: errcode.Success, Msg: "success", Data: &askAgentData{Answer: result.GetAnswer()}})
	}
}
