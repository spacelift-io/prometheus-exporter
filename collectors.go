package main

import (
	"context"
	"errors"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/spacelift-io/prometheus-exporter/client"
	"github.com/spacelift-io/prometheus-exporter/client/session"
	"github.com/spacelift-io/prometheus-exporter/collector"
	"github.com/spacelift-io/prometheus-exporter/logging"
)

// newCollectors returns every collector, in the order their documents are
// requested on each scrape.
func newCollectors() []collector.Collector {
	return []collector.Collector{
		collector.NewPublicWorkerPool(),
		collector.NewWorkerPools(),
		collector.NewUsage(),
		collector.NewAggregates(),
	}
}

// newExporter assembles the exporter over the given collectors. It performs no
// I/O, so constructing one in a test does not issue a query.
func newExporter(
	ctx context.Context,
	httpClient *http.Client,
	session session.Session,
	scrapeTimeout time.Duration,
	collectors []collector.Collector,
) (*collector.Exporter, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return nil, errors.New("could not read build info")
	}

	return collector.New(
		ctx,
		logging.FromContext(ctx).Sugar(),
		client.New(httpClient, session),
		scrapeTimeout,
		collector.BuildInfo{Version: version, Commit: commit, GoVersion: info.GoVersion},
		collectors,
	), nil
}
