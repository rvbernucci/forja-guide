package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rvbernucci/forja-guide/internal/alpha"
)

func main() {
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
