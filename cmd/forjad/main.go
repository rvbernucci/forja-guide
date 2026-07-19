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
	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/daemon"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/logging"
	"github.com/rvbernucci/forja-guide/internal/observability"
	"github.com/rvbernucci/forja-guide/internal/postgres"
	"github.com/rvbernucci/forja-guide/internal/runstate"
	"github.com/rvbernucci/forja-guide/internal/version"
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
	if err := config.ValidateDaemonListen(cfg.Listen); err != nil {
		return err
	}
	logger := logging.New(os.Stdout, cfg.LogLevel)
	telemetryConfig, err := observability.RuntimeConfigFromEnv(
		"forjad", version.Version, cfg.Environment, os.LookupEnv,
	)
	if err != nil {
		return err
	}
	telemetry, err := observability.NewRuntime(context.Background(), telemetryConfig, logger)
	if err != nil {
		return fmt.Errorf("initialize observability: %w", err)
	}
	defer shutdownTelemetry(telemetry, logger)
	authenticator, authority, err := httpTrustBoundary(os.Getenv)
	if err != nil {
		return err
	}
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
		pool, openErr := postgres.Open(
			connectContext,
			cfg.DatabaseURL,
			int32(cfg.DatabaseMaxConn),
			observability.NewPGXTracer(telemetry.Observer),
		)
		cancelConnect()
		if openErr != nil {
			return fmt.Errorf("open PostgreSQL: %w", openErr)
		}
		closeDatabase = pool.Close
		defer closeDatabase()
		if cfg.DatabaseMigrate {
			migrationContext, cancelMigration := context.WithTimeout(
				context.Background(),
				15*time.Second,
			)
			migrateErr := postgres.Migrate(migrationContext, pool)
			cancelMigration()
			if migrateErr != nil {
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
		operationalReader, readerErr := observability.NewPostgresOperationalReader(
			pool,
			postgres.DefaultTenantID,
			postgres.DefaultRepositoryID,
		)
		if readerErr != nil {
			return fmt.Errorf("initialize operational state reader: %w", readerErr)
		}
		operationalCollector, collectorErr := observability.NewOperationalCollector(
			operationalReader,
			observability.DefaultOperationalThresholds(),
		)
		if collectorErr != nil {
			return fmt.Errorf("initialize operational state collector: %w", collectorErr)
		}
		if registerErr := telemetry.RegisterCollector(operationalCollector); registerErr != nil {
			return registerErr
		}
		repository = durable
		logger.Info("durable PostgreSQL state enabled")
	} else {
		logger.Warn("using ephemeral in-memory state", "hint", "set FORJA_DATABASE_URL")
	}
	server, err := daemon.New(
		repository,
		registry,
		authenticator,
		authority,
		identity.NewRunID,
		logger,
		telemetry,
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

func shutdownTelemetry(runtime *observability.Runtime, logger interface {
	Warn(string, ...any)
}) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runtime.Shutdown(ctx); err != nil {
		logger.Warn(
			"telemetry shutdown incomplete",
			"failure_class",
			observability.FailureUnavailable,
		)
	}
}

func httpTrustBoundary(
	lookup func(string) string,
) (daemon.Authenticator, control.Authority, error) {
	authority := control.Authority{
		TenantID:     control.LocalTenantID,
		RepositoryID: control.LocalRepositoryID,
	}
	actorType := lookup("FORJA_HTTP_ACTOR_TYPE")
	if actorType == "" {
		actorType = "human"
	}
	principal, err := control.NewScopedPrincipal(
		actorType,
		lookup("FORJA_HTTP_ACTOR_ID"),
		authority.TenantID,
		authority.RepositoryID,
		control.AllPermissions...,
	)
	if err != nil {
		return nil, control.Authority{}, fmt.Errorf("configure HTTP principal: %w", err)
	}
	authenticator, err := daemon.NewStaticBearerAuthenticator(
		lookup("FORJA_HTTP_BEARER_TOKEN"),
		principal,
	)
	if err != nil {
		return nil, control.Authority{}, fmt.Errorf("configure HTTP authentication: %w", err)
	}
	return authenticator, authority, nil
}
