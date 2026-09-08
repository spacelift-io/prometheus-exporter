package collector

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// aggregates collects the account-wide figures from the `metrics` API.
type aggregates struct {
	stacksCountByState    *prometheus.Desc
	resourcesCountByDrift *prometheus.Desc
	avgStackSize          *prometheus.Desc
	averageRunDuration    *prometheus.Desc
	medianRunDuration     *prometheus.Desc
}

// NewAggregates returns the account-wide aggregates collector.
func NewAggregates() Collector {
	return &aggregates{
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

func (c *aggregates) Name() string { return "aggregates" }

func (c *aggregates) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.stacksCountByState
	ch <- c.resourcesCountByDrift
	ch <- c.avgStackSize
	ch <- c.averageRunDuration
	ch <- c.medianRunDuration
}

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

func (c *aggregates) Collect(ctx context.Context, api client.Client) ([]prometheus.Metric, error) {
	var query aggregatesQuery
	if err := api.QueryNamed(ctx, &query, nil, "Aggregates"); err != nil {
		return nil, err
	}

	var metrics []prometheus.Metric

	for _, state := range query.Metrics.StacksCountByState {
		if len(state.Labels) > 0 {
			metrics = append(metrics, prometheus.MustNewConstMetric(c.stacksCountByState, prometheus.GaugeValue, state.Value, state.Labels[0]))
		}
	}

	for _, state := range query.Metrics.ResourcesCountByDrift {
		if len(state.Labels) > 0 {
			metrics = append(metrics, prometheus.MustNewConstMetric(c.resourcesCountByDrift, prometheus.GaugeValue, state.Value, state.Labels[0]))
		}
	}

	if len(query.Metrics.AvgStackSizeByResourceCount) > 0 {
		metrics = append(metrics, prometheus.MustNewConstMetric(c.avgStackSize, prometheus.GaugeValue, query.Metrics.AvgStackSizeByResourceCount[0].Value))
	}

	if len(query.Metrics.AverageRunDuration) > 0 {
		metrics = append(metrics, prometheus.MustNewConstMetric(c.averageRunDuration, prometheus.GaugeValue, query.Metrics.AverageRunDuration[0].Value))
	}

	if len(query.Metrics.MedianRunDuration) > 0 {
		metrics = append(metrics, prometheus.MustNewConstMetric(c.medianRunDuration, prometheus.GaugeValue, query.Metrics.MedianRunDuration[0].Value))
	}

	return metrics, nil
}
