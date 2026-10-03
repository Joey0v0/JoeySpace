package main

import (
	"errors"
	"flag"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/bwmarrin/snowflake"
	driver "github.com/go-sql-driver/mysql"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/zrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	configFile := flag.String("f", "rpc/task/etc/task.yaml", "Task RPC config file")
	flag.Parse()
	var c zrpc.RpcServerConf
	conf.MustLoad(*configFile, &c)
	dsn, userAddr := os.Getenv("TASK_MYSQL_DSN"), os.Getenv("USER_RPC_ADDR")
	if dsn == "" || userAddr == "" {
		log.Fatal("TASK_MYSQL_DSN and USER_RPC_ADDR are required")
	}
	db, err := openTaskDatabase(dsn)
	if err != nil {
		log.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	conn, err := grpc.NewClient(userAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	var imClient impb.IMClient
	if imAddr := os.Getenv("IM_RPC_ADDR"); imAddr != "" {
		imConn, err := grpc.NewClient(imAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Fatal(err)
		}
		defer imConn.Close()
		imClient = impb.NewIMClient(imConn)
	}
	nodeID := int64(4) // 旧 API、用户 RPC、IM RPC 已分别使用 1、2、3。
	if value := os.Getenv("TASK_SNOWFLAKE_NODE_ID"); value != "" {
		nodeID, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			log.Fatal("invalid TASK_SNOWFLAKE_NODE_ID")
		}
	}
	idNode, err := snowflake.NewNode(nodeID)
	if err != nil {
		log.Fatal("invalid TASK_SNOWFLAKE_NODE_ID")
	}
	impl := &taskServer{db: db, idNode: idNode, teamClient: userpb.NewUserClient(conn), imClient: imClient}
	s := zrpc.MustNewServer(c, func(server *grpc.Server) {
		pb.RegisterTaskServer(server, impl)
	})
	defer s.Stop()
	s.Start()
}

func openTaskDatabase(dsn string) (*gorm.DB, error) {
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("invalid TASK_MYSQL_DSN")
	}
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 3*time.Second, 2*time.Second, 2*time.Second
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, errors.New("cannot connect to task database; check TASK_MYSQL_DSN and MySQL")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(5)
	sqlDB.SetMaxIdleConns(2)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	return db, nil
}
