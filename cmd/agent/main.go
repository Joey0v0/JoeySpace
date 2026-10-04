package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/bwmarrin/snowflake"
	agent "github.com/yjydist/go-im/rpc/agent"
	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/zrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/gorm"
)

func main() {
	configFile := flag.String("f", "rpc/agent/etc/agent.yaml", "Agent RPC config file")
	flag.Parse()
	var c zrpc.RpcServerConf
	conf.MustLoad(*configFile, &c)
	if err := runAgent(c); err != nil {
		log.Fatal(err)
	}
}

func runAgent(c zrpc.RpcServerConf) error {
	triggerConfig, err := loadAgentTriggerInboxConfig(os.Getenv)
	if err != nil {
		return err
	}
	workerConfig, err := loadAgentTriggerWorkerConfig(os.Getenv)
	if err != nil {
		return err
	}
	triggerSource, err := prepareAgentTriggerWorkerSource(workerConfig, os.Getenv, nil)
	if err != nil {
		return err
	}
	defer triggerSource.Close()
	dsn := os.Getenv("AGENT_MYSQL_DSN")
	if strings.TrimSpace(dsn) == "" {
		return errors.New("AGENT_MYSQL_DSN is required")
	}
	db, err := openAgentDatabase(dsn)
	if err != nil {
		return err
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	impl, closeClients, err := newAgentServer(context.Background(), os.Getenv("IM_RPC_ADDR"), os.Getenv("TASK_RPC_ADDR"), os.Getenv("USER_RPC_ADDR"), db)
	if err != nil {
		return err
	}
	defer closeClients()
	inbox, err := newAgentTriggerInbox(triggerConfig, db, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err := inbox.Stop(); err != nil {
			log.Print(err)
		}
	}()
	worker, err := newAgentTriggerWorker(workerConfig, impl, triggerSource, db, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err := worker.Stop(); err != nil {
			log.Print(err)
		}
	}()
	s, err := zrpc.NewServer(c, func(server *grpc.Server) {
		pb.RegisterAgentServer(server, impl)
		// The ordinary service is registered before background work begins.
		// A worker fault is reported without stopping that service or intake.
		if err := worker.Start(context.Background(), func(err error) { log.Print(err) }); err != nil {
			log.Print("cannot start Agent trigger worker")
		}
	})
	if err != nil {
		return errors.New("cannot prepare Agent RPC server")
	}
	defer s.Stop()
	if err := inbox.Start(context.Background(), func(err error) { log.Print(err) }); err != nil {
		return err
	}
	s.Start()
	return nil
}

func newAgentServer(ctx context.Context, imAddr, taskAddr, userAddr string, db *gorm.DB) (*agent.Server, func(), error) {
	if strings.TrimSpace(imAddr) == "" || strings.TrimSpace(taskAddr) == "" || strings.TrimSpace(userAddr) == "" {
		return nil, nil, errors.New("IM_RPC_ADDR, TASK_RPC_ADDR and USER_RPC_ADDR are required")
	}
	if db == nil {
		return nil, nil, errors.New("Agent database is required")
	}
	nodeID := int64(5) // 旧 API、User、IM、Task 已分别使用 1、2、3、4。
	if value := os.Getenv("AGENT_SNOWFLAKE_NODE_ID"); value != "" {
		var parseErr error
		nodeID, parseErr = strconv.ParseInt(value, 10, 64)
		if parseErr != nil {
			return nil, nil, errors.New("invalid AGENT_SNOWFLAKE_NODE_ID")
		}
	}
	idNode, err := snowflake.NewNode(nodeID)
	if err != nil {
		return nil, nil, errors.New("invalid AGENT_SNOWFLAKE_NODE_ID")
	}
	botClient, closeBotClient, err := agent.NewBotReplyClient(os.Getenv)
	if err != nil {
		return nil, nil, err
	}
	keepBotClient := false
	defer func() {
		if !keepBotClient {
			closeBotClient()
		}
	}()
	generator, err := agent.NewArkGroupReplyGenerator(ctx)
	if err != nil {
		return nil, nil, err
	}
	draftGenerator, err := agent.NewArkTaskDraftGenerator(ctx)
	if err != nil {
		return nil, nil, err
	}
	imConn, err := grpc.NewClient(imAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, errors.New("invalid IM_RPC_ADDR")
	}
	taskConn, err := grpc.NewClient(taskAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		imConn.Close()
		return nil, nil, errors.New("invalid TASK_RPC_ADDR")
	}
	userConn, err := grpc.NewClient(userAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		taskConn.Close()
		imConn.Close()
		return nil, nil, errors.New("invalid USER_RPC_ADDR")
	}
	reader := agent.NewContextReader(impb.NewIMClient(imConn), taskpb.NewTaskClient(taskConn))
	impl := agent.NewServer(agent.NewGroupAnswerer(reader, generator))
	impl.ConfigureDraftAccess(db, userpb.NewUserClient(userConn), impb.NewIMClient(imConn))
	impl.ConfigureDraftPreparation(db, userpb.NewUserClient(userConn), impb.NewIMClient(imConn), draftGenerator, idNode)
	impl.ConfigureDraftConfirmation(db, taskpb.NewTaskClient(taskConn))
	impl.ConfigureDraftReplies(db, botClient)
	keepBotClient = true
	return impl, func() { closeBotClient(); userConn.Close(); taskConn.Close(); imConn.Close() }, nil
}
