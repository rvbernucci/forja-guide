package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rvbernucci/forja-guide/internal/alpha"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "seed-identities" {
		if err := seedIdentities(os.Args[2:]); err != nil {
			log.Fatalf("forja-alpha seed-identities: %v", err)
		}
		return
	}
	config, err := alpha.LoadConfig()
	if err != nil {
		log.Fatalf("forja-alpha configuration: %v", err)
	}
	handler, err := alpha.NewHandler(alpha.NewService(config))
	if err != nil {
		log.Fatalf("forja-alpha web application: %v", err)
	}
	server := &http.Server{
		Addr:              config.Address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			log.Printf("forja-alpha shutdown: %v", err)
		}
	}()

	log.Printf("forja-alpha listening on http://%s (local-only core inference policy enabled)", config.Address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("forja-alpha serve: %v", err)
	}
}

func seedIdentities(arguments []string) error {
	flags := flag.NewFlagSet("forja-alpha seed-identities", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	tenantID := flags.String("tenant-id", "", "target tenant UUID")
	repositoryID := flags.String("repository-id", "", "target repository UUID")
	companyTickersPath := flags.String("company-tickers-json", "", "optional local SEC company_tickers.json snapshot")
	availableAtRaw := flags.String("available-at", "", "snapshot availability timestamp in RFC3339 format; required with --company-tickers-json")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *companyTickersPath == "" {
		if *availableAtRaw != "" {
			return errors.New("--available-at requires --company-tickers-json")
		}
		return alpha.WriteSECIdentitySeedSQL(os.Stdout, *tenantID, *repositoryID)
	}
	if *availableAtRaw == "" {
		return errors.New("--company-tickers-json requires --available-at")
	}
	availableAt, err := time.Parse(time.RFC3339, *availableAtRaw)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(*companyTickersPath)
	if err != nil {
		return err
	}
	snapshot, err := alpha.ParseSECCompanyTickersSnapshot(content, availableAt)
	if err != nil {
		return err
	}
	return alpha.WriteSECIdentitySeedSQLWithSnapshot(os.Stdout, *tenantID, *repositoryID, &snapshot)
}
