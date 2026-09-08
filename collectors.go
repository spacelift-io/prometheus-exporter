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

// collectorSpecs is sorted by name so collector execution, logging and help
// output remain stable. A deployment- or tier-specific collector should be off
// by default so unsupported deployments do not report it permanently.
var collectorSpecs = []collectorSpec{
	{name: "aggregates", defaultEnabled: true, build: collector.NewAggregates},
	{name: "publicworkerpool", defaultEnabled: true, build: collector.NewPublicWorkerPool},
	{name: "usage", defaultEnabled: true, build: collector.NewUsage},
	{name: "workerpools", defaultEnabled: true, build: collector.NewWorkerPools},
}

// newCollectors builds the enabled collector set. Unknown names in the map are
// ignored: the only production caller derives its keys from collectorSpecs
// itself, and a mistyped --collector.<name> flag is rejected by the CLI before
// this runs.
func newCollectors(enabled map[string]bool) []collector.Collector {
	// Emit in a stable order so that /metrics output does not shuffle
	// between scrapes.
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

// newExporter assembles the exporter. It performs no I/O, so constructing one
// in a test does not issue a query.
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
		client.NewNamed(httpClient, session),
		scrapeTimeout,
		collector.BuildInfo{Version: version, Commit: commit, GoVersion: info.GoVersion},
		collectors,
		partialScrapes,
	), nil
}
