package collector

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// workerPools collects per-pool metrics for the account's private worker pools.
type workerPools struct {
	runsPending    *prometheus.Desc
	workersBusy    *prometheus.Desc
	workers        *prometheus.Desc
	workersDrained *prometheus.Desc
}

// NewWorkerPools returns the private worker pool collector.
func NewWorkerPools() Collector {
	labels := []string{"worker_pool_id", "worker_pool_name"}

	return &workerPools{
		runsPending: prometheus.NewDesc(
			"spacelift_worker_pool_runs_pending",
			"The number of runs currently queued and waiting for a worker from a particular pool",
			labels,
			nil),
		workersBusy: prometheus.NewDesc(
			"spacelift_worker_pool_workers_busy",
			"The number of currently busy workers in a worker pool",
			labels,
			nil),
		workers: prometheus.NewDesc(
			"spacelift_worker_pool_workers",
			"The number of workers in a worker pool",
			labels,
			nil),
		workersDrained: prometheus.NewDesc(
			"spacelift_worker_pool_workers_drained",
			"The number of workers in a worker pool that have been drained",
			labels,
			nil),
	}
}

func (c *workerPools) Name() string { return "workerpools" }

func (c *workerPools) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.runsPending
	ch <- c.workersBusy
	ch <- c.workers
	ch <- c.workersDrained
}

type workerPoolsQuery struct {
	WorkerPools []struct {
		ID          string `graphql:"id"`
		Name        string `graphql:"name"`
		PendingRuns int    `graphql:"pendingRuns"`
		BusyWorkers int    `graphql:"busyWorkers"`
		Workers     []struct {
			ID      string `graphql:"id"`
			Drained bool   `graphql:"drained"`
		} `graphql:"workers"`
	} `graphql:"workerPools"`
}

func (c *workerPools) Collect(ctx context.Context, api client.Client) ([]prometheus.Metric, error) {
	var query workerPoolsQuery
	if err := api.QueryNamed(ctx, &query, nil, "WorkerPools"); err != nil {
		return nil, err
	}

	metrics := make([]prometheus.Metric, 0, len(query.WorkerPools)*4)

	for _, pool := range query.WorkerPools {
		drained := 0
		for _, worker := range pool.Workers {
			if worker.Drained {
				drained++
			}
		}

		metrics = append(metrics,
			prometheus.MustNewConstMetric(c.runsPending, prometheus.GaugeValue, float64(pool.PendingRuns), pool.ID, pool.Name),
			prometheus.MustNewConstMetric(c.workersBusy, prometheus.GaugeValue, float64(pool.BusyWorkers), pool.ID, pool.Name),
			prometheus.MustNewConstMetric(c.workers, prometheus.GaugeValue, float64(len(pool.Workers)), pool.ID, pool.Name),
			prometheus.MustNewConstMetric(c.workersDrained, prometheus.GaugeValue, float64(drained), pool.ID, pool.Name),
		)
	}

	return metrics, nil
}
