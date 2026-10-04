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
	configFile := flag.String("f", "rpc/im/etc/im.yaml", "IM RPC config file")
	flag.Parse()
	var c zrpc.RpcServerConf
	conf.MustLoad(*configFile, &c)
	botConfig, err := loadBotReplyConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	triggerConfig, err := loadIMTriggerConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	if err := validateIMTriggerStartup(triggerConfig, c.ListenOn, botConfig.ListenOn); err != nil {
		log.Fatal(err)
	}
	triggerTeamsConfig, err := loadIMTriggerTeamConfig(triggerConfig, os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	dsn, secret := os.Getenv("IM_MYSQL_DSN"), os.Getenv("IM_JWT_SECRET")
	if dsn == "" || secret == "" {
		log.Fatal("IM_MYSQL_DSN and IM_JWT_SECRET are required")
	}
	db, err := openIMDatabase(dsn)
	if err != nil {
		log.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	serverImpl := &imServer{db: db, jwtSecret: secret}
	nodeID := int64(3) // 旧 API 使用 1，用户 RPC 默认使用 2。
	if value := os.Getenv("IM_SNOWFLAKE_NODE_ID"); value != "" {
		nodeID, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			log.Fatal("invalid IM_SNOWFLAKE_NODE_ID")
		}
	}
	serverImpl.idNode, err = snowflake.NewNode(nodeID)
	if err != nil {
		log.Fatal("invalid IM_SNOWFLAKE_NODE_ID")
	}
	var ordinaryUserConn *grpc.ClientConn
	if addr := os.Getenv("USER_RPC_ADDR"); addr != "" {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Fatal(err)
		}
		defer conn.Close()
		ordinaryUserConn = conn
		serverImpl.teamClient = userpb.NewUserClient(conn)
	}

	var triggerTeams *triggerTeamClient
	var triggerRuntime *imTriggerRuntime
	var botRuntime *botReplyRuntime
	// log.Fatal skips defers: close already prepared resources on startup errors.
	failPreparedStartup := func(err error) {
		if botRuntime != nil {
			botRuntime.Stop()
		}
		triggerRuntime.Stop()
		_ = triggerTeams.Close()
		if ordinaryUserConn != nil {
			_ = ordinaryUserConn.Close()
		}
		_ = sqlDB.Close()
		log.Fatal(err)
	}
	if !triggerConfig.disabled() {
		triggerTeams, err = newTriggerTeamClient(triggerTeamsConfig)
		if err != nil {
			failPreparedStartup(err)
		}
		triggerRuntime, err = newIMTriggerRuntime(triggerConfig, db, triggerTeams)
		if err != nil {
			failPreparedStartup(err)
		}
	}
	ordinaryReady := make(chan *grpc.Server, 1)
	triggerFailed := make(chan struct{}, 2)
	s, err := zrpc.NewServer(c, func(server *grpc.Server) {
		registerOrdinaryIM(server, serverImpl)
		if triggerRuntime != nil {
			// go-zero calls register during Start, after binding the ordinary port.
			ordinaryReady <- server
			go func() {
				if err := triggerRuntime.server.Serve(triggerRuntime.listener); err != nil {
					triggerFailed <- struct{}{}
				}
			}()
			if botRuntime != nil {
				go func() {
					if err := botRuntime.server.Serve(botRuntime.listener); err != nil {
						triggerFailed <- struct{}{}
					}
				}()
			}
		}
	})
	if err != nil {
		failPreparedStartup(err)
	}
	defer s.Stop()
	defer triggerTeams.Close()
	defer triggerRuntime.Stop()
	botRuntime, err = newBotReplyRuntime(botConfig, serverImpl)
	if err != nil {
		failPreparedStartup(err)
	}
	if botRuntime != nil {
		defer botRuntime.Stop()
	}
	if triggerRuntime != nil {
		if waitIMTriggerServers(s.Start, ordinaryReady, triggerFailed) {
			log.Print("IM service listener stopped unexpectedly; stopping IM RPC")
		}
		return
	}
	if botRuntime != nil {
		go func() {
			if err := botRuntime.server.Serve(botRuntime.listener); err != nil {
				log.Printf("IM bot listener stopped: %v", err)
				s.Stop()
			}
		}()
	}
	s.Start()
}

func openIMDatabase(dsn string) (*gorm.DB, error) {
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("invalid IM_MYSQL_DSN")
	}
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 3*time.Second, 2*time.Second, 2*time.Second
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, errors.New("cannot connect to IM database; check IM_MYSQL_DSN and MySQL")
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
