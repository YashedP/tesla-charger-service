package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestSupervisorStopsWhenWorkerFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	server := &http.Server{Addr: "127.0.0.1:0", ReadHeaderTimeout: time.Second}
	err := run(ctx, server, func(context.Context) error { return errors.New("test worker failure") })
	if err == nil {
		t.Fatal("worker failure did not fail service")
	}
}

func TestSupervisorGracefulCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	server := &http.Server{Addr: "127.0.0.1:0", ReadHeaderTimeout: time.Second}
	err := run(ctx, server, func(ctx context.Context) error { cancel(); <-ctx.Done(); return ctx.Err() })
	if err != nil {
		t.Fatalf("normal shutdown failed: %v", err)
	}
}
