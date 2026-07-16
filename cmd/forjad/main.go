package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/rvbernucci/forja-guide/internal/config"
	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/daemon"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/logging"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "forjad: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Args[1:], os.LookupEnv)
	if err != nil {
		return err
	}
	logger := logging.New(os.Stdout, cfg.LogLevel)
	registry, err := contracts.NewRegistry()
	if err != nil {
		return fmt.Errorf("initialize contract registry: %w", err)
	}
	server, err := daemon.New(
		runstate.NewStore(nil),
		registry,
		identity.NewRunID,
		logger,
	)
	if err != nil {
		return fmt.Errorf("initialize daemon: %w", err)
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}
	defer listener.Close()

	logger.Info(
		"forjad ready",
		"listen",
		listener.Addr().String(),
		"environment",
		cfg.Environment,
	)
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	return daemon.ListenAndServe(
		ctx,
		listener,
		server.Handler(),
		cfg.ShutdownTimeout,
		logger,
	)
}
