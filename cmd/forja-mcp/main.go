package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rvbernucci/forja-guide/internal/config"
	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/logging"
	"github.com/rvbernucci/forja-guide/internal/mcpserver"
	"github.com/rvbernucci/forja-guide/internal/observability"
	"github.com/rvbernucci/forja-guide/internal/postgres"
	"github.com/rvbernucci/forja-guide/internal/version"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "forja-mcp: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	actorID := strings.TrimSpace(os.Getenv("FORJA_MCP_ACTOR_ID"))
	if actorID == "" {
		return fmt.Errorf("FORJA_MCP_ACTOR_ID is required for authenticated stdio")
	}
	actorType := strings.TrimSpace(os.Getenv("FORJA_MCP_ACTOR_TYPE"))
	if actorType == "" {
		actorType = "agent"
	}
	principal, err := control.NewPrincipal(actorType, actorID, stdioPermissions(actorType)...)
	if err != nil {
		return fmt.Errorf("configure MCP principal: %w", err)
	}
	cfg, err := config.Load(nil, os.LookupEnv)
	if err != nil {
		return fmt.Errorf("load runtime configuration: %w", err)
	}
	logger := logging.New(os.Stderr, cfg.LogLevel)
	telemetryConfig, err := observability.RuntimeConfigFromEnv(
		"forja-mcp", version.Version, cfg.Environment, os.LookupEnv,
	)
	if err != nil {
		return err
	}
	telemetry, err := observability.NewRuntime(context.Background(), telemetryConfig, logger)
	if err != nil {
		return fmt.Errorf("initialize observability: %w", err)
	}
	defer shutdownTelemetry(telemetry, logger)
	metricsListen, err := mcpMetricsListen(os.LookupEnv)
	if err != nil {
		return err
	}
	metricsEndpoint, metricsErr := startMCPMetricsEndpoint(
		metricsListen, telemetry.MetricsHandler(), logger,
	)
	if metricsErr != nil {
		logger.Warn(
			"MCP metrics endpoint unavailable",
			"failure_class", observability.FailureUnavailable,
		)
	} else if metricsEndpoint != nil {
		defer metricsEndpoint.shutdown(logger)
	}
	var repository control.Repository = control.NewMemoryRepository(nil)
	if cfg.DatabaseURL != "" {
		connectContext, cancelConnect := context.WithTimeout(context.Background(), 15*time.Second)
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
		defer pool.Close()
		if cfg.DatabaseMigrate {
			migrationContext, cancelMigration := context.WithTimeout(context.Background(), 15*time.Second)
			migrationErr := postgres.Migrate(migrationContext, pool)
			cancelMigration()
			if migrationErr != nil {
				return fmt.Errorf("migrate PostgreSQL: %w", migrationErr)
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
		readyContext, cancelReady := context.WithTimeout(context.Background(), 15*time.Second)
		readyErr := durable.Ready(readyContext)
		cancelReady()
		if readyErr != nil {
			return fmt.Errorf("verify PostgreSQL readiness: %w", readyErr)
		}
		repository = durable
	} else {
		logger.Warn("using ephemeral state", "hint", "set FORJA_DATABASE_URL")
	}
	service, err := control.NewService(repository)
	if err != nil {
		return err
	}
	adapter, err := mcpserver.New(
		service,
		mcpserver.FixedPrincipalResolver{Principal: principal},
		version.Version,
		telemetry.Observer,
	)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return normalizeServerError(ctx, adapter.Server().Run(ctx, &mcp.StdioTransport{}))
}

const defaultMCPMetricsListen = "127.0.0.1:9464"

type mcpMetricsEndpoint struct {
	server   *http.Server
	listener net.Listener
}

func mcpMetricsListen(lookup func(string) (string, bool)) (string, error) {
	listen := defaultMCPMetricsListen
	if value, ok := lookup("FORJA_MCP_METRICS_LISTEN"); ok {
		listen = strings.TrimSpace(value)
		if strings.EqualFold(listen, "off") {
			return "", nil
		}
	}
	if listen == "" {
		return "", fmt.Errorf("FORJA_MCP_METRICS_LISTEN cannot be empty; use off to disable it")
	}
	if err := config.ValidateDaemonListen(listen); err != nil {
		return "", fmt.Errorf("validate FORJA_MCP_METRICS_LISTEN: %w", err)
	}
	return listen, nil
}

func startMCPMetricsEndpoint(
	listen string,
	handler http.Handler,
	logger *slog.Logger,
) (*mcpMetricsEndpoint, error) {
	if listen == "" {
		return nil, nil
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, err
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 2 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Warn(
				"MCP metrics server stopped",
				"failure_class", observability.FailureUnavailable,
			)
		}
	}()
	return &mcpMetricsEndpoint{server: server, listener: listener}, nil
}

func (endpoint *mcpMetricsEndpoint) shutdown(logger interface {
	Warn(string, ...any)
}) {
	if endpoint == nil || endpoint.server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := endpoint.server.Shutdown(ctx); err != nil {
		logger.Warn(
			"MCP metrics shutdown incomplete",
			"failure_class", observability.FailureUnavailable,
		)
	}
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

func stdioPermissions(actorType string) []control.Permission {
	switch strings.TrimSpace(actorType) {
	case "agent":
		return []control.Permission{
			control.PermissionPlan,
			control.PermissionRead,
			control.PermissionSubmit,
			control.PermissionCancel,
		}
	case "worker":
		return []control.Permission{control.PermissionRead}
	case "human", "system":
		return append([]control.Permission(nil), control.AllPermissions...)
	default:
		return nil
	}
}

func normalizeServerError(ctx context.Context, err error) error {
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
