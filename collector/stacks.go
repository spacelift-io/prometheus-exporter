package collector

import (
	"context"
	"slices"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// stacks collects one set of series per stack. Its cardinality grows with the
// account, which is why it is off by default.
type stacks struct {
	state             *prometheus.Desc
	stateSetTimestamp *prometheus.Desc
	disabled          *prometheus.Desc
	locked            *prometheus.Desc
}

// NewStacks returns the per-stack collector.
func NewStacks() Collector {
	labels := []string{"stack_id", "stack_name", "space_id"}

	return &stacks{
		state: prometheus.NewDesc(
			"spacelift_stack_state",
			"Always 1, with the stack's current state as the state label",
			append(slices.Clone(labels), "state"),
			nil),
		stateSetTimestamp: prometheus.NewDesc(
			"spacelift_stack_state_set_timestamp_seconds",
			"The Unix time at which the stack entered its current state, absent for a stack that has never run",
			labels,
			nil),
		disabled: prometheus.NewDesc(
			"spacelift_stack_disabled",
			"Whether the stack is disabled (1) or not (0)",
			labels,
			nil),
		locked: prometheus.NewDesc(
			"spacelift_stack_locked",
			"Whether the stack is locked by a user (1) or not (0)",
			labels,
			nil),
	}
}

func (c *stacks) Name() string { return "stacks" }

func (c *stacks) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.state
	ch <- c.stateSetTimestamp
	ch <- c.disabled
	ch <- c.locked
}

type stacksQuery struct {
	Stacks []struct {
		ID         string `graphql:"id"`
		Name       string `graphql:"name"`
		Space      string `graphql:"space"`
		State      string `graphql:"state"`
		StateSetAt *int64 `graphql:"stateSetAt"`
		IsDisabled bool   `graphql:"isDisabled"`
		// LockedBy is read only for whether it is set. It names a user, so
		// it never becomes a label.
		LockedBy *string `graphql:"lockedBy"`
	} `graphql:"stacks"`
}

func (c *stacks) Collect(ctx context.Context, api client.Client) ([]prometheus.Metric, error) {
	var query stacksQuery
	if err := api.QueryNamed(ctx, &query, nil, "Stacks"); err != nil {
		return nil, err
	}

	metrics := make([]prometheus.Metric, 0, len(query.Stacks)*4)

	for _, stack := range query.Stacks {
		metrics = append(metrics,
			prometheus.MustNewConstMetric(c.state, prometheus.GaugeValue, 1, stack.ID, stack.Name, stack.Space, stack.State),
			prometheus.MustNewConstMetric(c.disabled, prometheus.GaugeValue, boolValue(stack.IsDisabled), stack.ID, stack.Name, stack.Space),
			prometheus.MustNewConstMetric(c.locked, prometheus.GaugeValue, boolValue(stack.LockedBy != nil), stack.ID, stack.Name, stack.Space),
		)

		if stack.StateSetAt != nil {
			metrics = append(metrics,
				prometheus.MustNewConstMetric(c.stateSetTimestamp, prometheus.GaugeValue, float64(*stack.StateSetAt), stack.ID, stack.Name, stack.Space))
		}
	}

	return metrics, nil
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}

	return 0
}
