package collector

import (
	"context"
	"errors"
	"testing"

	"github.com/hasura/go-graphql-client"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// plainCollector declares no version-gated fields.
type plainCollector struct{}

func (plainCollector) Name() string                     { return "plain" }
func (plainCollector) Describe(chan<- *prometheus.Desc) {}
func (plainCollector) Collect(context.Context, client.Client) ([]prometheus.Metric, error) {
	return nil, nil
}

// gatedCollector declares the run queue fields as version-gated.
type gatedCollector struct{ plainCollector }

func (gatedCollector) VersionGatedFields() []string {
	return []string{"searchSchedulableRuns", "searchSchedulableRunsSuggestions"}
}

// apiErrors builds the error the GraphQL client returns for these messages.
func apiErrors(messages ...string) error {
	errs := make(graphql.Errors, 0, len(messages))
	for _, message := range messages {
		errs = append(errs, graphql.Error{Message: message})
	}

	return errs
}

func TestClassify(t *testing.T) {
	const (
		// Captured from the live API: a field missing from WorkerPool, with
		// the suggestion suffix, and one missing from Query, without it.
		liveTypo      = `Cannot query field "searchSchedulableRunz" on type "WorkerPool". Did you mean "searchSchedulableRuns", or "searchSchedulableRunsSuggestions"?`
		liveRootField = `Cannot query field "doesNotExist" on type "Query".`

		// The same message shape, naming the declared fields.
		gatedRuns        = `Cannot query field "searchSchedulableRuns" on type "WorkerPool".`
		gatedSuggestions = `Cannot query field "searchSchedulableRunsSuggestions" on type "WorkerPool".`
	)

	for _, test := range []struct {
		name        string
		collector   Collector
		err         error
		unsupported bool
	}{
		{name: "nil", collector: gatedCollector{}, err: nil},
		{name: "genuine failure", collector: gatedCollector{}, err: apiErrors("internal error")},
		{name: "machine session", collector: plainCollector{}, err: apiErrors("not available for machine sessions"), unsupported: true},
		{name: "declared field missing", collector: gatedCollector{}, err: apiErrors(gatedRuns), unsupported: true},
		{name: "every declared field missing", collector: gatedCollector{}, err: apiErrors(gatedRuns, gatedSuggestions), unsupported: true},
		{name: "undeclared field missing, suggesting declared ones", collector: gatedCollector{}, err: apiErrors(liveTypo)},
		{name: "undeclared root field missing", collector: gatedCollector{}, err: apiErrors(liveRootField)},
		{name: "declared and undeclared fields missing", collector: gatedCollector{}, err: apiErrors(gatedRuns, liveRootField)},
		{name: "declared field missing beside another validation error", collector: gatedCollector{}, err: apiErrors(gatedRuns, `Unknown type "SearchInput".`), unsupported: true},
		{name: "collector declaring no fields", collector: plainCollector{}, err: apiErrors(gatedRuns)},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := classify(test.collector, test.err)

			if test.err == nil {
				if got != nil {
					t.Fatalf("classify(nil) = %v, want nil", got)
				}
				return
			}

			if errors.Is(got, ErrNotSupported) != test.unsupported {
				t.Errorf("classify(%q) unsupported = %t, want %t", test.err, !test.unsupported, test.unsupported)
			}
			// graphql.Errors is a slice, so compare messages, not values.
			if !test.unsupported && got.Error() != test.err.Error() {
				t.Errorf("classify(%q) = %v, want the error unchanged", test.err, got)
			}
		})
	}
}
