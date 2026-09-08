package collector

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// Aggregates collects the account-wide figures from the `metrics` API.
//
// Every field here is a GROUP BY or scalar aggregate, so none of them can carry
// stack or space labels. That is not an oversight in the exporter: the
// underlying queries count rows grouped by state, and there is no entity
// identity in the result to label with. Per-stack metrics have to come from
// searchStacks instead.
type Aggregates struct {
	stacksCountByState    *prometheus.Desc
	resourcesCountByDrift *prometheus.Desc
	avgStackSize          *prometheus.Desc
	averageRunDuration    *prometheus.Desc
	medianRunDuration     *prometheus.Desc
}

// NewAggregates returns the account-wide aggregates collector.
func NewAggregates() Collector {
	return &Aggregates{
		stacksCountByState: prometheus.NewDesc(
			"spacelift_current_stacks_count_by_state",
			"The number of stacks grouped by state",
			[]string{"state"},
			nil),
		resourcesCountByDrift: prometheus.NewDesc(
			"spacelift_current_resources_count_by_drift",
			"The number of drifted resources",
			[]string{"state"},
			nil),
		avgStackSize: prometheus.NewDesc(
			"spacelift_current_avg_stack_size_by_resource_count",
			"The average stack size by resource count",
			nil,
			nil),
		// The API returns these two in nanoseconds despite the unitless
		// metric names, and computes them over a server-side 30-day
		// window that callers cannot see or change. Both predate this
		// refactor and are left alone rather than silently redefined.
		averageRunDuration: prometheus.NewDesc(
			"spacelift_current_average_run_duration",
			"The average run duration",
			nil,
			nil),
		medianRunDuration: prometheus.NewDesc(
			"spacelift_current_median_run_duration",
			"The median run duration",
			nil,
			nil),
	}
}

// Name implements Collector.
func (c *Aggregates) Name() string { return "aggregates" }

// Describe implements Collector.
func (c *Aggregates) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.stacksCountByState
	ch <- c.resourcesCountByDrift
	ch <- c.avgStackSize
	ch <- c.averageRunDuration
	ch <- c.medianRunDuration
}

// dataPoint is the API's DataPoint type. Labels is a list because some fields
// group by more than one dimension, though the ones read here use at most one.
type dataPoint struct {
	Value  float64
	Labels []string
}

type aggregatesQuery struct {
	Metrics struct {
		StacksCountByState          []dataPoint `graphql:"stacksCountByState"`
		ResourcesCountByDrift       []dataPoint `graphql:"resourcesCountByDrift"`
		AvgStackSizeByResourceCount []dataPoint `graphql:"avgStackSizeByResourceCount"`
		AverageRunDuration          []dataPoint `graphql:"averageRunDuration"`
		MedianRunDuration           []dataPoint `graphql:"medianRunDuration"`
	} `graphql:"metrics"`
}

// Collect implements Collector.
func (c *Aggregates) Collect(ctx context.Context, api client.NamedClient) ([]prometheus.Metric, error) {
	var query aggregatesQuery
	if err := api.QueryNamed(ctx, &query, nil, "Aggregates"); err != nil {
		return nil, classify(err, c.Name())
	}

	metrics := make([]prometheus.Metric, 0,
		len(query.Metrics.StacksCountByState)+len(query.Metrics.ResourcesCountByDrift)+3)

	for _, point := range query.Metrics.StacksCountByState {
		if len(point.Labels) > 0 {
			metrics = append(metrics, prometheus.MustNewConstMetric(
				c.stacksCountByState, prometheus.GaugeValue, point.Value, point.Labels[0]))
		}
	}

	for _, point := range query.Metrics.ResourcesCountByDrift {
		if len(point.Labels) > 0 {
			metrics = append(metrics, prometheus.MustNewConstMetric(
				c.resourcesCountByDrift, prometheus.GaugeValue, point.Value, point.Labels[0]))
		}
	}

	metrics = appendFirst(metrics, c.avgStackSize, query.Metrics.AvgStackSizeByResourceCount)
	metrics = appendFirst(metrics, c.averageRunDuration, query.Metrics.AverageRunDuration)
	metrics = appendFirst(metrics, c.medianRunDuration, query.Metrics.MedianRunDuration)

	return metrics, nil
}

// appendFirst appends a single-valued series, which the API still returns as a
// list. An empty list means the account has no data yet, in which case the
// series is omitted rather than reported as zero.
func appendFirst(metrics []prometheus.Metric, desc *prometheus.Desc, points []dataPoint) []prometheus.Metric {
	if len(points) == 0 {
		return metrics
	}

	return append(metrics, prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, points[0].Value))
}
