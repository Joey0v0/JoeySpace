package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/bwmarrin/snowflake"
	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/zrpc"
	"google.golang.org/grpc"
)

func main() {
	configFile := flag.String("f", "rpc/user/etc/user.yaml", "RPC config file")
	profileEnabled := flag.Bool("profile", false, "enable database-backed user methods")
	flag.Parse()

	triggerConfig, err := loadUserTriggerConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	pushConfig, err := loadUserPushConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	leaveIMConfig, err := loadTeamLeaveIMConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	leaveConfigured, _ := validateTeamLeaveIMConfig(leaveIMConfig)
	if leaveConfigured && !*profileEnabled {
		log.Fatal("User leave IM RPC requires -profile")
	}
	var c zrpc.RpcServerConf
	conf.MustLoad(*configFile, &c)
	if err := validateUserTriggerStartup(triggerConfig, *profileEnabled, c.ListenOn); err != nil {
		log.Fatal(err)
	}
	if err := validateUserPushStartup(pushConfig, *profileEnabled, c.ListenOn, triggerConfig.ListenOn); err != nil {
		log.Fatal(err)
	}
	users := &userServer{}
	if *profileEnabled {
		dsn, secret := os.Getenv("USER_MYSQL_DSN"), os.Getenv("USER_JWT_SECRET")
		if dsn == "" || secret == "" {
			log.Fatal("-profile requires USER_MYSQL_DSN and USER_JWT_SECRET")
		}
		db, err := openProfileDatabase(dsn)
		if err != nil {
			log.Fatal(err)
		}
		sqlDB, _ := db.DB()
		defer sqlDB.Close()
		users.db, users.jwtSecret = db, secret
		nodeID := int64(2) // 旧 API 当前使用节点号 1；两个写入进程不能共用节点号。
		if value := os.Getenv("USER_SNOWFLAKE_NODE_ID"); value != "" {
			nodeID, err = strconv.ParseInt(value, 10, 64)
			if err != nil {
				log.Fatal("invalid USER_SNOWFLAKE_NODE_ID")
			}
		}
		users.idNode, err = snowflake.NewNode(nodeID)
		if err != nil {
			log.Fatal("invalid USER_SNOWFLAKE_NODE_ID")
		}
	}
	leaveClient, err := newTeamLeaveIMClient(leaveIMConfig)
	if err != nil {
		if users.db != nil {
			sqlDB, _ := users.db.DB()
			_ = sqlDB.Close()
		}
		log.Fatal(err)
	}
	defer leaveClient.Close()
	users.leaveIM = leaveClient

	triggerRuntime, err := newUserTriggerRuntime(triggerConfig, users)
	if err != nil {
		// log.Fatal skips defers, so release the prepared database explicitly.
		if users.db != nil {
			sqlDB, _ := users.db.DB()
			_ = sqlDB.Close()
		}
		_ = leaveClient.Close()
		log.Fatal(err)
	}
	pushRuntime, err := newUserPushRuntime(pushConfig, users)
	if err != nil {
		triggerRuntime.Stop()
		_ = leaveClient.Close()
		if users.db != nil {
			sqlDB, _ := users.db.DB()
			_ = sqlDB.Close()
		}
		log.Fatal(err)
	}
	defer triggerRuntime.Stop()
	defer pushRuntime.Stop()
	ordinaryReady := make(chan *grpc.Server, 1)
	privateFailed := make(chan struct{}, 2)
	// 将我们实现的查询方法注册到独立的 RPC 服务中。
	c = rpcauth.WithoutRPCRequestContent(c, &pb.User_ServiceDesc)
	s := zrpc.MustNewServer(c, func(server *grpc.Server) {
		pb.RegisterUserServer(server, users)
		if triggerRuntime != nil || pushRuntime != nil {
			// go-zero invokes this callback during Start, after binding its port.
			ordinaryReady <- server
		}
		if triggerRuntime != nil {
			go func() {
				if err := triggerRuntime.server.Serve(triggerRuntime.listener); err != nil {
					privateFailed <- struct{}{}
				}
			}()
		}
		if pushRuntime != nil {
			go func() {
				if err := pushRuntime.server.Serve(pushRuntime.listener); err != nil {
					privateFailed <- struct{}{}
				}
			}()
		}
	})
	defer s.Stop()

	fmt.Printf("User RPC listening on %s (profile enabled: %t)\n", c.ListenOn, *profileEnabled)
	if triggerRuntime == nil && pushRuntime == nil {
		s.Start()
		return
	}
	if waitUserPrivateServers(s.Start, ordinaryReady, privateFailed) {
		// Panic preserves defers (unlike log.Fatal) and exits nonzero so a
		// supervisor does not mistake a lost authorization listener for success.
		log.Panic("User dedicated listener stopped unexpectedly; stopping User RPC")
	}
}
