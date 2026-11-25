package collector

import (
	"context"
	"fmt"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// runStateReady is the state of a run that is queued and waiting for a worker.
// A pool's pendingRuns counts exactly these runs, so counting them here keeps
// the by-type series summing to spacelift_worker_pool_runs_pending.
const runStateReady = "READY"

// knownRunTypes are the run types a series is always emitted for, lower-cased,
// whether or not any run of the type is queued. Emitting zeros keeps a scaler
// that selects one series from reading a missing series as an error or as 0.
// A type the API adds later is still counted, under its own lower-cased name.
var knownRunTypes = []string{"proposed", "tracked", "task", "testing", "destroy", "parse"}

// workerPoolRuns breaks each private worker pool's pending runs down by run
// type and by whether the run belongs to a drift detection workflow. It exists
// so that autoscalers can ignore low-priority work such as drift detection,
// which pendingRuns on its own cannot tell apart.
//
// It is a collector of its own, with its own document, so that a failure here
// does not take the workerpools metrics down with it.
type workerPoolRuns struct {
	runsPendingByType *prometheus.Desc
}

// NewWorkerPoolRuns returns the per-type pending runs collector.
func NewWorkerPoolRuns() Collector {
	return &workerPoolRuns{
		runsPendingByType: prometheus.NewDesc(
			"spacelift_worker_pool_runs_pending_by_type",
			"The number of runs currently queued and waiting for a worker from a particular pool, by run type and drift detection",
			[]string{"worker_pool_id", "worker_pool_name", "run_type", "drift"},
			nil),
	}
}

func (c *workerPoolRuns) Name() string { return "workerpoolruns" }

func (c *workerPoolRuns) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.runsPendingByType
}

type workerPoolRunsQuery struct {
	WorkerPools []struct {
		ID                    string `graphql:"id"`
		Name                  string `graphql:"name"`
		SearchSchedulableRuns struct {
			Edges []struct {
				Node struct {
					Run struct {
						Type           string `graphql:"type"`
						State          string `graphql:"state"`
						DriftDetection bool   `graphql:"driftDetection"`
					} `graphql:"run"`
				} `graphql:"node"`
			} `graphql:"edges"`
			PageInfo struct {
				HasNextPage bool `graphql:"hasNextPage"`
			} `graphql:"pageInfo"`
		} `graphql:"searchSchedulableRuns(input: {})"`
	} `graphql:"workerPools"`
}

// runKey is one series of a pool.
type runKey struct {
	runType string
	drift   bool
}

func (c *workerPoolRuns) Collect(ctx context.Context, api client.Client) ([]prometheus.Metric, error) {
	var query workerPoolRunsQuery
	if err := api.QueryNamed(ctx, &query, nil, "WorkerPoolRuns"); err != nil {
		return nil, err
	}

	var metrics []prometheus.Metric

	for _, pool := range query.WorkerPools {
		// The API returns the whole queue and ignores `first` on this field.
		// If that ever changes, counting a partial page would silently
		// under-report and under-scale, so fail the collector instead.
		if pool.SearchSchedulableRuns.PageInfo.HasNextPage {
			return nil, fmt.Errorf("worker pool %q: queue is paginated, refusing to count a partial page", pool.Name)
		}

		counts := make(map[runKey]int)
		for _, runType := range knownRunTypes {
			counts[runKey{runType, false}] = 0
			counts[runKey{runType, true}] = 0
		}

		for _, edge := range pool.SearchSchedulableRuns.Edges {
			run := edge.Node.Run
			if run.State != runStateReady {
				continue
			}

			counts[runKey{strings.ToLower(run.Type), run.DriftDetection}]++
		}

		for key, count := range counts {
			metrics = append(metrics, prometheus.MustNewConstMetric(
				c.runsPendingByType,
				prometheus.GaugeValue,
				float64(count),
				pool.ID, pool.Name, key.runType, fmt.Sprint(key.drift),
			))
		}
	}

	return metrics, nil
}
