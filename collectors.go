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

type collectorSpec struct {
	name           string
	defaultEnabled bool
	build          func() collector.Collector
}

// collectorSpecs lists every collector in the order their documents are
// requested on each scrape, which is also the order of their flags in --help.
// A collector whose data exists only on some deployments or tiers should be
// off by default, so that the others do not report it as unsupported forever.
var collectorSpecs = []collectorSpec{
	{name: "publicworkerpool", defaultEnabled: true, build: collector.NewPublicWorkerPool},
	{name: "workerpools", defaultEnabled: true, build: collector.NewWorkerPools},
	{name: "usage", defaultEnabled: true, build: collector.NewUsage},
	{name: "aggregates", defaultEnabled: true, build: collector.NewAggregates},
}

// newCollectors builds the enabled collectors. A collector absent from enabled
// takes its default; a nil map yields the default set.
func newCollectors(enabled map[string]bool) []collector.Collector {
	out := make([]collector.Collector, 0, len(collectorSpecs))
	for _, spec := range collectorSpecs {
		on, set := enabled[spec.name]
		if !set {
			on = spec.defaultEnabled
		}
		if on {
			out = append(out, spec.build())
		}
	}

	return out
}

// newExporter assembles the exporter over the given collectors. It performs no
// I/O, so constructing one in a test does not issue a query.
func newExporter(
	ctx context.Context,
	httpClient *http.Client,
	session session.Session,
	scrapeTimeout time.Duration,
	collectors []collector.Collector,
	partialScrapes bool,
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
		partialScrapes,
	), nil
}
