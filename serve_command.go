package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spacelift-io/spacelift-prometheus-exporter/client/session"
	"github.com/urfave/cli/v2"
	"golang.org/x/net/context"
)

var (
	listenAddress     string
	flagListenAddress = &cli.StringFlag{
		Name:        "listen-address",
		Aliases:     []string{"l"},
		Value:       ":8080",
		Usage:       "The address to listen on for HTTP requests",
		EnvVars:     []string{"SPACELIFT_PROM_LISTEN_ADDRESS"},
		Destination: &listenAddress,
	}

	apiEndpoint     string
	flagAPIEndpoint = &cli.StringFlag{
		Name:        "api-endpoint",
		Aliases:     []string{"e"},
		Usage:       "Your spacelift API endpoint (e.g. https://myaccount.app.spacelift.io)",
		EnvVars:     []string{"SPACELIFT_PROM_API_ENDPOINT"},
		Required:    true,
		Destination: &apiEndpoint,
	}

	apiKeyID     string
	flagAPIKeyID = &cli.StringFlag{
		Name:        "api-key-id",
		Aliases:     []string{"k"},
		Usage:       "Your spacelift API key ID",
		EnvVars:     []string{"SPACELIFT_PROM_API_KEY_ID"},
		Required:    true,
		Destination: &apiKeyID,
	}

	apiKeySecret     string
	flagAPIKeySecret = &cli.StringFlag{
		Name:        "api-key-secret",
		Aliases:     []string{"s"},
		Usage:       "Your spacelift API key secret",
		EnvVars:     []string{"SPACELIFT_PROM_API_KEY_SECRET"},
		Required:    true,
		Destination: &apiKeySecret,
	}
)

var serveCommand *cli.Command = &cli.Command{
	Name:  "serve",
	Usage: "Starts the Prometheus exporter",
	Flags: []cli.Flag{
		flagListenAddress,
		flagAPIEndpoint,
		flagAPIKeyID,
		flagAPIKeySecret,
	},
	Action: func(ctx *cli.Context) error {
		session, err := func() (session.Session, error) {
			sessionCtx, cancel := context.WithTimeout(ctx.Context, time.Second*5)
			defer cancel()
			return session.New(sessionCtx, http.DefaultClient, apiEndpoint, apiKeyID, apiKeySecret)
		}()
		if err != nil {
			return errors.Wrap(err, "could not create session from Spacelift API key")
		}

		// Create a new registry.
		reg := prometheus.NewRegistry()

		// Add Go module build info.
		reg.MustRegister(collectors.NewBuildInfoCollector())
		reg.MustRegister(collectors.NewGoCollector(
			collectors.WithGoCollections(collectors.GoRuntimeMemStatsCollection | collectors.GoRuntimeMetricsCollection),
		))
		reg.MustRegister(newSpaceliftCollector(http.DefaultClient, session))

		// Expose the registered metrics via HTTP.
		http.Handle("/metrics", promhttp.HandlerFor(
			reg,
			promhttp.HandlerOpts{
				// Opt into OpenMetrics to support exemplars.
				EnableOpenMetrics: true,
			},
		))
		listenAddress := ctx.String(flagListenAddress.Name)
		fmt.Printf("Listening on %s\n", listenAddress)

		return http.ListenAndServe(listenAddress, nil)
	},
}
