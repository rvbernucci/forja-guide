package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/rvbernucci/forja-guide/internal/cli"
)

func main() {
	timeout, err := cli.ParseTimeout(os.Getenv("FORJA_TIMEOUT"))
	if err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	os.Exit(cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
