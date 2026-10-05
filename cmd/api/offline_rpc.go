package main

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/yjydist/go-im/internal/handler"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// One lazy connection serves both offline List and Ack for the API process.
func newOfflineMessagesClient() (handler.OfflineMessagesClient, *grpc.ClientConn, error) {
	addr, err := offlineRPCAddr(os.Getenv("IM_RPC_ADDR"))
	if err != nil {
		return nil, nil, err
	}
	// gRPC parses its target as a URL; preserve a scoped IPv6 '%' through that parse.
	target := strings.ReplaceAll(addr, "%", "%25")
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, errors.New("cannot initialize IM RPC client")
	}
	return impb.NewIMClient(conn), conn, nil
}

func offlineRPCAddr(addr string) (string, error) {
	if addr == "" {
		return "localhost:9002", nil
	}
	invalid := errors.New("invalid IM_RPC_ADDR: expected host:port with port 1-65535")
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || strings.ContainsAny(addr, "/\\?#@") || strings.IndexFunc(addr, unicode.IsSpace) >= 0 {
		return "", invalid
	}
	if strings.Contains(host, ":") {
		if ip, err := netip.ParseAddr(host); err != nil || !ip.Is6() {
			return "", invalid
		}
	} else {
		for _, char := range host {
			if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '.' || char == '-' || char == '_') {
				return "", invalid
			}
		}
		if strings.ContainsAny(addr, "[]") {
			return "", invalid
		}
	}
	for _, char := range port {
		if char < '0' || char > '9' {
			return "", invalid
		}
	}
	if number, err := strconv.ParseUint(port, 10, 16); err != nil || number == 0 {
		return "", invalid
	}
	return addr, nil
}
