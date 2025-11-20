package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"
)

var version string = "local"
var commit string = "devel"

func main() {
	app := &cli.Command{
		Name:      "spacelift-promex",
		Usage:     "Exports metrics from your Spacelift account to Prometheus",
		Commands:  []*cli.Command{serveCommand},
		Version:   fmt.Sprintf("%s - %s", version, commit),
		Copyright: fmt.Sprintf("Copyright (c) %d spacelift-io", time.Now().Year()),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, os.Args); err != nil {
		log.Panic(err)
	}
}
