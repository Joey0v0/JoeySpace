package main

import (
	"testing"

	"google.golang.org/grpc/connectivity"
)

func TestOfflineRPCAddr(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{name: "empty uses local default", want: "localhost:9002"},
		{name: "container", addr: "im-rpc:9002", want: "im-rpc:9002"},
		{name: "IPv4", addr: "127.0.0.1:9002", want: "127.0.0.1:9002"},
		{name: "IPv6", addr: "[::1]:9002", want: "[::1]:9002"},
		{name: "IPv6 zone", addr: "[fe80::1%eth0]:9002", want: "[fe80::1%eth0]:9002"},
		{name: "smallest port", addr: "localhost:1", want: "localhost:1"},
		{name: "largest port", addr: "localhost:65535", want: "localhost:65535"},
		{name: "missing host", addr: ":9002"},
		{name: "missing port", addr: "localhost"},
		{name: "empty port", addr: "localhost:"},
		{name: "service name port", addr: "localhost:http"},
		{name: "zero port", addr: "localhost:0"},
		{name: "negative port", addr: "localhost:-1"},
		{name: "signed port", addr: "localhost:+9002"},
		{name: "port overflow", addr: "localhost:65536"},
		{name: "URL", addr: "http://localhost:9002"},
		{name: "resolver target", addr: "dns:///localhost:9002"},
		{name: "path suffix", addr: "localhost:9002/path"},
		{name: "credentials", addr: "user@localhost:9002"},
		{name: "whitespace", addr: " localhost:9002"},
		{name: "internal whitespace", addr: "local host:9002"},
		{name: "unbracketed IPv6", addr: "::1:9002"},
		{name: "bad IPv6", addr: "[::invalid]:9002"},
		{name: "bracketed name", addr: "[localhost]:9002"},
		{name: "invalid host character", addr: "local$host:9002"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := offlineRPCAddr(test.addr)
			if test.want == "" {
				if err == nil || got != "" {
					t.Fatalf("invalid address accepted: got %q, error %v", got, err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("got %q, error %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestOfflineMessagesClientEnvironmentAndLazyConnection(t *testing.T) {
	for _, test := range []struct {
		name string
		env  string
		want string
	}{
		{name: "default", want: "localhost:9002"},
		{name: "explicit unavailable address", env: "127.0.0.1:1", want: "127.0.0.1:1"},
		{name: "container override", env: "im-rpc:9002", want: "im-rpc:9002"},
		{name: "IPv6 override", env: "[::1]:9002", want: "[::1]:9002"},
		{name: "scoped IPv6 override", env: "[fe80::1%eth0]:9002", want: "[fe80::1%25eth0]:9002"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("IM_RPC_ADDR", test.env)
			client, conn, err := newOfflineMessagesClient()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			if client == nil || conn.Target() != test.want {
				t.Fatalf("client %v, target %q; want %q", client, conn.Target(), test.want)
			}
			// Construction must not dial, resolve the container name, or claim readiness.
			if state := conn.GetState(); state != connectivity.Idle {
				t.Fatalf("connection eagerly started: %v", state)
			}
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
			if state := conn.GetState(); state != connectivity.Shutdown {
				t.Fatalf("connection was not released: %v", state)
			}
		})
	}
}

func TestOfflineMessagesClientRejectsInvalidEnvironment(t *testing.T) {
	t.Setenv("IM_RPC_ADDR", "http://localhost:9002")
	client, conn, err := newOfflineMessagesClient()
	if err == nil || client != nil || conn != nil {
		t.Fatalf("invalid environment produced a client: client %v, connection %v, error %v", client, conn, err)
	}
}
