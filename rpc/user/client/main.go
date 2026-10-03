// 本地验证客户端：代替未来的 API，向独立的用户服务发起 RPC 请求。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/zrpc"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9001", "user RPC address")
	userID := flag.Int64("id", 1, "demo user ID")
	flag.Parse()

	if err := run(*addr, *userID); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(addr string, userID int64) error {
	client, err := zrpc.NewClient(zrpc.RpcClientConf{
		Endpoints: []string{addr},
		NonBlock:  true,
		Timeout:   2000,
	})
	if err != nil {
		return fmt.Errorf("create RPC client: %w", err)
	}
	defer client.Conn().Close()

	// 网络调用必须限定等待时间，避免服务不可用时一直等待。
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := pb.NewUserClient(client.Conn()).GetUserInfo(ctx, &pb.GetUserInfoRequest{UserId: userID})
	if err != nil {
		return fmt.Errorf("GetUserInfo failed (%s): %s", status.Code(err), status.Convert(err).Message())
	}

	data, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(result)
	if err != nil {
		return fmt.Errorf("encode response: %w", err)
	}
	fmt.Println(string(data))
	return nil
}
