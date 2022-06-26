package main

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spacelift-io/spacelift-prometheus-exporter/client/session"
	"github.com/spacelift-io/spacelift-prometheus-exporter/logging"
	"github.com/urfave/cli/v2"
	"go.uber.org/zap"
	"golang.org/x/net/context"
)

var (
	listenAddress     string
	flagListenAddress = &cli.StringFlag{
		Name:        "listen-address",
		Aliases:     []string{"l"},
		Value:       ":9953",
		Usage:       "The address to listen on for HTTP requests",
		EnvVars:     []string{"SPACELIFT_PROMEX_LISTEN_ADDRESS"},
		Destination: &listenAddress,
	}

	apiEndpoint     string
	flagAPIEndpoint = &cli.StringFlag{
		Name:        "api-endpoint",
		Aliases:     []string{"e"},
		Usage:       "Your spacelift API endpoint (e.g. https://myaccount.app.spacelift.io)",
		EnvVars:     []string{"SPACELIFT_PROMEX_API_ENDPOINT"},
		Required:    true,
		Destination: &apiEndpoint,
	}

	apiKeyID     string
	flagAPIKeyID = &cli.StringFlag{
		Name:        "api-key-id",
		Aliases:     []string{"k"},
		Usage:       "Your spacelift API key ID",
		EnvVars:     []string{"SPACELIFT_PROMEX_API_KEY_ID"},
		Required:    true,
		Destination: &apiKeyID,
	}

	apiKeySecret     string
	flagAPIKeySecret = &cli.StringFlag{
		Name:        "api-key-secret",
		Aliases:     []string{"s"},
		Usage:       "Your spacelift API key secret",
		EnvVars:     []string{"SPACELIFT_PROMEX_API_KEY_SECRET"},
		Required:    true,
		Destination: &apiKeySecret,
	}

	isDevelopment     bool
	flagIsDevelopment = &cli.BoolFlag{
		Name:        "is-development",
		Aliases:     []string{"d"},
		Usage:       "Uses settings appropriate during local development",
		EnvVars:     []string{"SPACELIFT_PROMEX_IS_DEVELOPMENT"},
		Destination: &isDevelopment,
	}

	scrapeTimeout     time.Duration
	flagScrapeTimeout = &cli.DurationFlag{
		Name:        "scrape-timeout",
		Aliases:     []string{"t"},
		Usage:       "The maximum duration to wait for a response from the Spacelift API during scraping",
		EnvVars:     []string{"SPACELIFT_PROMEX_SCRAPE_TIMEOUT"},
		Value:       time.Second * 5,
		Destination: &scrapeTimeout,
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
		flagIsDevelopment,
		flagScrapeTimeout,
	},
	Action: func(cliCtx *cli.Context) error {
		ctx := logging.Init(cliCtx.Context, isDevelopment)
		logger := logging.FromContext(ctx).Sugar()

		if scrapeTimeout <= 0 {
			return cli.Exit("scrape-timeout must be greater than 0", ExitCodeStartupError)
		}

		session, err := func() (session.Session, error) {
			sessionCtx, cancel := context.WithTimeout(ctx, time.Second*5)
			defer cancel()
			return session.New(sessionCtx, http.DefaultClient, apiEndpoint, apiKeyID, apiKeySecret)
		}()
		if err != nil {
			logger.Fatalw("failed to create Spacelift API session", zap.Error(err))
			return cli.Exit("could not create session from Spacelift API key", ExitCodeStartupError)
		}

		// Create a new registry.
		reg := prometheus.NewRegistry()

		// Add Go module build info.
		reg.MustRegister(collectors.NewGoCollector(
			collectors.WithGoCollections(collectors.GoRuntimeMemStatsCollection | collectors.GoRuntimeMetricsCollection),
		))
		reg.MustRegister(newSpaceliftCollector(ctx, http.DefaultClient, session, scrapeTimeout))

		// Expose the registered metrics via HTTP.
		http.Handle("/metrics", promhttp.HandlerFor(
			reg,
			promhttp.HandlerOpts{
				// Opt into OpenMetrics to support exemplars.
				EnableOpenMetrics: true,
			},
		))

		http.Handle("/health", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("Countdown complete - ready to serve metrics!"))
		}))

		listenAddress := cliCtx.String(flagListenAddress.Name)

		logger.Info("Listening on ", listenAddress)

		return http.ListenAndServe(listenAddress, nil)
	},
}
