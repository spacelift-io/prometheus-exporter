// Package collector implements the Spacelift metric collectors.
//
// Each collector owns one GraphQL document. GraphQL propagates a field error
// up to the nearest nullable parent, and several of the fields this exporter
// reads are non-null at the query root (`usage: Usage!`, `workerPools:
// [WorkerPool!]!`), so one erroring field nulls an entire shared response and
// partial success is impossible inside a single selection set. One document
// per collector keeps a failure contained to the collector that owns it.
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

// Collector gathers one subsystem's metrics from a single GraphQL document.
type Collector interface {
	// Name identifies the collector in logs and errors.
	Name() string

	// Describe sends the descriptors of every metric this collector can emit.
	Describe(ch chan<- *prometheus.Desc)

	// Collect queries the API and returns a complete metric set, or an error
	// in which case no metrics are returned.
	Collect(ctx context.Context, c client.Client) ([]prometheus.Metric, error)
}

// BuildInfo identifies the running exporter.
type BuildInfo struct {
	Version   string
	Commit    string
	GoVersion string
}

// Exporter is the prometheus.Collector registered with the registry. It runs
// every collector on each scrape and reports the scrape as a whole.
type Exporter struct {
	ctx            context.Context
	logger         *zap.SugaredLogger
	client         client.Client
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

// New returns an Exporter over the given collectors.
//
// With partialScrapes false, any collector error or unsupported result fails
// the scrape, which is the exporter's original behaviour. With it true, the
// scrape serves whatever the healthy collectors returned and fails only when
// no collector succeeded at all.
func New(
	ctx context.Context,
	logger *zap.SugaredLogger,
	c client.Client,
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
		// scrapeError is only ever emitted through NewInvalidMetric, whose
		// purpose is to fail Gather() so that promhttp returns HTTP 500. It
		// never renders in the exposition format, so it is not described.
		scrapeError: prometheus.NewDesc(
			"spacelift_error",
			"Failed to request metrics from the Spacelift API",
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
// The collectors run concurrently under one scrape deadline, so scrape latency
// is bounded by the slowest collector rather than the sum. Each reports whether
// it succeeded, whether its data is available at all, and how long its request
// took. A collector that panics fails like one that returned an error.
//
// By default any collector error or unsupported result fails the scrape: the
// spacelift_error invalid metric makes Gather() fail, promhttp discards
// everything gathered and returns HTTP 500 naming the collectors involved, so
// existing alerting on `up` keeps working. In that mode the per-collector
// series are only served on scrapes where every collector succeeded.
//
// With partial scrapes enabled, the healthy collectors' metrics and every
// collector's health series are served, and the scrape fails only when no
// collector succeeded at all: an exporter with nothing to export is a failed
// scrape, not a healthy target silently serving zero data.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(e.ctx, e.scrapeTimeout)
	defer cancel()

	results := make([]collectionResult, len(e.collectors))
	var waitGroup sync.WaitGroup
	for index, c := range e.collectors {
		waitGroup.Go(func() { results[index] = e.collect(ctx, c) })
	}
	waitGroup.Wait()

	var metrics []prometheus.Metric
	var issues []error
	failed, succeeded := 0, 0

	for index, c := range e.collectors {
		result := results[index]
		ch <- prometheus.MustNewConstMetric(
			e.collectorDuration, prometheus.GaugeValue, result.duration.Seconds(), c.Name())

		success, supported := 1.0, 1.0
		switch err := classify(result.err); {
		case errors.Is(err, ErrNotSupported):
			supported = 0
			log := e.logger.Debugw
			if !e.partialScrapes {
				log = e.logger.Errorw
			}
			log("Collector is not supported on this deployment", "collector", c.Name(), zap.Error(err))
			issues = append(issues, fmt.Errorf("%s: %w", c.Name(), err))
		case err != nil:
			success = 0
			failed++
			e.logger.Errorw("Failed to request metrics from the Spacelift API",
				"collector", c.Name(), "timeout", e.scrapeTimeout, zap.Error(err))
			issues = append(issues, fmt.Errorf("%s: %w", c.Name(), err))
		default:
			succeeded++
		}
		ch <- prometheus.MustNewConstMetric(e.collectorSuccess, prometheus.GaugeValue, success, c.Name())
		ch <- prometheus.MustNewConstMetric(e.collectorSupported, prometheus.GaugeValue, supported, c.Name())

		metrics = append(metrics, result.metrics...)
	}

	ch <- prometheus.MustNewConstMetric(e.scrapeDuration, prometheus.GaugeValue, time.Since(start).Seconds())

	if len(issues) > 0 && (!e.partialScrapes || succeeded == 0) {
		ch <- prometheus.NewInvalidMetric(e.scrapeError, errors.Join(issues...))

		return
	}

	ch <- prometheus.MustNewConstMetric(e.buildInfo, prometheus.GaugeValue, 1)
	for _, metric := range metrics {
		ch <- metric
	}
}

type collectionResult struct {
	metrics  []prometheus.Metric
	err      error
	duration time.Duration
}

// collect runs one collector, timing it and turning a panic into an error so
// that a bug in one collector cannot take the process down.
func (e *Exporter) collect(ctx context.Context, c Collector) (result collectionResult) {
	start := time.Now()
	defer func() {
		result.duration = time.Since(start)
		if recovered := recover(); recovered != nil {
			result.metrics, result.err = nil, fmt.Errorf("collector panicked: %v", recovered)
		}
	}()

	result.metrics, result.err = c.Collect(ctx, e.client)

	return result
}
