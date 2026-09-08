package main

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
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

// TestCollectGoldenPartialFailure pins the exposition output of a partial-mode
// scrape in which one collector fails. This is the only golden that captures a
// failure shape: success=0 for the broken collector, the healthy collectors'
// metrics still present, and the failed collector's own families absent rather
// than zeroed. Strict mode has no golden because a failing strict scrape
// discards the gather entirely; its contract is pinned by
// TestStrictModeGatherErrorNamesTheFailure in the collector package.
func TestCollectGoldenPartialFailure(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "saas"))
	stub.failOperation("PrometheusExporterAggregates", `{"errors":[{"message":"internal error"}]}`)

	assertGolden(t, "partial-failure", gather(t, stub.partialCollector(t)))
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
// counters, no reserved suffixes, consistent HELP. It also fails when a
// baseline entry stops being reported, so the grandfather list can only
// shrink and never silently rots.
//
// One shape suffices: saas emits every family the exporter has, so linting the
// other fixtures adds runtime without adding surface.
func TestCollectLint(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "saas"))

	problems, err := testutil.CollectAndLint(stub.collector(t))
	if err != nil {
		t.Fatalf("linting collector output: %v", err)
	}

	found := map[string]string{}
	for _, problem := range problems {
		found[problem.Metric] = problem.Text
		if lintBaseline[problem.Metric] != problem.Text {
			t.Errorf("promlint: %s: %s", problem.Metric, problem.Text)
		}
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
	queries := stub.recordedQueries()
	defaultCollectors := newCollectors(nil)

	// One request per enabled collector, and no more. Isolation costs
	// requests, so the count is part of the contract with Spacelift's
	// backend and a reviewer should see it change in a diff.
	if got, want := len(queries), len(defaultCollectors); got != want {
		t.Errorf("a scrape issued %d GraphQL requests, want %d (one per enabled collector)", got, want)
	}

	// Every request carries a collector-specific operation name, so API load
	// can be attributed to a subsystem rather than to "the exporter", and each
	// document is pinned exactly: a change to any field selection is a change
	// to the API-cost contract and must show up in this diff.
	expectedQueries := map[string]string{
		"PrometheusExporterPublicWorkerPool": `query PrometheusExporterPublicWorkerPool{publicWorkerPool{parallelism,busyWorkers,pendingRuns}}`,
		"PrometheusExporterWorkerPools":      `query PrometheusExporterWorkerPools{workerPools{id,name,pendingRuns,busyWorkers,workers{id,drained}}}`,
		"PrometheusExporterUsage":            `query PrometheusExporterUsage{usage{billingPeriodStart,billingPeriodEnd,usedPrivateMinutes,usedPublicMinutes,usedSeats}}`,
		"PrometheusExporterAggregates":       `query PrometheusExporterAggregates{metrics{stacksCountByState{value,labels},resourcesCountByDrift{value,labels},avgStackSizeByResourceCount{value,labels},averageRunDuration{value,labels},medianRunDuration{value,labels}}}`,
	}

	seen := map[string]string{}
	for _, query := range queries {
		seen[operationOf(query)] = query
	}

	for operation, want := range expectedQueries {
		got, ok := seen[operation]
		if !ok {
			t.Errorf("no request was named %s; got %v", operation, operationNames(queries))
			continue
		}
		if got != want {
			t.Errorf("%s changed without updating the API-cost contract\nwant: %s\n got: %s", operation, want, got)
		}
	}

	// The envelope operationName must match the document, exactly one per
	// collector, or Spacelift's APM attribution sees anonymous or misattributed
	// queries. Compared as a sorted multiset so a duplicate of one valid name
	// cannot mask another going missing.
	envelopeNames := stub.recordedOperationNames()
	sort.Strings(envelopeNames)

	wantNames := make([]string, 0, len(expectedQueries))
	for name := range expectedQueries {
		wantNames = append(wantNames, name)
	}
	sort.Strings(wantNames)

	if !slices.Equal(envelopeNames, wantNames) {
		t.Errorf("operationName envelope fields = %v, want exactly %v", envelopeNames, wantNames)
	}

	// Range fields return a bucket per day over a server-chosen window.
	// Prometheus should be given point-in-time values and left to do its
	// own windowing, so none of these belong in a scrape.
	for _, query := range queries {
		for _, forbidden := range []string{"metricsRange", "Range{", "Range(", "averageRunDurationRange", "stackFailuresRange"} {
			if strings.Contains(query, forbidden) {
				t.Errorf("query selects the windowed field %q; Prometheus must do its own windowing", forbidden)
			}
		}
	}
}

// TestCollectorsAreIsolated is the point of the refactor: one failing domain
// must not suppress the others.
//
// The stub fails the aggregates request while every other collector receives a
// healthy response. A scrape should still produce the working collectors'
// metrics and report precisely which collector did not work.
func TestCollectorsAreIsolated(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "saas"))
	stub.failOperation("PrometheusExporterAggregates", fixture(t, "partial-failure"))

	output := gather(t, stub.partialCollector(t))

	// The failing collector is reported, and only it.
	for _, want := range []string{
		`spacelift_scrape_collector_success{collector="aggregates"} 0`,
		`spacelift_scrape_collector_success{collector="workerpools"} 1`,
		`spacelift_scrape_collector_success{collector="usage"} 1`,
		`spacelift_scrape_collector_success{collector="publicworkerpool"} 1`,
	} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q in:\n%s", want, output)
		}
	}

	// The healthy collectors still produced their metrics.
	for _, want := range []string{
		"spacelift_worker_pool_runs_pending{",
		"spacelift_current_billing_period_used_seats ",
		"spacelift_public_worker_pool_parallelism ",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("a failure in one collector suppressed %q:\n%s", want, output)
		}
	}

	// The failing collector's metrics are absent rather than zeroed, so a
	// stale value is never mistaken for a live one.
	if strings.Contains(output, "spacelift_current_stacks_count_by_state{") {
		t.Error("the failed collector emitted metrics anyway")
	}
}

func TestMetricsHandlerPartialScrapePolicy(t *testing.T) {
	for _, test := range []struct {
		name            string
		partialScrapes  bool
		failAll         bool
		wantStatus      int
		wantPartialBody bool
	}{
		{
			name:       "legacy behavior is the default",
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:            "partial scrapes are opt in",
			partialScrapes:  true,
			wantStatus:      http.StatusOK,
			wantPartialBody: true,
		},
		{
			name:           "complete failure stays visible in partial mode",
			partialScrapes: true,
			failAll:        true,
			wantStatus:     http.StatusInternalServerError,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := newGraphQLStub(t, fixture(t, "saas"))
			if test.failAll {
				stub.response = `{"errors":[{"message":"internal error"}]}`
			} else {
				stub.failOperation(
					"PrometheusExporterAggregates",
					`{"errors":[{"message":"internal error"}]}`,
				)
			}

			registry := prometheus.NewPedanticRegistry()
			collector := stub.collectorWithPartialScrapes(t, test.partialScrapes)
			if err := registry.Register(collector); err != nil {
				t.Fatalf("registering collector: %v", err)
			}

			request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			response := httptest.NewRecorder()
			newMetricsHandler(registry).ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("HTTP status = %d, want %d; body:\n%s",
					response.Code, test.wantStatus, response.Body.String())
			}
			if test.wantPartialBody {
				for _, want := range []string{
					`spacelift_scrape_collector_success{collector="aggregates"} 0`,
					"spacelift_worker_pool_runs_pending{",
				} {
					if !strings.Contains(response.Body.String(), want) {
						t.Errorf("partial response is missing %q:\n%s", want, response.Body.String())
					}
				}
			}
		})
	}
}

// legacyMetrics are the 19 families the exporter shipped before per-collector
// isolation. Every one must still be emitted.
//
// This exists because "the golden diff has 0 deletions" does not prove what it
// looks like it proves: a metric that stops being emitted in every fixture
// disappears from all of them, and a golden regeneration would happily record
// its absence. This asserts presence directly.
var legacyMetrics = []string{
	"spacelift_public_worker_pool_runs_pending",
	"spacelift_public_worker_pool_workers_busy",
	"spacelift_public_worker_pool_parallelism",
	"spacelift_worker_pool_runs_pending",
	"spacelift_worker_pool_workers_busy",
	"spacelift_worker_pool_workers",
	"spacelift_worker_pool_workers_drained",
	"spacelift_current_billing_period_start_timestamp_seconds",
	"spacelift_current_billing_period_end_timestamp_seconds",
	"spacelift_current_billing_period_used_private_seconds",
	"spacelift_current_billing_period_used_public_seconds",
	"spacelift_current_billing_period_used_seats",
	"spacelift_current_stacks_count_by_state",
	"spacelift_current_resources_count_by_drift",
	"spacelift_current_avg_stack_size_by_resource_count",
	"spacelift_current_average_run_duration",
	"spacelift_current_median_run_duration",
	"spacelift_scrape_duration_seconds",
	"spacelift_build_info",
}

// TestLegacyMetricsStillEmitted fails if any pre-existing metric family stops
// being exported, independently of what the golden files happen to contain.
func TestLegacyMetricsStillEmitted(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "saas"))
	output := gather(t, stub.collector(t))

	for _, name := range legacyMetrics {
		if !strings.Contains(output, "\n"+name+"{") && !strings.Contains(output, "\n"+name+" ") {
			t.Errorf("%s is no longer emitted; removing a shipped metric breaks every dashboard built on it", name)
		}
	}

	if got, want := len(legacyMetrics), 19; got != want {
		t.Errorf("legacyMetrics has %d entries, want %d; the shipped surface should not change", got, want)
	}
}

// TestGatedBackendDegradesGracefully covers backends that reject machine
// sessions on publicWorkerPool, usage and metrics while serving workerPools.
// Spacelift SaaS removed those gates in August 2026 (backend #16058), but
// Self-Hosted releases older than that still have them, and under the old
// single query they meant no metrics at all.
func TestGatedBackendDegradesGracefully(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "saas"))
	machineError := `{"errors":[{"message":"not available for machine sessions"}]}`
	for _, operation := range []string{
		"PrometheusExporterPublicWorkerPool",
		"PrometheusExporterUsage",
		"PrometheusExporterAggregates",
	} {
		stub.failOperation(operation, machineError)
	}

	output := gather(t, stub.partialCollector(t))

	// Gated collectors report unsupported, not failed: the key is fine,
	// the data simply is not available to it.
	for _, name := range []string{"publicworkerpool", "usage", "aggregates"} {
		for _, want := range []string{
			`spacelift_scrape_collector_supported{collector="` + name + `"} 0`,
			`spacelift_scrape_collector_success{collector="` + name + `"} 1`,
		} {
			if !strings.Contains(output, want) {
				t.Errorf("missing %q in:\n%s", want, output)
			}
		}
	}

	// And the ungated collector still works, which is the whole point.
	if !strings.Contains(output, "spacelift_worker_pool_runs_pending{") {
		t.Errorf("worker pool metrics should still be collected on a gated backend:\n%s", output)
	}
}

// The retry-path coverage that lived here (session refresh on unauthorized,
// operation-name preservation across retries) moved to client/client_test.go,
// where it also proves the refreshed bearer token is actually used and that
// concurrent unauthorized responses trigger only one refresh.
func operationNames(queries []string) []string {
	out := make([]string, 0, len(queries))
	for _, q := range queries {
		out = append(out, operationOf(q))
	}

	return out
}
