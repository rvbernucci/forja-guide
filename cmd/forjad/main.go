package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rvbernucci/forja-guide/internal/config"
	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/daemon"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/logging"
	"github.com/rvbernucci/forja-guide/internal/postgres"
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
	var repository runstate.Repository = runstate.NewStore(nil)
	var closeDatabase func()
	if cfg.DatabaseURL != "" {
		connectContext, cancelConnect := context.WithTimeout(
			context.Background(),
			15*time.Second,
		)
		defer cancelConnect()
		pool, openErr := postgres.Open(
			connectContext,
			cfg.DatabaseURL,
			int32(cfg.DatabaseMaxConn),
		)
		if openErr != nil {
			return fmt.Errorf("open PostgreSQL: %w", openErr)
		}
		closeDatabase = pool.Close
		defer closeDatabase()
		if cfg.DatabaseMigrate {
			if migrateErr := postgres.Migrate(connectContext, pool); migrateErr != nil {
				return fmt.Errorf("migrate PostgreSQL: %w", migrateErr)
			}
		}
		durable, storeErr := postgres.NewStore(
			pool,
			nil,
			postgres.DefaultTenantID,
			postgres.DefaultRepositoryID,
		)
		if storeErr != nil {
			return fmt.Errorf("initialize PostgreSQL store: %w", storeErr)
		}
		repository = durable
		logger.Info("durable PostgreSQL state enabled")
	} else {
		logger.Warn("using ephemeral in-memory state", "hint", "set FORJA_DATABASE_URL")
	}
	server, err := daemon.New(
		repository,
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
