package rpcauth

import (
	"github.com/zeromicro/go-zero/zrpc"
	"google.golang.org/grpc"
)

// WithoutRPCRequestContent keeps RPC statistics and slow-call timing while
// preventing go-zero's stat interceptor from serializing business requests.
// Deriving names from descriptors also covers newly generated unary methods.
func WithoutRPCRequestContent(c zrpc.RpcServerConf, services ...*grpc.ServiceDesc) zrpc.RpcServerConf {
	ignored := append([]string(nil), c.Middlewares.StatConf.IgnoreContentMethods...)
	seen := make(map[string]bool, len(ignored))
	for _, method := range ignored {
		seen[method] = true
	}
	for _, service := range services {
		if service == nil {
			continue
		}
		for _, method := range service.Methods {
			name := "/" + service.ServiceName + "/" + method.MethodName
			if !seen[name] {
				ignored = append(ignored, name)
				seen[name] = true
			}
		}
	}
	c.Middlewares.StatConf.IgnoreContentMethods = ignored
	return c
}
