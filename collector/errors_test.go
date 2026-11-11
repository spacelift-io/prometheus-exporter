package collector

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

// endpointSession points the client at a test server with a fixed token.
type endpointSession string

func (s endpointSession) BearerToken(context.Context) (string, error) { return "token", nil }
func (s endpointSession) Endpoint() string                            { return string(s) }
func (s endpointSession) RefreshToken(context.Context) error          { return nil }

// TestClassifyThroughTheClient serves the API's error responses, as the live
// API sends them (HTTP 200, a GraphQL errors envelope), through the real
// client, so classify sees the error the GraphQL library actually builds from
// the wire rather than one assembled by hand. The messages come from the API's
// schema validation, not from the library, but classification relies on the
// library keeping each message in the error it returns. A library change that
// drops or rewrites them fails here.
func TestClassifyThroughTheClient(t *testing.T) {
	for _, test := range []struct {
		name        string
		collector   Collector
		body        string
		unsupported bool
	}{
		{
			name:        "declared field missing",
			collector:   gatedCollector{},
			body:        `{"errors":[{"message":"Cannot query field \"searchSchedulableRuns\" on type \"WorkerPool\".","locations":[{"line":1,"column":60}]}]}`,
			unsupported: true,
		},
		{
			name:      "undeclared field missing, suggesting declared ones",
			collector: gatedCollector{},
			body:      `{"errors":[{"message":"Cannot query field \"searchSchedulableRunz\" on type \"WorkerPool\". Did you mean \"searchSchedulableRuns\", or \"searchSchedulableRunsSuggestions\"?","locations":[{"line":1,"column":60}]}]}`,
		},
		{
			name:        "machine session",
			collector:   plainCollector{},
			body:        `{"errors":[{"message":"not available for machine sessions"}]}`,
			unsupported: true,
		},
		{
			name:      "genuine failure",
			collector: gatedCollector{},
			body:      `{"errors":[{"message":"internal error"}]}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.body)
			}))
			t.Cleanup(server.Close)

			var query struct {
				Viewer struct {
					ID string `graphql:"id"`
				} `graphql:"viewer"`
			}
			err := client.New(server.Client(), endpointSession(server.URL)).Query(context.Background(), &query, nil)
			if err == nil {
				t.Fatal("the client returned no error for an errors-only response")
			}

			if got := classify(test.collector, err); errors.Is(got, ErrNotSupported) != test.unsupported {
				t.Errorf("classify(%q) unsupported = %t, want %t", err, !test.unsupported, test.unsupported)
			}
		})
	}
}
