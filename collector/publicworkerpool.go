package collector

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// publicWorkerPool collects metrics for the shared public worker pool. Self-
// hosted accounts have no public worker pool, so the values are all zero there.
type publicWorkerPool struct {
	runsPending *prometheus.Desc
	workersBusy *prometheus.Desc
	parallelism *prometheus.Desc
}

// NewPublicWorkerPool returns the public worker pool collector.
func NewPublicWorkerPool() Collector {
	return &publicWorkerPool{
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

func (c *publicWorkerPool) Name() string { return "publicworkerpool" }

func (c *publicWorkerPool) Describe(ch chan<- *prometheus.Desc) {
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

func (c *publicWorkerPool) Collect(ctx context.Context, api client.Client) ([]prometheus.Metric, error) {
	var query publicWorkerPoolQuery
	if err := api.QueryNamed(ctx, &query, nil, "PublicWorkerPool"); err != nil {
		return nil, err
	}

	pool := query.PublicWorkerPool

	return []prometheus.Metric{
		prometheus.MustNewConstMetric(c.runsPending, prometheus.GaugeValue, float64(pool.PendingRuns)),
		prometheus.MustNewConstMetric(c.workersBusy, prometheus.GaugeValue, float64(pool.BusyWorkers)),
		prometheus.MustNewConstMetric(c.parallelism, prometheus.GaugeValue, float64(pool.Parallelism)),
	}, nil
}
