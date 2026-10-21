package collector

import (
	"cmp"
	"context"
	"slices"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// searchInput is the SearchInput variable of the schedulable runs search. It
// sets no page size: the search ignores `first` and reports hasNextPage false
// regardless, so completeness is judged against the search's own total
// instead (see summarizeQueue).
type searchInput struct {
	OrderBy []searchQueryOrder `json:"orderBy"`
}

// GetGraphQLType names the variable's type in the document, which the client
// would otherwise derive from the Go type name.
func (searchInput) GetGraphQLType() string { return "SearchInput" }

type searchQueryOrder struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}

// searchSuggestionsInput is the SearchSuggestionsInput variable of the
// suggestions query, which is read only for its filteredCount. An empty field
// list asks for no per-field suggestions.
type searchSuggestionsInput struct {
	Fields []string `json:"fields"`
}

// GetGraphQLType names the variable's type in the document.
func (searchSuggestionsInput) GetGraphQLType() string { return "SearchSuggestionsInput" }

// queueVariables asks for the runs oldest first and for the total alone. The
// order arranges what the search returns. Nothing in the response says it was
// applied across every matched run, so it is not relied on to make a partial
// result hold the oldest runs.
var queueVariables = map[string]any{
	"input": searchInput{OrderBy: []searchQueryOrder{{Field: "createdAt", Direction: "ASC"}}},
	"total": searchSuggestionsInput{Fields: []string{}},
}

// schedulableRunsTotal is the number of runs the search matches, which the
// search result itself does not carry.
type schedulableRunsTotal struct {
	FilteredCount int `graphql:"filteredCount"`
}

// schedulableRuns is the part of a pool's schedulable runs the search
// returned. These are the runs waiting for a worker (state READY) and the runs
// a worker is executing.
type schedulableRuns struct {
	Edges []struct {
		Node struct {
			Run struct {
				CreatedAt int64  `graphql:"createdAt"`
				State     string `graphql:"state"`
				Type      string `graphql:"type"`
			} `graphql:"run"`
		} `graphql:"node"`
	} `graphql:"edges"`
	PageInfo struct {
		HasNextPage bool `graphql:"hasNextPage"`
	} `graphql:"pageInfo"`
}

type runKey struct {
	state, runType string
}

type runCount struct {
	runKey
	count int
}

// queueSummary is what the metrics report about a queue.
type queueSummary struct {
	// counts holds one entry per state and type returned, sorted by both.
	counts []runCount

	// oldest is the earliest creation time, or nil when the queue is empty
	// or the search returned only part of it.
	oldest *int64

	truncated bool
}

// summarizeQueue reports the runs the search returned against total, the
// number it matched. Fewer returned than matched means the counts are partial.
// The oldest creation time is then withheld rather than understated, because
// nothing guarantees that a partial result contains the oldest run.
//
// The search and the total are separate resolvers, not one snapshot. A run
// created between them makes a complete result read as truncated for that
// scrape; runs finishing between them can make a partial result read as
// complete. Both last one scrape.
func summarizeQueue(runs schedulableRuns, total int) queueSummary {
	counts := map[runKey]int{}
	var summary queueSummary

	for _, edge := range runs.Edges {
		run := edge.Node.Run
		counts[runKey{state: run.State, runType: run.Type}]++

		if summary.oldest == nil || run.CreatedAt < *summary.oldest {
			createdAt := run.CreatedAt
			summary.oldest = &createdAt
		}
	}

	for key, count := range counts {
		summary.counts = append(summary.counts, runCount{runKey: key, count: count})
	}
	slices.SortFunc(summary.counts, func(a, b runCount) int {
		return cmp.Or(cmp.Compare(a.state, b.state), cmp.Compare(a.runType, b.runType))
	})

	summary.truncated = runs.PageInfo.HasNextPage || len(runs.Edges) < total
	if summary.truncated {
		summary.oldest = nil
	}

	return summary
}

// queueDescs are the three descriptors one pool's queue reports, for either
// the private or the public pool.
type queueDescs struct {
	runs, oldest, truncated *prometheus.Desc
}

func newQueueDescs(prefix, pool string, labels []string) queueDescs {
	return queueDescs{
		runs: prometheus.NewDesc(
			prefix+"_schedulable_runs",
			"The number of runs waiting for a worker (state READY) or executing on one in "+pool+", by state and type; partial when "+prefix+"_schedulable_runs_truncated is 1",
			append(slices.Clone(labels), "state", "type"),
			nil),
		oldest: prometheus.NewDesc(
			prefix+"_oldest_schedulable_run_created_timestamp_seconds",
			"The Unix creation time of the oldest run waiting for or executing on "+pool+", absent when there is none or when the search returned only some of them",
			labels,
			nil),
		truncated: prometheus.NewDesc(
			prefix+"_schedulable_runs_truncated",
			"Whether the search returned only some of "+pool+"'s schedulable runs (1), so the by-state counts are partial and the oldest-run time is withheld, or all of them (0)",
			labels,
			nil),
	}
}

func (d queueDescs) describe(ch chan<- *prometheus.Desc) {
	ch <- d.runs
	ch <- d.oldest
	ch <- d.truncated
}

func (d queueDescs) metrics(runs schedulableRuns, total int, labelValues ...string) []prometheus.Metric {
	summary := summarizeQueue(runs, total)
	out := make([]prometheus.Metric, 0, len(summary.counts)+2)

	for _, c := range summary.counts {
		out = append(out, prometheus.MustNewConstMetric(
			d.runs, prometheus.GaugeValue, float64(c.count), append(slices.Clone(labelValues), c.state, c.runType)...))
	}

	if summary.oldest != nil {
		out = append(out, prometheus.MustNewConstMetric(d.oldest, prometheus.GaugeValue, float64(*summary.oldest), labelValues...))
	}

	return append(out, prometheus.MustNewConstMetric(d.truncated, prometheus.GaugeValue, boolValue(summary.truncated), labelValues...))
}

// queue collects the schedulable runs of every private worker pool. It issues
// a search and a count per pool on each scrape, which is why it is off by
// default.
type queue struct {
	descs queueDescs
}

// NewQueue returns the private worker pool queue collector.
func NewQueue() Collector {
	return &queue{descs: newQueueDescs(
		"spacelift_worker_pool", "a worker pool", []string{"worker_pool_id", "worker_pool_name"})}
}

func (c *queue) Name() string { return "queue" }

func (c *queue) Describe(ch chan<- *prometheus.Desc) { c.descs.describe(ch) }

type queueQuery struct {
	WorkerPools []struct {
		ID                    string                `graphql:"id"`
		Name                  string                `graphql:"name"`
		SearchSchedulableRuns *schedulableRuns      `graphql:"searchSchedulableRuns(input:$input)"`
		Total                 *schedulableRunsTotal `graphql:"searchSchedulableRunsSuggestions(input:$total)"`
	} `graphql:"workerPools"`
}

func (c *queue) Collect(ctx context.Context, api client.Client) ([]prometheus.Metric, error) {
	var query queueQuery
	if err := api.QueryNamed(ctx, &query, queueVariables, "Queue"); err != nil {
		return nil, err
	}

	var metrics []prometheus.Metric
	for _, pool := range query.WorkerPools {
		// Both fields are nullable. A null without an error means the API
		// has no answer for this pool, which then reports nothing rather
		// than an empty or unjudged queue. A null with an error never
		// reaches here: the client returns the error alone, which fails
		// the collector for every pool.
		if pool.SearchSchedulableRuns == nil || pool.Total == nil {
			continue
		}
		metrics = append(metrics, c.descs.metrics(*pool.SearchSchedulableRuns, pool.Total.FilteredCount, pool.ID, pool.Name)...)
	}

	return metrics, nil
}

// publicQueue collects the account's schedulable runs on the public worker
// pool.
type publicQueue struct {
	descs queueDescs
}

// NewPublicQueue returns the public worker pool queue collector.
func NewPublicQueue() Collector {
	return &publicQueue{descs: newQueueDescs("spacelift_public_worker_pool", "the public worker pool", nil)}
}

func (c *publicQueue) Name() string { return "publicqueue" }

func (c *publicQueue) Describe(ch chan<- *prometheus.Desc) { c.descs.describe(ch) }

type publicQueueQuery struct {
	PublicWorkerPool *struct {
		SearchSchedulableRuns *schedulableRuns      `graphql:"searchSchedulableRuns(input:$input)"`
		Total                 *schedulableRunsTotal `graphql:"searchSchedulableRunsSuggestions(input:$total)"`
	} `graphql:"publicWorkerPool"`
}

func (c *publicQueue) Collect(ctx context.Context, api client.Client) ([]prometheus.Metric, error) {
	var query publicQueueQuery
	if err := api.QueryNamed(ctx, &query, queueVariables, "PublicQueue"); err != nil {
		return nil, err
	}

	pool := query.PublicWorkerPool
	if pool == nil || pool.SearchSchedulableRuns == nil || pool.Total == nil {
		return nil, nil
	}

	return c.descs.metrics(*pool.SearchSchedulableRuns, pool.Total.FilteredCount), nil
}
