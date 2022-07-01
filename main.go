package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/urfave/cli/v2"
)

var version string = "local"

func main() {
	app := &cli.App{
		Name:      "spacelift-promex",
		Usage:     "Exports metrics from your Spacelift account to Prometheus",
		Commands:  []*cli.Command{serveCommand},
		Version:   version,
		Copyright: fmt.Sprintf("Copyright (c) %d spacelift-io", time.Now().Year()),
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}
