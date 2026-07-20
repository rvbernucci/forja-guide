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
	if len(os.Args) > 1 && os.Args[1] == "seed-submissions" {
		if err := seedSubmissions(os.Args[2:]); err != nil {
			log.Fatalf("forja-alpha seed-submissions: %v", err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "seed-company-facts" {
		if err := seedCompanyFacts(os.Args[2:]); err != nil {
			log.Fatalf("forja-alpha seed-company-facts: %v", err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "seed-metrics" {
		if err := seedMetrics(os.Args[2:]); err != nil {
			log.Fatalf("forja-alpha seed-metrics: %v", err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "seed-metric-observations" {
		if err := seedMetricObservations(os.Args[2:]); err != nil {
			log.Fatalf("forja-alpha seed-metric-observations: %v", err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "seed-treasury-series" {
		if err := seedTreasurySeries(os.Args[2:]); err != nil {
			log.Fatalf("forja-alpha seed-treasury-series: %v", err)
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

func seedSubmissions(arguments []string) error {
	flags := flag.NewFlagSet("forja-alpha seed-submissions", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	tenantID := flags.String("tenant-id", "", "target tenant UUID")
	repositoryID := flags.String("repository-id", "", "target repository UUID")
	ticker := flags.String("ticker", "", "covered ticker symbol")
	submissionsPath := flags.String("submissions-json", "", "local SEC submissions/CIK##########.json snapshot")
	availableAtRaw := flags.String("available-at", "", "snapshot availability timestamp in RFC3339 format")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *ticker == "" {
		return errors.New("--ticker is required")
	}
	if *submissionsPath == "" {
		return errors.New("--submissions-json is required")
	}
	if *availableAtRaw == "" {
		return errors.New("--available-at is required")
	}
	company, ok := alpha.ResolveSECCompany(*ticker)
	if !ok {
		return errors.New("--ticker is outside the bounded Alpha universe")
	}
	availableAt, err := time.Parse(time.RFC3339, *availableAtRaw)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(*submissionsPath)
	if err != nil {
		return err
	}
	snapshot, err := alpha.ParseSECSubmissionsSnapshot(content, company, availableAt)
	if err != nil {
		return err
	}
	return alpha.WriteSECSubmissionsSeedSQL(os.Stdout, *tenantID, *repositoryID, snapshot)
}

func seedCompanyFacts(arguments []string) error {
	flags := flag.NewFlagSet("forja-alpha seed-company-facts", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	tenantID := flags.String("tenant-id", "", "target tenant UUID")
	repositoryID := flags.String("repository-id", "", "target repository UUID")
	ticker := flags.String("ticker", "", "covered ticker symbol")
	companyFactsPath := flags.String("company-facts-json", "", "local SEC companyfacts/CIK##########.json snapshot")
	availableAtRaw := flags.String("available-at", "", "snapshot availability timestamp in RFC3339 format")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *ticker == "" {
		return errors.New("--ticker is required")
	}
	if *companyFactsPath == "" {
		return errors.New("--company-facts-json is required")
	}
	if *availableAtRaw == "" {
		return errors.New("--available-at is required")
	}
	company, ok := alpha.ResolveSECCompany(*ticker)
	if !ok {
		return errors.New("--ticker is outside the bounded Alpha universe")
	}
	availableAt, err := time.Parse(time.RFC3339, *availableAtRaw)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(*companyFactsPath)
	if err != nil {
		return err
	}
	snapshot, err := alpha.ParseSECCompanyFactsSnapshot(content, company, availableAt)
	if err != nil {
		return err
	}
	return alpha.WriteSECCompanyFactsSeedSQL(os.Stdout, *tenantID, *repositoryID, snapshot)
}

func seedMetrics(arguments []string) error {
	flags := flag.NewFlagSet("forja-alpha seed-metrics", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	tenantID := flags.String("tenant-id", "", "target tenant UUID")
	repositoryID := flags.String("repository-id", "", "target repository UUID")
	ticker := flags.String("ticker", "", "covered ticker symbol for issuer-scoped reviewed mappings")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *ticker == "" {
		return errors.New("--ticker is required")
	}
	company, ok := alpha.ResolveSECCompany(*ticker)
	if !ok {
		return errors.New("--ticker is outside the bounded Alpha universe")
	}
	return alpha.WriteAlphaMetricRegistrySeedSQL(os.Stdout, *tenantID, *repositoryID, company.IssuerID)
}

func seedMetricObservations(arguments []string) error {
	flags := flag.NewFlagSet("forja-alpha seed-metric-observations", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	tenantID := flags.String("tenant-id", "", "target tenant UUID")
	repositoryID := flags.String("repository-id", "", "target repository UUID")
	ticker := flags.String("ticker", "", "covered ticker symbol")
	companyFactsPath := flags.String("company-facts-json", "", "local SEC companyfacts/CIK##########.json snapshot")
	availableAtRaw := flags.String("available-at", "", "snapshot availability timestamp in RFC3339 format")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *ticker == "" {
		return errors.New("--ticker is required")
	}
	if *companyFactsPath == "" {
		return errors.New("--company-facts-json is required")
	}
	if *availableAtRaw == "" {
		return errors.New("--available-at is required")
	}
	company, ok := alpha.ResolveSECCompany(*ticker)
	if !ok {
		return errors.New("--ticker is outside the bounded Alpha universe")
	}
	availableAt, err := time.Parse(time.RFC3339, *availableAtRaw)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(*companyFactsPath)
	if err != nil {
		return err
	}
	snapshot, err := alpha.ParseSECCompanyFactsSnapshot(content, company, availableAt)
	if err != nil {
		return err
	}
	return alpha.WriteAlphaMetricObservationsSeedSQL(os.Stdout, *tenantID, *repositoryID, snapshot)
}

func seedTreasurySeries(arguments []string) error {
	flags := flag.NewFlagSet("forja-alpha seed-treasury-series", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	tenantID := flags.String("tenant-id", "", "target tenant UUID")
	repositoryID := flags.String("repository-id", "", "target repository UUID")
	csvPath := flags.String("csv", "", "local hash-pinned Treasury CSV snapshot")
	availableAtRaw := flags.String("available-at", "", "snapshot availability timestamp in RFC3339 format")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *csvPath == "" {
		return errors.New("--csv is required")
	}
	if *availableAtRaw == "" {
		return errors.New("--available-at is required")
	}
	availableAt, err := time.Parse(time.RFC3339, *availableAtRaw)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(*csvPath)
	if err != nil {
		return err
	}
	snapshot, err := alpha.ParseTreasurySeriesCSVSnapshot(content, alpha.DefaultTreasuryRealYield10YSeries(), availableAt)
	if err != nil {
		return err
	}
	return alpha.WriteTreasurySeriesSeedSQL(os.Stdout, *tenantID, *repositoryID, snapshot)
}
