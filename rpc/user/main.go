package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/bwmarrin/snowflake"
	"github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/zrpc"
	"google.golang.org/grpc"
)

func main() {
	configFile := flag.String("f", "rpc/user/etc/user.yaml", "RPC config file")
	profileEnabled := flag.Bool("profile", false, "enable database-backed user methods")
	flag.Parse()

	var c zrpc.RpcServerConf
	conf.MustLoad(*configFile, &c)
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

	// 将我们实现的查询方法注册到独立的 RPC 服务中。
	s := zrpc.MustNewServer(c, func(server *grpc.Server) {
		pb.RegisterUserServer(server, users)
	})
	defer s.Stop()

	fmt.Printf("User RPC listening on %s (profile enabled: %t)\n", c.ListenOn, *profileEnabled)
	s.Start()
}
