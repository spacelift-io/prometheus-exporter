package collector

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// PublicWorkerPool collects metrics for the shared public worker pool.
//
// This is a separate collector from WorkerPools, despite the two being adjacent
// in the API and having been in the same query, because their availability can
// differ. Self-Hosted releases older than August 2026 reject machine sessions
// for publicWorkerPool but not workerPools; sharing a document on those
// backends means a machine key produces neither.
//
// Self-hosted accounts have no public worker pool, so the values are all zero
// there.
type PublicWorkerPool struct {
	runsPending *prometheus.Desc
	workersBusy *prometheus.Desc
	parallelism *prometheus.Desc
}

// NewPublicWorkerPool returns the public worker pool collector.
func NewPublicWorkerPool() Collector {
	return &PublicWorkerPool{
		runsPending: prometheus.NewDesc(
			"spacelift_public_worker_pool_runs_pending",
			"The number of runs in your account currently queued and waiting for a public worker",
			nil,
			nil),
		workersBusy: prometheus.NewDesc(
			"spacelift_public_worker_pool_workers_busy",
			"The number of currently busy workers in the public worker pool for this account",
			nil,
			nil),
		parallelism: prometheus.NewDesc(
			"spacelift_public_worker_pool_parallelism",
			"The maximum number of simultaneously executing runs on the public worker pool for this account",
			nil,
			nil),
	}
}

// Name implements Collector.
func (c *PublicWorkerPool) Name() string { return "publicworkerpool" }

// Describe implements Collector.
func (c *PublicWorkerPool) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.runsPending
	ch <- c.workersBusy
	ch <- c.parallelism
}

type publicWorkerPoolQuery struct {
	PublicWorkerPool struct {
		Parallelism int `graphql:"parallelism"`
		BusyWorkers int `graphql:"busyWorkers"`
		PendingRuns int `graphql:"pendingRuns"`
	} `graphql:"publicWorkerPool"`
}

// Collect implements Collector.
func (c *PublicWorkerPool) Collect(ctx context.Context, api client.NamedClient) ([]prometheus.Metric, error) {
	var query publicWorkerPoolQuery
	if err := api.QueryNamed(ctx, &query, nil, "PublicWorkerPool"); err != nil {
		return nil, classify(err, c.Name())
	}

	pool := query.PublicWorkerPool

	return []prometheus.Metric{
		prometheus.MustNewConstMetric(c.runsPending, prometheus.GaugeValue, float64(pool.PendingRuns)),
		prometheus.MustNewConstMetric(c.workersBusy, prometheus.GaugeValue, float64(pool.BusyWorkers)),
		prometheus.MustNewConstMetric(c.parallelism, prometheus.GaugeValue, float64(pool.Parallelism)),
	}, nil
}
