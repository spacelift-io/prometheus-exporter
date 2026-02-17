package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/urfave/cli/v3"
	"go.uber.org/zap"

	"github.com/spacelift-io/prometheus-exporter/client/session"
	"github.com/spacelift-io/prometheus-exporter/logging"
)

var (
	listenAddress     string
	flagListenAddress = &cli.StringFlag{
		Name:        "listen-address",
		Aliases:     []string{"l"},
		Value:       ":9953",
		Usage:       "The address to listen on for HTTP requests",
		Sources:     cli.EnvVars("SPACELIFT_PROMEX_LISTEN_ADDRESS"),
		Destination: &listenAddress,
	}

	apiEndpoint     string
	flagAPIEndpoint = &cli.StringFlag{
		Name:        "api-endpoint",
		Aliases:     []string{"e"},
		Usage:       "Your spacelift API endpoint (e.g. https://myaccount.app.spacelift.io)",
		Sources:     cli.EnvVars("SPACELIFT_PROMEX_API_ENDPOINT"),
		Required:    true,
		Destination: &apiEndpoint,
	}

	caCertPath     string
	flagCACertPath = &cli.StringFlag{
		Name:        "ca-cert-path",
		Usage:       "Path to a PEM-encoded CA certificate to trust in addition to system certificates",
		Sources:     cli.EnvVars("SPACELIFT_PROMEX_CA_CERT_PATH"),
		Destination: &caCertPath,
	}

	apiKeyID     string
	flagAPIKeyID = &cli.StringFlag{
		Name:        "api-key-id",
		Aliases:     []string{"k"},
		Usage:       "Your spacelift API key ID",
		Sources:     cli.EnvVars("SPACELIFT_PROMEX_API_KEY_ID"),
		Required:    true,
		Destination: &apiKeyID,
	}

	apiKeySecret     string
	flagAPIKeySecret = &cli.StringFlag{
		Name:        "api-key-secret",
		Aliases:     []string{"s"},
		Usage:       "Your spacelift API key secret",
		Sources:     cli.EnvVars("SPACELIFT_PROMEX_API_KEY_SECRET"),
		Required:    true,
		Destination: &apiKeySecret,
	}

	isDevelopment     bool
	flagIsDevelopment = &cli.BoolFlag{
		Name:        "is-development",
		Aliases:     []string{"d"},
		Usage:       "Uses settings appropriate during local development",
		Sources:     cli.EnvVars("SPACELIFT_PROMEX_IS_DEVELOPMENT"),
		Destination: &isDevelopment,
	}

	scrapeTimeout     time.Duration
	flagScrapeTimeout = &cli.DurationFlag{
		Name:        "scrape-timeout",
		Aliases:     []string{"t"},
		Usage:       "The maximum duration to wait for a response from the Spacelift API during scraping",
		Sources:     cli.EnvVars("SPACELIFT_PROMEX_SCRAPE_TIMEOUT"),
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
		flagCACertPath,
		flagAPIKeyID,
		flagAPIKeySecret,
		flagIsDevelopment,
		flagScrapeTimeout,
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		ctx = logging.Init(ctx, isDevelopment)
		logger := logging.FromContext(ctx).Sugar()

		if scrapeTimeout <= 0 {
			return cli.Exit("scrape-timeout must be greater than 0", ExitCodeStartupError)
		}

		if url, err := url.Parse(apiEndpoint); err != nil || url.Scheme == "" || url.Host == "" {
			return cli.Exit(fmt.Sprintf("api-endpoint %q does not seem to be a valid URL", apiEndpoint), ExitCodeStartupError)
		}

		httpClient, err := newHTTPClient(caCertPath)
		if err != nil {
			return cli.Exit(fmt.Sprintf("could not configure HTTP client: %v", err), ExitCodeStartupError)
		}

		logger.Info("Prepping exporter for lift-off")

		session, err := func() (session.Session, error) {
			sessionCtx, cancel := context.WithTimeout(ctx, time.Second*5)
			defer cancel()
			return session.New(sessionCtx, httpClient, apiEndpoint, apiKeyID, apiKeySecret)
		}()
		if err != nil {
			logger.Fatalw("failed to create Spacelift API session", zap.Error(err))
			return cli.Exit("could not create session from Spacelift API key", ExitCodeStartupError)
		}

		logger.Info("Successfully created Spacelift API session")

		http.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`
			<html>
				<head>
					<title>Spacelift Prometheus Exporter</title>
				</head>
				<body>
					Welcome to the Spacelift Prometheus exporter! Please find the available metrics at <a href="/metrics">/metrics</a>.
				</body>
			</html>`))
		}))

		// Create a new registry.
		reg := prometheus.NewRegistry()

		collector, err := newSpaceliftCollector(ctx, httpClient, session, scrapeTimeout)
		if err != nil {
			return cli.Exit(fmt.Sprintf("could not create Spacelift collector: %v", err), ExitCodeStartupError)
		}
		reg.MustRegister(collector)

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

		listenAddress := cmd.String(flagListenAddress.Name)

		logger.Info("Ready for launch! Listening on ", listenAddress)

		server := http.Server{Addr: listenAddress, ReadHeaderTimeout: time.Second * 5}

		go func() {
			if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Fatalw("Error running HTTP server", zap.Error(err))
			}
		}()

		// Wait for interrupt signal to gracefully shutdown the server.
		<-ctx.Done()

		logger.Info("Received stop signal - shutting down exporter")

		ctx, cancel := context.WithTimeout(ctx, time.Second*5)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			logger.Errorw("Failed to gracefully shutdown exporter", zap.Error(err))
		}

		logger.Info("Exporter has landed successfully!")

		return nil
	},
}

func newHTTPClient(caCertPath string) (*http.Client, error) {
	rootCAs, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("could not load system cert pool: %w", err)
	}

	if rootCAs == nil {
		rootCAs = x509.NewCertPool()
	}

	if caCertPath != "" {
		certPEM, err := os.ReadFile(caCertPath)
		if err != nil {
			return nil, fmt.Errorf("could not read CA cert file %q: %w", caCertPath, err)
		}

		if ok := rootCAs.AppendCertsFromPEM(certPEM); !ok {
			return nil, fmt.Errorf("could not parse CA cert file %q: no certificates found", caCertPath)
		}
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		RootCAs:    rootCAs,
		MinVersion: tls.VersionTLS12,
	}

	return &http.Client{Transport: transport}, nil
}
