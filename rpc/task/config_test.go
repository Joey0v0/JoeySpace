package main

import (
	"testing"

	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/zrpc"
)

func TestTaskRPCConfigLoadsLocalAndContainerAddresses(t *testing.T) {
	for _, tc := range []struct{ file, address string }{
		{"etc/task.yaml", "127.0.0.1:9003"},
		{"../../deploy/task-rpc.yaml", "0.0.0.0:9003"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			var c zrpc.RpcServerConf
			if err := conf.Load(tc.file, &c); err != nil {
				t.Fatal(err)
			}
			if c.ListenOn != tc.address {
				t.Fatalf("listen address = %q, want %q", c.ListenOn, tc.address)
			}
		})
	}
}
