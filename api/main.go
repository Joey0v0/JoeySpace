package main

import (
	"flag"
	"fmt"
	"net/http"
	"time"

	"github.com/yjydist/go-im/examples"
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
	server.AddRoutes([]rest.Route{
		{Method: http.MethodGet, Path: "/demo/multi-draft-core.js", Handler: chatDemoScriptHandler(examples.MultiDraftCoreJS)},
		{Method: http.MethodGet, Path: "/demo/multi-draft-actions.js", Handler: chatDemoScriptHandler(examples.MultiDraftActionsJS)},
		{Method: http.MethodGet, Path: "/demo/multi-draft-view.js", Handler: chatDemoScriptHandler(examples.MultiDraftViewJS)},
		{Method: http.MethodGet, Path: "/demo/task-notifications.js", Handler: chatDemoScriptHandler(examples.TaskNotificationsJS)},
		{Method: http.MethodGet, Path: "/demo/task-notifications-view.js", Handler: chatDemoScriptHandler(examples.TaskNotificationsViewJS)},
		{Method: http.MethodGet, Path: "/demo/team-group-unread.js", Handler: chatDemoScriptHandler(examples.TeamGroupUnreadJS)},
		{Method: http.MethodGet, Path: "/demo/direct-unread.js", Handler: chatDemoScriptHandler(examples.DirectUnreadJS)},
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodGet,
		Path:    "/api/v1/teams/:team_id/groups/:group_id/agent-triggers/:message_id",
		Handler: getTaskTriggerStatusHandler(agentpb.NewAgentClient(agentRPCClient.Conn())),
	}, rest.WithTimeout(15*time.Second))
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
		Method:  http.MethodPost,
		Path:    "/api/v1/teams/:team_id/groups/:group_id/task-draft-collections",
		Handler: prepareTaskDraftCollectionHandler(agentpb.NewAgentClient(agentRPCClient.Conn())),
	}, rest.WithTimeout(22*time.Second))
	server.AddRoute(rest.Route{
		Method:  http.MethodGet,
		Path:    "/api/v1/agent/runs/:run_id/drafts",
		Handler: getTaskDraftCollectionHandler(agentpb.NewAgentClient(agentRPCClient.Conn())),
	}, rest.WithTimeout(15*time.Second))
	server.AddRoute(rest.Route{
		Method:  http.MethodGet,
		Path:    "/api/v1/agent/runs/:run_id/drafts/:item_index",
		Handler: getTaskDraftItemHandler(agentpb.NewAgentClient(agentRPCClient.Conn())),
	}, rest.WithTimeout(15*time.Second))
	server.AddRoute(rest.Route{
		Method:  http.MethodPut,
		Path:    "/api/v1/agent/runs/:run_id/drafts/:item_index",
		Handler: editTaskDraftItemTextHandler(agentpb.NewAgentClient(agentRPCClient.Conn())),
	}, rest.WithTimeout(15*time.Second))
	server.AddRoute(rest.Route{
		Method:  http.MethodPut,
		Path:    "/api/v1/agent/runs/:run_id/drafts/:item_index/assignee",
		Handler: selectTaskDraftItemAssigneeHandler(agentpb.NewAgentClient(agentRPCClient.Conn())),
	}, rest.WithTimeout(15*time.Second))
	server.AddRoute(rest.Route{
		Method:  http.MethodPut,
		Path:    "/api/v1/agent/runs/:run_id/drafts/:item_index/deadline",
		Handler: editTaskDraftItemDeadlineHandler(agentpb.NewAgentClient(agentRPCClient.Conn())),
	}, rest.WithTimeout(15*time.Second))
	server.AddRoute(rest.Route{
		Method:  http.MethodPost,
		Path:    "/api/v1/agent/runs/:run_id/drafts/:item_index/confirm",
		Handler: confirmTaskDraftItemHandler(agentpb.NewAgentClient(agentRPCClient.Conn())),
	}, rest.WithTimeout(20*time.Second))
	server.AddRoute(rest.Route{
		Method:  http.MethodPost,
		Path:    "/api/v1/agent/runs/:run_id/drafts/:item_index/skip",
		Handler: skipTaskDraftItemHandler(agentpb.NewAgentClient(agentRPCClient.Conn())),
	}, rest.WithTimeout(15*time.Second))
	server.AddRoute(rest.Route{
		Method:  http.MethodPost,
		Path:    "/api/v1/agent/runs/:run_id/drafts/:item_index/reply/retry",
		Handler: retryTaskReplyItemHandler(agentpb.NewAgentClient(agentRPCClient.Conn())),
	}, rest.WithTimeout(19*time.Second))
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
		Method:  http.MethodPut,
		Path:    "/api/v1/agent/runs/:run_id/draft/deadline",
		Handler: editTaskDraftDeadlineHandler(agentpb.NewAgentClient(agentRPCClient.Conn())),
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
		Method:  http.MethodGet,
		Path:    "/api/v1/teams/:team_id/task-notifications",
		Handler: listTaskNotificationsHandler(taskpb.NewTaskClient(taskRPCClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodPut,
		Path:    "/api/v1/teams/:team_id/task-notifications/:notification_id/read",
		Handler: markTaskNotificationReadHandler(taskpb.NewTaskClient(taskRPCClient.Conn())),
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
		Method:  http.MethodGet,
		Path:    "/api/v1/teams/:team_id/groups/:group_id",
		Handler: getTeamGroupHandler(impb.NewIMClient(imRPCClient.Conn())),
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
		Path:    "/api/v1/teams/:team_id/groups/:group_id/unread",
		Handler: getTeamGroupUnreadHandler(impb.NewIMClient(imRPCClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodPost,
		Path:    "/api/v1/teams/:team_id/groups/:group_id/read",
		Handler: markTeamGroupMessagesReadHandler(impb.NewIMClient(imRPCClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodGet,
		Path:    "/api/v1/direct/:peer_id/messages",
		Handler: listDirectMessagesHandler(impb.NewIMClient(imRPCClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodGet,
		Path:    "/api/v1/me/direct-conversations",
		Handler: listMyDirectConversationsHandler(impb.NewIMClient(imRPCClient.Conn()), userpb.NewUserClient(rpcClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodGet,
		Path:    "/api/v1/messages/unread-conversations",
		Handler: listMyUnreadConversationsHandler(impb.NewIMClient(imRPCClient.Conn()), userpb.NewUserClient(rpcClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodGet,
		Path:    "/api/v1/me/direct-conversations/:peer_id",
		Handler: getMyDirectConversationHandler(impb.NewIMClient(imRPCClient.Conn()), userpb.NewUserClient(rpcClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodGet,
		Path:    "/api/v1/direct/:peer_id/unread",
		Handler: getDirectUnreadHandler(impb.NewIMClient(imRPCClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodPost,
		Path:    "/api/v1/direct/:peer_id/read",
		Handler: markDirectMessagesReadHandler(impb.NewIMClient(imRPCClient.Conn())),
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
		Method:  http.MethodGet,
		Path:    "/api/v1/teams",
		Handler: listMyTeamsHandler(userpb.NewUserClient(rpcClient.Conn())),
	})
	server.AddRoute(rest.Route{
		Method:  http.MethodPost,
		Path:    "/api/v1/teams/:team_id/leave",
		Handler: leaveTeamHandler(userpb.NewUserClient(rpcClient.Conn())),
	}, rest.WithTimeout(7*time.Second))
	server.AddRoute(rest.Route{
		Method:  http.MethodGet,
		Path:    "/api/v1/teams/:team_id/leave",
		Handler: getTeamLeaveOperationHandler(userpb.NewUserClient(rpcClient.Conn())),
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
