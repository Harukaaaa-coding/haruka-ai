package main

import (
	"GopherAI/common/health"
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestRunHTTPServerDrainsActiveRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte("done"))
	})}
	checker := health.NewChecker(time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() { serverDone <- runHTTPServer(ctx, server, listener, checker, time.Second) }()

	responseDone := make(chan error, 1)
	go func() {
		response, requestErr := http.Get("http://" + listener.Addr().String())
		if requestErr == nil {
			_, requestErr = io.ReadAll(response.Body)
			_ = response.Body.Close()
		}
		responseDone <- requestErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not reach the handler")
	}
	cancel()
	select {
	case err := <-serverDone:
		t.Fatalf("server stopped before the active request drained: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-responseDone; err != nil {
		t.Fatalf("active request failed during graceful drain: %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("graceful shutdown failed: %v", err)
	}
	if checker.Accepting() {
		t.Fatal("checker remained ready after shutdown")
	}
}

func TestRunHTTPServerForceClosesAfterDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release
	})}
	checker := health.NewChecker(time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() { serverDone <- runHTTPServer(ctx, server, listener, checker, 30*time.Millisecond) }()
	clientDone := make(chan struct{})
	go func() {
		response, _ := http.Get("http://" + listener.Addr().String())
		if response != nil {
			_ = response.Body.Close()
		}
		close(clientDone)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not reach the handler")
	}
	cancel()
	select {
	case err := <-serverDone:
		if err == nil {
			t.Fatal("forced shutdown unexpectedly returned nil")
		}
	case <-time.After(time.Second):
		t.Fatal("forced shutdown exceeded its deadline")
	}
	close(release)
	select {
	case <-clientDone:
	case <-time.After(time.Second):
		t.Fatal("forced-close client did not exit")
	}
}
