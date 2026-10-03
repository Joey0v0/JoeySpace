package main

import (
	"flag"
	"fmt"
	"net/http"
	"time"

	agentpb "github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/zrpc"
)

type config struct {
	rest.RestConf
	UserRPC  zrpc.RpcClientConf
	IMRPC    zrpc.RpcClientConf
	TaskRPC  zrpc.RpcClientConf
	AgentRPC zrpc.RpcClientConf
}

func main() {
	configFile := flag.String("f", "api/etc/api.yaml", "API config file")
	flag.Parse()
	var c config
	conf.MustLoad(*configFile, &c)

	// 整个 API 进程复用各服务的 RPC 客户端，不为每次 HTTP 请求重新建连接。
	rpcClient := zrpc.MustNewClient(c.UserRPC)
	defer rpcClient.Conn().Close()
	imRPCClient := zrpc.MustNewClient(c.IMRPC)
	defer imRPCClient.Conn().Close()
	taskRPCClient := zrpc.MustNewClient(c.TaskRPC)
	defer taskRPCClient.Conn().Close()
	agentRPCClient := zrpc.MustNewClient(c.AgentRPC)
	defer agentRPCClient.Conn().Close()

	server := rest.MustNewServer(c.RestConf)
	defer server.Stop()
	server.AddRoute(rest.Route{
		Method:  http.MethodGet,
		Path:    "/demo/chat",
		Handler: chatDemoHandler,
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodPost,
		Path:    "/api/v1/teams/:team_id/groups/:group_id/ask",
		Handler: askAgentHandler(agentpb.NewAgentClient(agentRPCClient.Conn())),
	}, rest.WithTimeout(22*time.Second))
	server.AddRoute(rest.Route{
		Method:  http.MethodPost,
		Path:    "/api/v1/teams/:team_id/groups/:group_id/task-drafts",
		Handler: prepareTaskDraftHandler(agentpb.NewAgentClient(agentRPCClient.Conn())),
	}, rest.WithTimeout(22*time.Second))
	server.AddRoute(rest.Route{
		Method:  http.MethodGet,
		Path:    "/api/v1/agent/runs/:run_id/draft",
		Handler: getTaskDraftHandler(agentpb.NewAgentClient(agentRPCClient.Conn())),
	}, rest.WithTimeout(15*time.Second))
	server.AddRoute(rest.Route{
		Method:  http.MethodPut,
		Path:    "/api/v1/agent/runs/:run_id/draft",
		Handler: editTaskDraftHandler(agentpb.NewAgentClient(agentRPCClient.Conn())),
	}, rest.WithTimeout(15*time.Second))
	server.AddRoute(rest.Route{
		Method:  http.MethodPut,
		Path:    "/api/v1/agent/runs/:run_id/draft/assignee",
		Handler: selectTaskDraftAssigneeHandler(agentpb.NewAgentClient(agentRPCClient.Conn())),
	}, rest.WithTimeout(15*time.Second))
	server.AddRoute(rest.Route{
		Method:  http.MethodPost,
		Path:    "/api/v1/agent/runs/:run_id/confirm",
		Handler: confirmTaskDraftHandler(agentpb.NewAgentClient(agentRPCClient.Conn())),
	}, rest.WithTimeout(19*time.Second))
	server.AddRoute(rest.Route{
		Method:  http.MethodPost,
		Path:    "/api/v1/agent/runs/:run_id/reply/retry",
		Handler: retryTaskReplyHandler(agentpb.NewAgentClient(agentRPCClient.Conn())),
	}, rest.WithTimeout(19*time.Second))
	server.AddRoute(rest.Route{
		Method:  http.MethodPost,
		Path:    "/api/v1/teams/:team_id/groups",
		Handler: createTeamGroupHandler(impb.NewIMClient(imRPCClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodPost,
		Path:    "/api/v1/teams/:team_id/tasks",
		Handler: createTaskHandler(taskpb.NewTaskClient(taskRPCClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodGet,
		Path:    "/api/v1/teams/:team_id/tasks",
		Handler: listTeamTasksHandler(taskpb.NewTaskClient(taskRPCClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodPut,
		Path:    "/api/v1/teams/:team_id/tasks/:task_id/status",
		Handler: setTaskStatusHandler(taskpb.NewTaskClient(taskRPCClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodGet,
		Path:    "/api/v1/teams/:team_id/groups",
		Handler: listTeamGroupsHandler(impb.NewIMClient(imRPCClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodPost,
		Path:    "/api/v1/teams/:team_id/groups/:group_id/join",
		Handler: joinTeamGroupHandler(impb.NewIMClient(imRPCClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodGet,
		Path:    "/api/v1/teams/:team_id/groups/:group_id/messages",
		Handler: listTeamGroupMessagesHandler(impb.NewIMClient(imRPCClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodGet,
		Path:    "/api/v1/message/offline",
		Handler: listOfflineMessagesHandler(impb.NewIMClient(imRPCClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodPost,
		Path:    "/api/v1/message/offline/ack",
		Handler: ackOfflineMessagesHandler(impb.NewIMClient(imRPCClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodPost,
		Path:    "/api/v1/teams",
		Handler: createTeamHandler(userpb.NewUserClient(rpcClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodPost,
		Path:    "/api/v1/teams/:team_id/members",
		Handler: addTeamMemberHandler(userpb.NewUserClient(rpcClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodGet,
		Path:    "/api/v1/teams/:team_id/members",
		Handler: listTeamMembersHandler(userpb.NewUserClient(rpcClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodPut,
		Path:    "/api/v1/teams/:team_id/members/:user_id/role",
		Handler: setTeamMemberRoleHandler(userpb.NewUserClient(rpcClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodPost,
		Path:    "/api/v1/user/register",
		Handler: registerHandler(userpb.NewUserClient(rpcClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodPost,
		Path:    "/api/v1/user/login",
		Handler: loginHandler(userpb.NewUserClient(rpcClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodGet,
		Path:    "/api/v1/user/info",
		Handler: getMyInfoHandler(userpb.NewUserClient(rpcClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodGet,
		Path:    "/demo/user/info",
		Handler: getUserInfoHandler(userpb.NewUserClient(rpcClient.Conn())),
	})

	fmt.Printf("API listening on http://%s:%d\n", c.Host, c.Port)
	server.Start()
}
