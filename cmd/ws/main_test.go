package main

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestWSHTTPSecondBindFailureReleasesFirstListener(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	publicAddr := probe.Addr().String()
	_ = probe.Close()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	if err := serveWSHTTP(context.Background(), &http.Server{Addr: publicAddr}, &http.Server{Addr: occupied.Addr().String()}, nil); err == nil {
		t.Fatal("second bind failure ignored")
	}
	listener, err := net.Listen("tcp", publicAddr)
	if err != nil {
		t.Fatal("failed startup leaked public listener", err)
	}
	_ = listener.Close()
}

func TestWSHTTPShutdownWaitsForActiveHTTPBeforeReturning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	public := &http.Server{Addr: "127.0.0.1:0", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		close(finished)
		w.WriteHeader(http.StatusNoContent)
	})}
	// Use a fixed temporary address to reach the listener once startup binds it.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()
	public.Addr = addr
	done := make(chan error, 1)
	go func() { done <- serveWSHTTP(ctx, public, &http.Server{Addr: "127.0.0.1:0"}, nil) }()
	var conn net.Conn
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = conn.Write([]byte("GET / HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n"))
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("handler did not start")
	}
	cancel()
	select {
	case err := <-done:
		close(release)
		t.Fatal("returned with active request", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	select {
	case <-finished:
	default:
		t.Fatal("HTTP handler not finished")
	}
	if conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("listener remained open")
	}
}
