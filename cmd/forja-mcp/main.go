package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rvbernucci/forja-guide/internal/config"
	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/mcpserver"
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
	var repository control.Repository = control.NewMemoryRepository(nil)
	if cfg.DatabaseURL != "" {
		connectContext, cancelConnect := context.WithTimeout(context.Background(), 15*time.Second)
		pool, openErr := postgres.Open(connectContext, cfg.DatabaseURL, int32(cfg.DatabaseMaxConn))
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
		_, _ = fmt.Fprintln(os.Stderr, "forja-mcp: using ephemeral state; set FORJA_DATABASE_URL for durability")
	}
	service, err := control.NewService(repository)
	if err != nil {
		return err
	}
	adapter, err := mcpserver.New(
		service,
		mcpserver.FixedPrincipalResolver{Principal: principal},
		version.Version,
	)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return normalizeServerError(ctx, adapter.Server().Run(ctx, &mcp.StdioTransport{}))
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
