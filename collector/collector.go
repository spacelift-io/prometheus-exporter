// Package collector implements the Spacelift metric collectors.
//
// Each collector owns exactly one GraphQL document. That is not a stylistic
// choice: GraphQL propagates a field error up to the nearest nullable parent,
// and several of the fields this exporter reads are non-null at the query root
// (`usage: Usage!`, `workerPools: [WorkerPool!]!`, `messageQueueStats:
// [MessageQueueStats!]!`), while every field of `SpaceliftMetrics` is non-null
// under a nullable `metrics`. One erroring field therefore nulls the entire
// response, so partial success is impossible inside a single selection set.
//
// The boundaries were drawn along availability. When this split was written,
// `publicWorkerPool`, `usage` and `metrics` rejected machine sessions while
// `workerPools` did not, so a machine key that could serve worker pool metrics
// produced nothing at all under a single document. Spacelift SaaS removed
// those gates in August 2026 (backend #16058); Self-Hosted releases older than
// that still have them, and the non-null propagation problem above is
// unaffected either way.
package collector

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// ErrNotSupported is returned by a Collector whose data is not available on
// this deployment, tier, or for this API key. It is reported as
// spacelift_scrape_collector_supported=0 rather than as a failure, so that a
// self-hosted-only field on SaaS does not page anyone.
var ErrNotSupported = errors.New("not supported on this Spacelift deployment")

// Collector gathers one subsystem's metrics from a single GraphQL document.
type Collector interface {
	// Name is the value of the "collector" label, and the suffix of the
	// --collector.<name> flag.
	Name() string

	// Describe sends the descriptors of every metric this collector can emit.
	Describe(ch chan<- *prometheus.Desc)

	// Collect queries the API and returns a complete metric set. Returning
	// ErrNotSupported marks the collector unsupported rather than failed.
	Collect(ctx context.Context, c client.NamedClient) ([]prometheus.Metric, error)
}

// Exporter fans a scrape out across collectors, isolating their failures from
// each other.
type Exporter struct {
	ctx            context.Context
	logger         *zap.SugaredLogger
	client         client.NamedClient
	scrapeTimeout  time.Duration
	collectors     []Collector
	partialScrapes bool

	collectorSuccess   *prometheus.Desc
	collectorDuration  *prometheus.Desc
	collectorSupported *prometheus.Desc
	scrapeDuration     *prometheus.Desc
	buildInfo          *prometheus.Desc
	scrapeError        *prometheus.Desc
}

// BuildInfo identifies the running exporter.
type BuildInfo struct {
	Version   string
	Commit    string
	GoVersion string
}

// New returns an Exporter over the given collectors.
//
// partialScrapes, when true, returns available metrics unless every supported
// collector fails. When false, any collector error or unsupported result
// preserves the legacy HTTP 500 behavior.
func New(
	ctx context.Context,
	logger *zap.SugaredLogger,
	c client.NamedClient,
	scrapeTimeout time.Duration,
	build BuildInfo,
	collectors []Collector,
	partialScrapes bool,
) *Exporter {
	return &Exporter{
		ctx:            ctx,
		logger:         logger,
		client:         c,
		scrapeTimeout:  scrapeTimeout,
		collectors:     collectors,
		partialScrapes: partialScrapes,

		collectorSuccess: prometheus.NewDesc(
			"spacelift_scrape_collector_success",
			"Whether a collector succeeded on the last scrape (1) or failed (0).",
			[]string{"collector"},
			nil),
		collectorDuration: prometheus.NewDesc(
			"spacelift_scrape_collector_duration_seconds",
			"Duration of a collector's Spacelift API request on the last scrape.",
			[]string{"collector"},
			nil),
		collectorSupported: prometheus.NewDesc(
			"spacelift_scrape_collector_supported",
			"Whether a collector's data is available on this deployment, tier and API key (1) or not (0). "+
				"An unsupported collector is not a failure.",
			[]string{"collector"},
			nil),
		scrapeDuration: prometheus.NewDesc(
			"spacelift_scrape_duration_seconds",
			"The duration in seconds of the request to the Spacelift API for metrics",
			nil,
			nil),
		buildInfo: prometheus.NewDesc(
			"spacelift_build_info",
			"Contains build information about the exporter",
			nil,
			prometheus.Labels{
				"version":   build.Version,
				"commit":    build.Commit,
				"goversion": build.GoVersion,
			}),
		// scrapeError is deliberately absent from Describe: it is only
		// ever emitted through NewInvalidMetric, whose purpose is to fail
		// Gather() so promhttp returns HTTP 500. It never renders in the
		// exposition format, so it is not part of the metric surface.
		// The Gather() error text names it; TestStrictModeGatherErrorNames
		// pins that.
		scrapeError: prometheus.NewDesc(
			"spacelift_error",
			"One or more Spacelift metric collectors failed",
			nil,
			nil),
	}
}

// Describe implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- e.collectorSuccess
	ch <- e.collectorDuration
	ch <- e.collectorSupported
	ch <- e.scrapeDuration
	ch <- e.buildInfo

	for _, c := range e.collectors {
		c.Describe(ch)
	}
}

// Collect implements prometheus.Collector.
//
// Every collector runs, and one failing collector does not suppress metrics
// collected by the others. Strict mode preserves the legacy failed-scrape
// contract: the spacelift_error invalid metric fails Gather(), promhttp then
// discards everything gathered and returns HTTP 500, so on a failing scrape
// the broken subsystem is named in the response body and the logs rather than
// in a queryable series. The health series are scrapeable during failures only
// in partial mode, which exposes available metrics unless every supported
// collector fails.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(e.ctx, e.scrapeTimeout)
	defer cancel()

	ch <- prometheus.MustNewConstMetric(e.buildInfo, prometheus.GaugeValue, 1)

	results := make([]collectionResult, len(e.collectors))
	var waitGroup sync.WaitGroup
	waitGroup.Add(len(e.collectors))

	for index, c := range e.collectors {
		go func() {
			defer waitGroup.Done()
			results[index] = e.collect(ctx, c)
		}()
	}
	waitGroup.Wait()

	var scrapeIssues []error
	successfulCollectors := 0
	failedCollectors := 0

	for _, result := range results {
		for _, metric := range result.metrics {
			ch <- metric
		}

		success, supported := 1.0, 1.0
		switch {
		case errors.Is(result.err, ErrNotSupported):
			supported = 0
			logUnsupported := e.logger.Debugw
			if !e.partialScrapes {
				logUnsupported = e.logger.Errorw
			}
			logUnsupported(
				"Collector is not supported on this deployment",
				"collector", result.collector.Name(),
			)
			scrapeIssues = append(scrapeIssues, result.err)
		case result.err != nil:
			success = 0
			failedCollectors++
			e.logger.Errorw(
				"Collector failed",
				"collector", result.collector.Name(),
				"timeout", e.scrapeTimeout,
				zap.Error(result.err),
			)
			scrapeIssues = append(scrapeIssues, result.err)
		default:
			successfulCollectors++
		}

		ch <- prometheus.MustNewConstMetric(
			e.collectorSuccess, prometheus.GaugeValue, success, result.collector.Name())
		ch <- prometheus.MustNewConstMetric(
			e.collectorSupported, prometheus.GaugeValue, supported, result.collector.Name())
		ch <- prometheus.MustNewConstMetric(
			e.collectorDuration, prometheus.GaugeValue, result.duration.Seconds(), result.collector.Name())
	}

	ch <- prometheus.MustNewConstMetric(
		e.scrapeDuration, prometheus.GaugeValue, time.Since(start).Seconds(),
	)

	// Preserve the existing failure contract unless partial scrapes are
	// explicitly enabled. In partial mode, unsupported collectors are healthy
	// but unavailable; only a genuine failure with no successful collector
	// stays an HTTP 500.
	partialFailure := failedCollectors > 0 && successfulCollectors == 0
	if len(scrapeIssues) > 0 && (!e.partialScrapes || partialFailure) {
		ch <- prometheus.NewInvalidMetric(e.scrapeError, errors.Join(scrapeIssues...))
	}
}

type collectionResult struct {
	collector Collector
	metrics   []prometheus.Metric
	err       error
	duration  time.Duration
}

func (e *Exporter) collect(ctx context.Context, c Collector) (result collectionResult) {
	start := time.Now()
	result.collector = c

	defer func() {
		result.duration = time.Since(start)
		if recovered := recover(); recovered != nil {
			result.err = fmt.Errorf("%s collector panicked: %v", c.Name(), recovered)
		}
	}()

	result.metrics, result.err = c.Collect(ctx, e.client)
	if result.err != nil {
		// A collector is atomic: never expose metrics produced alongside an
		// error, because they may be incomplete.
		result.metrics = nil
	}

	return result
}
