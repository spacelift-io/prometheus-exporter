package collector

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// Worker statuses, derived from each worker's busy and drained flags, so that
// a drained worker still finishing a run (draining) is counted apart from a
// drained idle one.
const (
	workerStatusIdle     = "idle"
	workerStatusBusy     = "busy"
	workerStatusDraining = "draining"
	workerStatusDrained  = "drained"
)

// workerStatuses is every status, in the order they are emitted. Each is
// emitted for every pool, zero or not, so the series add up to the pool's
// worker count. spacelift_worker_pool_workers comes from a separate request,
// so a worker registering or changing state between the two can make them
// differ for a scrape.
var workerStatuses = []string{workerStatusIdle, workerStatusBusy, workerStatusDraining, workerStatusDrained}

// workerStatus buckets a worker by its two flags.
func workerStatus(busy, drained bool) string {
	switch {
	case drained && busy:
		return workerStatusDraining
	case drained:
		return workerStatusDrained
	case busy:
		return workerStatusBusy
	default:
		return workerStatusIdle
	}
}

// workerStatusCollector breaks each private worker pool's workers down by
// status. It sends its own workerPools document, listing every worker a
// second time, so that the default document, and its cost, do not change for
// accounts that do not enable it.
type workerStatusCollector struct {
	workersByStatus *prometheus.Desc
}

// NewWorkerStatus returns the per-pool worker status collector.
func NewWorkerStatus() Collector {
	return &workerStatusCollector{
		workersByStatus: prometheus.NewDesc(
			"spacelift_worker_pool_workers_by_status",
			"The number of workers in a worker pool by status: idle, busy, draining (drained and busy) or drained (drained and idle). "+
				"draining plus drained normally equals spacelift_worker_pool_workers_drained, "+
				"and busy plus draining normally equals spacelift_worker_pool_workers_busy (each from a separate request)",
			[]string{"worker_pool_id", "worker_pool_name", "status"},
			nil),
	}
}

func (c *workerStatusCollector) Name() string { return "workerstatus" }

func (c *workerStatusCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.workersByStatus
}

type workerStatusQuery struct {
	WorkerPools []struct {
		ID      string `graphql:"id"`
		Name    string `graphql:"name"`
		Workers []struct {
			Busy    bool `graphql:"busy"`
			Drained bool `graphql:"drained"`
		} `graphql:"workers"`
	} `graphql:"workerPools"`
}

func (c *workerStatusCollector) Collect(ctx context.Context, api client.Client) ([]prometheus.Metric, error) {
	var query workerStatusQuery
	if err := api.QueryNamed(ctx, &query, nil, "WorkerStatus"); err != nil {
		return nil, err
	}

	metrics := make([]prometheus.Metric, 0, len(query.WorkerPools)*len(workerStatuses))

	for _, pool := range query.WorkerPools {
		counts := make(map[string]int, len(workerStatuses))
		for _, worker := range pool.Workers {
			counts[workerStatus(worker.Busy, worker.Drained)]++
		}

		for _, status := range workerStatuses {
			metrics = append(metrics,
				prometheus.MustNewConstMetric(c.workersByStatus, prometheus.GaugeValue, float64(counts[status]), pool.ID, pool.Name, status))
		}
	}

	return metrics, nil
}
