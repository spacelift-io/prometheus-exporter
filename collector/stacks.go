package collector

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// stacks collects one set of series per stack. Its cardinality grows with the
// account, which is why it is off by default.
//
// The stack's name and space, which can change, are labels on the info series
// only. Every other series is keyed by stack_id, so renaming a stack or moving
// it to another space replaces one series rather than all of them.
type stacks struct {
	info              *prometheus.Desc
	state             *prometheus.Desc
	stateSetTimestamp *prometheus.Desc
	disabled          *prometheus.Desc
	locked            *prometheus.Desc
}

// NewStacks returns the per-stack collector.
func NewStacks() Collector {
	id := []string{"stack_id"}

	return &stacks{
		info: prometheus.NewDesc(
			"spacelift_stack_info",
			"Always 1, with the stack's name and space as labels, to join to the other spacelift_stack_ series of the same target on stack_id",
			[]string{"stack_id", "stack_name", "space_id"},
			nil),
		state: prometheus.NewDesc(
			"spacelift_stack_state",
			"Always 1, with the stack's current state as the state label",
			[]string{"stack_id", "state"},
			nil),
		stateSetTimestamp: prometheus.NewDesc(
			"spacelift_stack_state_set_timestamp_seconds",
			"The Unix time at which the stack entered its current state, absent when the API reports no time for the current state",
			id,
			nil),
		disabled: prometheus.NewDesc(
			"spacelift_stack_disabled",
			"Whether the stack is disabled (1) or not (0)",
			id,
			nil),
		locked: prometheus.NewDesc(
			"spacelift_stack_locked",
			"Whether the stack is locked by a user (1) or not (0)",
			id,
			nil),
	}
}

func (c *stacks) Name() string { return "stacks" }

func (c *stacks) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.info
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

	metrics := make([]prometheus.Metric, 0, len(query.Stacks)*5)

	for _, stack := range query.Stacks {
		metrics = append(metrics,
			prometheus.MustNewConstMetric(c.info, prometheus.GaugeValue, 1, stack.ID, stack.Name, stack.Space),
			prometheus.MustNewConstMetric(c.state, prometheus.GaugeValue, 1, stack.ID, stack.State),
			prometheus.MustNewConstMetric(c.disabled, prometheus.GaugeValue, boolValue(stack.IsDisabled), stack.ID),
			prometheus.MustNewConstMetric(c.locked, prometheus.GaugeValue, boolValue(stack.LockedBy != nil), stack.ID),
		)

		if stack.StateSetAt != nil {
			metrics = append(metrics,
				prometheus.MustNewConstMetric(c.stateSetTimestamp, prometheus.GaugeValue, float64(*stack.StateSetAt), stack.ID))
		}
	}

	return metrics, nil
}
