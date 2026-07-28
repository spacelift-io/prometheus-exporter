package main

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestCollectGolden pins the exact exposition output of the collector for each
// deployment shape we support. A diff here is the metric-surface review: any
// change to a metric name, type, label set, HELP string or value shows up as a
// reviewable text diff rather than having to be inferred from Go code.
//
// Regenerate with: go test . -update-golden
func TestCollectGolden(t *testing.T) {
	for _, shape := range []string{"saas", "self-hosted", "empty-account"} {
		t.Run(shape, func(t *testing.T) {
			stub := newGraphQLStub(t, fixture(t, shape))
			assertGolden(t, shape, gather(t, stub.collector(t)))
		})
	}
}

// TestDescribeMatchesCollect asserts that every descriptor announced by
// Describe is actually emitted by Collect, and vice versa.
//
// This is a regression test for a real defect: spacelift_worker_pool_workers
// was declared in Describe but never sent to the metric channel, and
// spacelift_scrape_duration_seconds was emitted but never described. Both were
// fixed in #75. A pedantic registry does not catch either case on its own.
func TestDescribeMatchesCollect(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "saas"))
	collector := stub.collector(t)

	described := make(chan *prometheus.Desc, 256)
	collector.Describe(described)
	close(described)

	describedNames := map[string]bool{}
	for desc := range described {
		describedNames[fqName(t, desc.String())] = true
	}

	collected := make(chan prometheus.Metric, 256)
	collector.Collect(collected)
	close(collected)

	collectedNames := map[string]bool{}
	for metric := range collected {
		collectedNames[fqName(t, metric.Desc().String())] = true
	}

	for name := range describedNames {
		if !collectedNames[name] {
			t.Errorf("%s is announced by Describe but never emitted by Collect", name)
		}
	}

	for name := range collectedNames {
		if !describedNames[name] {
			t.Errorf("%s is emitted by Collect but never announced by Describe", name)
		}
	}
}

// lintBaseline lists the promlint findings that already exist on the shipped
// metric surface. They are grandfathered because renaming a published metric
// breaks every dashboard and alert built on it; they are not a licence to add
// more.
//
// Nothing should ever be added to this map. A new promlint finding means the
// metric being added does not follow Prometheus conventions, and the fix is to
// name it correctly before it ships.
var lintBaseline = map[string]string{
	// Named before the convention was applied. The value is a mean, not a
	// histogram count, so the _count suffix is misleading. Renaming it
	// would break existing consumers.
	"spacelift_current_avg_stack_size_by_resource_count": `non-histogram and non-summary metrics should not have "_count" suffix`,
}

// TestCollectLint enforces the Prometheus naming and unit conventions on
// everything except the grandfathered baseline: base units, _total only on
// counters, no reserved suffixes, consistent HELP.
//
// This is the gate that stops a large metrics PR from shipping convention bugs
// that a human reviewer would have to catch by eye.
func TestCollectLint(t *testing.T) {
	for _, shape := range []string{"saas", "self-hosted", "empty-account"} {
		t.Run(shape, func(t *testing.T) {
			stub := newGraphQLStub(t, fixture(t, shape))

			problems, err := testutil.CollectAndLint(stub.collector(t))
			if err != nil {
				t.Fatalf("linting collector output: %v", err)
			}

			for _, problem := range problems {
				if lintBaseline[problem.Metric] == problem.Text {
					continue
				}
				t.Errorf("promlint: %s: %s", problem.Metric, problem.Text)
			}
		})
	}
}

// TestLintBaselineIsNotStale fails if a grandfathered finding has been fixed,
// so the baseline shrinks as names are corrected and never silently rots.
func TestLintBaselineIsNotStale(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "saas"))

	problems, err := testutil.CollectAndLint(stub.collector(t))
	if err != nil {
		t.Fatalf("linting collector output: %v", err)
	}

	found := map[string]string{}
	for _, problem := range problems {
		found[problem.Metric] = problem.Text
	}

	for metric, text := range lintBaseline {
		if found[metric] != text {
			t.Errorf("%s is in lintBaseline but promlint no longer reports %q; remove the baseline entry", metric, text)
		}
	}
}

// TestQueryShape locks down the GraphQL document the exporter sends. The API
// is expensive for Spacelift to serve for metrics, so the cost of a scrape is
// part of this exporter's contract: a reviewer should be able to see, from a
// test diff, that a change adds a field or a round trip.
func TestQueryShape(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "saas"))

	metrics := make(chan prometheus.Metric, 256)
	stub.collector(t).Collect(metrics)
	close(metrics)

	if got := len(stub.queries); got != 1 {
		t.Errorf("a scrape issued %d GraphQL requests, want exactly 1", got)
	}

	query := stub.lastQuery(t)

	// The operation name lets Spacelift attribute backend cost to the
	// exporter in their own APM.
	if !strings.HasPrefix(query, "query PrometheusExporter{") {
		t.Errorf("query is not named PrometheusExporter: %s", query)
	}

	// Range fields return a bucket per day over a server-chosen window.
	// Prometheus should be given point-in-time values and left to do its
	// own windowing, so none of these belong in a scrape.
	for _, forbidden := range []string{"metricsRange", "Range{", "Range(", "averageRunDurationRange", "stackFailuresRange"} {
		if strings.Contains(query, forbidden) {
			t.Errorf("query selects the windowed field %q; Prometheus must do its own windowing", forbidden)
		}
	}
}

func TestCollectorSurfacesQueryErrors(t *testing.T) {
	for _, shape := range []string{"machine-key", "partial-failure"} {
		t.Run(shape, func(t *testing.T) {
			stub := newGraphQLStub(t, fixture(t, shape))

			metrics := make(chan prometheus.Metric, 256)
			stub.collector(t).Collect(metrics)
			close(metrics)

			var names []string
			for metric := range metrics {
				names = append(names, fqName(t, metric.Desc().String()))
			}

			// Current behaviour: a single failing field costs the whole
			// scrape. Everything except the scrape duration and the error
			// marker is dropped, even when the response carried usable
			// data alongside the error.
			//
			// This assertion is intentionally strict so that changing it
			// is a deliberate, visible act.
			want := map[string]bool{
				"spacelift_scrape_duration_seconds": true,
				"spacelift_error":                   true,
			}

			if len(names) != len(want) {
				t.Errorf("got metrics %v, want exactly %v", names, keys(want))
			}
			for _, name := range names {
				if !want[name] {
					t.Errorf("unexpected metric %q emitted on a failed scrape", name)
				}
			}
		})
	}
}

// TestGatherFailsOnQueryError records that a failed query currently makes the
// whole registry Gather() fail, which is what makes promhttp return HTTP 500
// and drives Prometheus's own up{} series to 0.
func TestGatherFailsOnQueryError(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "machine-key"))

	registry := prometheus.NewPedanticRegistry()
	if err := registry.Register(stub.collector(t)); err != nil {
		t.Fatalf("registering collector: %v", err)
	}

	if _, err := registry.Gather(); err == nil {
		t.Error("Gather() succeeded on a failed query; expected an invalid-metric error")
	}
}

// TestSessionRefreshedOnUnauthorized covers the retry path in client.Query,
// which refreshes the token and reissues the request when the API reports the
// session is no longer valid.
func TestSessionRefreshedOnUnauthorized(t *testing.T) {
	stub := newGraphQLStub(t, `{"errors":[{"message":"unauthorized"}]}`)
	session := &fakeSession{endpoint: stub.server.URL}

	collector := collectorWithSession(t, stub, session)

	metrics := make(chan prometheus.Metric, 256)
	collector.Collect(metrics)
	close(metrics)

	if session.refreshCalls != 1 {
		t.Errorf("RefreshToken called %d times, want 1", session.refreshCalls)
	}

	if len(stub.queries) != 2 {
		t.Errorf("got %d GraphQL requests, want 2 (original plus one retry)", len(stub.queries))
	}
}

// TestRetryPreservesOperationName guards a real bug: the retry in
// client.Query reissues the request without graphql.OperationName, so the
// second attempt reaches Spacelift as an anonymous query and cannot be
// attributed to the exporter in their APM.
func TestRetryPreservesOperationName(t *testing.T) {
	stub := newGraphQLStub(t, `{"errors":[{"message":"unauthorized"}]}`)

	metrics := make(chan prometheus.Metric, 256)
	stub.collector(t).Collect(metrics)
	close(metrics)

	if len(stub.queries) < 2 {
		t.Fatalf("expected a retry, got %d request(s)", len(stub.queries))
	}

	for i, query := range stub.queries {
		if !strings.Contains(query, "PrometheusExporter") {
			t.Errorf("request %d lost the operation name and is anonymous: %s", i+1, query)
		}
	}
}

func keys(in map[string]bool) []string {
	out := make([]string, 0, len(in))
	for k := range in {
		out = append(out, k)
	}

	return out
}
