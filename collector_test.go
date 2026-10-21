package main

import (
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
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

// TestCollectGoldenAllCollectors pins the output with every opt-in collector
// enabled as well. The default-set goldens above must not change when an
// opt-in collector is added; this one is where its metric surface is reviewed.
//
// new-account is a brand-new account: one registered pool with no workers yet,
// no stacks and nothing queued. It pins what the opt-in collectors emit when
// there is nothing to report.
func TestCollectGoldenAllCollectors(t *testing.T) {
	for _, shape := range []string{"saas", "new-account"} {
		t.Run(shape, func(t *testing.T) {
			stub := newGraphQLStub(t, fixture(t, shape))
			assertGolden(t, shape+"-all-collectors", gather(t, stub.allCollectors(t)))
		})
	}
}

// TestNewAccountAllCollectors states the empty-account contract the
// new-account golden pins: series that describe a pool are emitted for an
// empty one, and series that describe a stack or a run are absent when there
// is none.
func TestNewAccountAllCollectors(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "new-account"))
	output := gather(t, stub.allCollectors(t))

	const pool = `worker_pool_id="01HQZXE1B2C3D4E5F6G7H8J9K0",worker_pool_name="first-pool"`
	want := []string{
		`spacelift_worker_pool_workers{` + pool + `} 0`,
		`spacelift_worker_pool_schedulable_runs_truncated{` + pool + `} 0`,
		`spacelift_public_worker_pool_schedulable_runs_truncated 0`,
	}
	for _, status := range []string{"busy", "drained", "draining", "idle"} {
		want = append(want, `spacelift_worker_pool_workers_by_status{status="`+status+`",`+pool+`} 0`)
	}
	for _, line := range want {
		if !strings.Contains(output, line) {
			t.Errorf("missing %q in:\n%s", line, output)
		}
	}

	for _, family := range []string{
		"spacelift_stack_",
		"spacelift_worker_pool_schedulable_runs{",
		"spacelift_worker_pool_oldest_schedulable_run_created_timestamp_seconds",
		"spacelift_public_worker_pool_schedulable_runs{",
		"spacelift_public_worker_pool_oldest_schedulable_run_created_timestamp_seconds",
	} {
		if strings.Contains(output, "\n"+family) {
			t.Errorf("an empty account emitted %s series:\n%s", family, output)
		}
	}
}

// TestCollectGoldenPartialFailure pins the exposition output of a partial-mode
// scrape in which one collector fails: success=0 for the broken collector, the
// healthy collectors' metrics present, and the failed collector's own families
// absent rather than zeroed. Strict mode has no failure golden because a
// failing strict scrape discards the gather entirely; that contract is pinned
// by TestGatherErrorNamesTheFailingCollector.
func TestCollectGoldenPartialFailure(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "saas"))
	stub.failOperation("PrometheusExporter_Aggregates", `{"errors":[{"message":"internal error"}]}`)

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
	collector := stub.allCollectors(t)

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
	for _, test := range []struct {
		name, shape string
		all         bool
	}{
		{name: "saas", shape: "saas"},
		{name: "self-hosted", shape: "self-hosted"},
		{name: "empty-account", shape: "empty-account"},
		{name: "saas-all-collectors", shape: "saas", all: true},
		{name: "new-account-all-collectors", shape: "new-account", all: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := newGraphQLStub(t, fixture(t, test.shape))

			collector := stub.collector(t)
			if test.all {
				collector = stub.allCollectors(t)
			}

			problems, err := testutil.CollectAndLint(collector)
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

	problems, err := testutil.CollectAndLint(stub.allCollectors(t))
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

// TestQueryShape locks down the GraphQL documents the exporter sends. The API
// is expensive for Spacelift to serve for metrics, so the cost of a scrape is
// part of this exporter's contract: a reviewer should be able to see, from a
// test diff, that a change adds a field or a round trip.
func TestQueryShape(t *testing.T) {
	// Each document is pinned exactly, and each carries a collector-specific
	// operation name so that Spacelift can attribute API load to the part of
	// the exporter responsible.
	documents := map[string]string{
		"PrometheusExporter_PublicWorkerPool": `query PrometheusExporter_PublicWorkerPool{publicWorkerPool{parallelism,busyWorkers,pendingRuns}}`,
		"PrometheusExporter_WorkerPools":      `query PrometheusExporter_WorkerPools{workerPools{id,name,pendingRuns,busyWorkers,workers{id,drained}}}`,
		"PrometheusExporter_Usage":            `query PrometheusExporter_Usage{usage{billingPeriodStart,billingPeriodEnd,usedPrivateMinutes,usedPublicMinutes,usedSeats}}`,
		"PrometheusExporter_Aggregates":       `query PrometheusExporter_Aggregates{metrics{stacksCountByState{value,labels},resourcesCountByDrift{value,labels},avgStackSizeByResourceCount{value,labels},averageRunDuration{value,labels},medianRunDuration{value,labels}}}`,
		"PrometheusExporter_WorkerStatus":     `query PrometheusExporter_WorkerStatus{workerPools{id,name,workers{busy,drained}}}`,
		"PrometheusExporter_Stacks":           `query PrometheusExporter_Stacks{stacks{id,name,space,state,stateSetAt,isDisabled,lockedBy}}`,
		"PrometheusExporter_Queue":            `query PrometheusExporter_Queue($input:SearchInput!$total:SearchSuggestionsInput!){workerPools{id,name,searchSchedulableRuns(input:$input){edges{node{run{createdAt,state,type}}},pageInfo{hasNextPage}},searchSchedulableRunsSuggestions(input:$total){filteredCount}}}`,
		"PrometheusExporter_PublicQueue":      `query PrometheusExporter_PublicQueue($input:SearchInput!$total:SearchSuggestionsInput!){publicWorkerPool{searchSchedulableRuns(input:$input){edges{node{run{createdAt,state,type}}},pageInfo{hasNextPage}},searchSchedulableRunsSuggestions(input:$total){filteredCount}}}`,
	}

	// The variables each document sends, "" for none. The queue searches ask
	// for their runs oldest first, and for the total with no per-field
	// suggestions, which keeps that second call cheap; neither is visible in
	// the document text. They set no page size because the API ignores it.
	const queueVariables = `{"input":{"orderBy":[{"field":"createdAt","direction":"ASC"}]},"total":{"fields":[]}}`
	variables := map[string]string{
		"PrometheusExporter_Queue":       queueVariables,
		"PrometheusExporter_PublicQueue": queueVariables,
	}

	defaults := []string{
		"PrometheusExporter_PublicWorkerPool",
		"PrometheusExporter_WorkerPools",
		"PrometheusExporter_Usage",
		"PrometheusExporter_Aggregates",
	}

	for _, test := range []struct {
		name       string
		enabled    map[string]bool
		operations []string
	}{
		{name: "defaults", operations: defaults},
		{name: "all collectors", enabled: allCollectorsEnabled(), operations: slices.Collect(maps.Keys(documents))},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := newGraphQLStub(t, fixture(t, "saas"))

			metrics := make(chan prometheus.Metric, 256)
			stub.exporter(t, test.enabled, false).Collect(metrics)
			close(metrics)
			queries, operationNames := stub.recorded()

			// One request per collector, and no more.
			if got, want := len(queries), len(newCollectors(test.enabled)); got != want {
				t.Errorf("a scrape issued %d GraphQL requests, want %d (one per collector)", got, want)
			}

			seen := map[string]string{}
			for _, query := range queries {
				seen[operationOf(query)] = query
			}

			for _, operation := range test.operations {
				want := documents[operation]
				got, ok := seen[operation]
				if !ok {
					t.Errorf("no request was named %s; got %v", operation, operationNames)
					continue
				}
				if got != want {
					t.Errorf("%s changed without updating the API-cost contract\nwant: %s\n got: %s", operation, want, got)
				}
			}

			sent := stub.recordedVariables()
			for _, operation := range test.operations {
				if got, want := sent[operation], variables[operation]; got != want {
					t.Errorf("%s sent variables %s, want %s", operation, got, want)
				}
			}

			// The envelope operationName must match the document, exactly
			// once per collector. Compared as a sorted multiset so a
			// duplicate of one valid name cannot mask another going missing.
			envelopeNames := slices.Sorted(slices.Values(operationNames))
			wantNames := slices.Sorted(slices.Values(test.operations))
			if !slices.Equal(envelopeNames, wantNames) {
				t.Errorf("operationName envelope fields = %v, want exactly %v", envelopeNames, wantNames)
			}

			// Range fields return a bucket per day over a server-chosen
			// window. Prometheus should be given point-in-time values and
			// left to do its own windowing, so none of these belong in a
			// scrape.
			for _, query := range queries {
				for _, forbidden := range []string{"metricsRange", "Range{", "Range(", "averageRunDurationRange", "stackFailuresRange"} {
					if strings.Contains(query, forbidden) {
						t.Errorf("query selects the windowed field %q; Prometheus must do its own windowing", forbidden)
					}
				}
			}
		})
	}
}

// TestGatherErrorNamesTheFailingCollector fails one collector's document and
// leaves the rest healthy. The scrape still fails as a whole, as it always
// has, and the Gather() error, which is the HTTP 500 body an operator sees,
// names the collector that broke.
func TestGatherErrorNamesTheFailingCollector(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "saas"))
	stub.failOperation("PrometheusExporter_Aggregates", `{"errors":[{"message":"internal error"}]}`)

	registry := prometheus.NewPedanticRegistry()
	if err := registry.Register(stub.collector(t)); err != nil {
		t.Fatalf("registering collector: %v", err)
	}

	_, err := registry.Gather()
	if err == nil {
		t.Fatal("Gather() succeeded with a failing collector; expected an invalid-metric error")
	}
	for _, want := range []string{"spacelift_error", "aggregates", "internal error"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Gather() error %q does not mention %q", err, want)
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
				name := fqName(t, metric.Desc().String())
				if !slices.Contains(names, name) {
					names = append(names, name)
				}
			}

			// Current behaviour: a failing collector costs the whole
			// scrape. Everything except the scrape and per-collector
			// health series and the error marker is dropped, even when
			// other collectors returned usable data.
			//
			// This assertion is intentionally strict so that changing it
			// is a deliberate, visible act.
			want := map[string]bool{
				"spacelift_scrape_duration_seconds":           true,
				"spacelift_scrape_collector_duration_seconds": true,
				"spacelift_scrape_collector_success":          true,
				"spacelift_scrape_collector_supported":        true,
				"spacelift_error":                             true,
			}

			if len(names) != len(want) {
				t.Errorf("got metrics %v, want exactly %v", names, slices.Sorted(maps.Keys(want)))
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

// TestCollectorsAreIsolated is the point of one document per collector: one
// failing domain must not suppress the others. With partial scrapes enabled,
// the stub fails the aggregates request while every other collector receives a
// healthy response.
func TestCollectorsAreIsolated(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "saas"))
	stub.failOperation("PrometheusExporter_Aggregates", fixture(t, "partial-failure"))

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

// TestMetricsHandlerPolicy is the HTTP-level contract of both scrape modes.
func TestMetricsHandlerPolicy(t *testing.T) {
	const (
		internalError = `{"errors":[{"message":"internal error"}]}`
		gatedError    = `{"errors":[{"message":"not available for machine sessions"}]}`
	)

	for _, test := range []struct {
		name           string
		partialScrapes bool
		// responses overrides the aggregates operation, or every operation
		// when failAll is set.
		response   string
		failAll    bool
		wantStatus int
		wantBody   []string
	}{
		{
			name:       "strict: one failed collector fails the scrape",
			response:   internalError,
			wantStatus: http.StatusInternalServerError,
			wantBody:   []string{"spacelift_error", "aggregates: "},
		},
		{
			name:       "strict: one unsupported collector fails the scrape",
			response:   gatedError,
			wantStatus: http.StatusInternalServerError,
			wantBody:   []string{"spacelift_error", "aggregates: not supported"},
		},
		{
			name:           "partial: one failed collector is served around",
			partialScrapes: true,
			response:       internalError,
			wantStatus:     http.StatusOK,
			wantBody: []string{
				`spacelift_scrape_collector_success{collector="aggregates"} 0`,
				"spacelift_worker_pool_runs_pending{",
			},
		},
		{
			name:           "partial: an unsupported collector is healthy but unavailable",
			partialScrapes: true,
			response:       gatedError,
			wantStatus:     http.StatusOK,
			wantBody: []string{
				`spacelift_scrape_collector_success{collector="aggregates"} 1`,
				`spacelift_scrape_collector_supported{collector="aggregates"} 0`,
			},
		},
		{
			name:           "partial: every collector failing is still a failed scrape",
			partialScrapes: true,
			response:       internalError,
			failAll:        true,
			wantStatus:     http.StatusInternalServerError,
			wantBody:       []string{"spacelift_error"},
		},
		{
			// The flag's contract is that a scrape in which no collector
			// succeeds returns HTTP 500. That must hold when everything is
			// merely unsupported too, or a fully gated backend reads as a
			// healthy target exporting zero data.
			name:           "partial: every collector unsupported is still a failed scrape",
			partialScrapes: true,
			response:       gatedError,
			failAll:        true,
			wantStatus:     http.StatusInternalServerError,
			wantBody:       []string{"spacelift_error", "not supported"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := newGraphQLStub(t, fixture(t, "saas"))
			if test.failAll {
				stub.response = test.response
			} else {
				stub.failOperation("PrometheusExporter_Aggregates", test.response)
			}

			registry := prometheus.NewPedanticRegistry()
			if err := registry.Register(stub.collectorWithPartialScrapes(t, test.partialScrapes)); err != nil {
				t.Fatalf("registering collector: %v", err)
			}

			response := httptest.NewRecorder()
			newMetricsHandler(registry).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))

			if response.Code != test.wantStatus {
				t.Fatalf("HTTP status = %d, want %d; body:\n%s", response.Code, test.wantStatus, response.Body.String())
			}
			for _, want := range test.wantBody {
				if !strings.Contains(response.Body.String(), want) {
					t.Errorf("response is missing %q:\n%s", want, response.Body.String())
				}
			}
		})
	}
}

// TestGatedBackendDegradesGracefully covers backends that reject machine
// sessions on publicWorkerPool, usage and metrics while serving workerPools,
// which is how Self-Hosted releases older than August 2026 behave. Under the
// old single document they meant no metrics at all.
func TestGatedBackendDegradesGracefully(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "saas"))
	for _, operation := range []string{
		"PrometheusExporter_PublicWorkerPool",
		"PrometheusExporter_Usage",
		"PrometheusExporter_Aggregates",
	} {
		stub.failOperation(operation, `{"errors":[{"message":"not available for machine sessions"}]}`)
	}

	output := gather(t, stub.partialCollector(t))

	// Gated collectors report unsupported, not failed: the key is fine, the
	// data simply is not available to it.
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

	// And the ungated collector still works.
	if !strings.Contains(output, "spacelift_worker_pool_runs_pending{") {
		t.Errorf("worker pool metrics should still be collected on a gated backend:\n%s", output)
	}
}

// TestQueueWithoutAnAnswer covers the nullable search and total. A pool whose
// search or total the API returns as null, without an error, must report
// nothing rather than an empty queue: a truncated=0 series would claim the
// pool's queue was read in full.
func TestQueueWithoutAnAnswer(t *testing.T) {
	for _, test := range []struct {
		name, publicQueue string
	}{
		{name: "null search", publicQueue: `{"data":{"publicWorkerPool":{"searchSchedulableRuns":null,"searchSchedulableRunsSuggestions":{"filteredCount":0}}}}`},
		{name: "null total", publicQueue: `{"data":{"publicWorkerPool":{"searchSchedulableRuns":{"edges":[],"pageInfo":{"hasNextPage":false}},"searchSchedulableRunsSuggestions":null}}}`},
		{name: "null public pool", publicQueue: `{"data":{"publicWorkerPool":null}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := newGraphQLStub(t, fixture(t, "saas"))
			stub.failOperation("PrometheusExporter_Queue", `{"data":{"workerPools":[`+
				`{"id":"pool-unanswered","name":"unanswered","searchSchedulableRuns":null,"searchSchedulableRunsSuggestions":{"filteredCount":0}},`+
				`{"id":"pool-uncounted","name":"uncounted","searchSchedulableRuns":{"edges":[],"pageInfo":{"hasNextPage":false}},"searchSchedulableRunsSuggestions":null}]}}`)
			stub.failOperation("PrometheusExporter_PublicQueue", test.publicQueue)

			output := gather(t, stub.allCollectors(t))

			for _, want := range []string{
				`spacelift_scrape_collector_success{collector="queue"} 1`,
				`spacelift_scrape_collector_success{collector="publicqueue"} 1`,
			} {
				if !strings.Contains(output, want) {
					t.Errorf("missing %q in:\n%s", want, output)
				}
			}

			for _, unwanted := range []string{"pool-unanswered", "pool-uncounted", "schedulable"} {
				if strings.Contains(output, unwanted) {
					t.Errorf("a search with no answer still produced series mentioning %q:\n%s", unwanted, output)
				}
			}
		})
	}
}

// queueResponse renders a search result of n READY runs, created one second
// apart from first, with the given total and hasNextPage false, as the API
// returns it whatever the result's size.
func queueResponse(n int, first int64, total int) string {
	edges := make([]string, 0, n)
	for i := range n {
		edges = append(edges, fmt.Sprintf(`{"node":{"run":{"createdAt":%d,"state":"READY","type":"TRACKED"}}}`, first+int64(i)))
	}

	return fmt.Sprintf(`"searchSchedulableRuns":{"edges":[%s],"pageInfo":{"hasNextPage":false}},"searchSchedulableRunsSuggestions":{"filteredCount":%d}`,
		strings.Join(edges, ","), total)
}

// TestQueueBoundariesEndToEnd runs the API's truncation boundary through the
// real client and exporter, so that the total is decoded from the field the
// document selects and reaches the metrics. TestSummarizeQueueBoundaries
// covers the arithmetic at more sizes.
func TestQueueBoundariesEndToEnd(t *testing.T) {
	const firstCreated = 1752700000

	for _, test := range []struct {
		name      string
		total     int
		truncated bool
	}{
		{name: "250 of 250", total: 250},
		{name: "250 of 251", total: 251, truncated: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := queueResponse(250, firstCreated, test.total)
			stub := newGraphQLStub(t, fixture(t, "saas"))
			stub.failOperation("PrometheusExporter_Queue",
				`{"data":{"workerPools":[{"id":"pool-big","name":"big",`+body+`}]}}`)
			stub.failOperation("PrometheusExporter_PublicQueue", `{"data":{"publicWorkerPool":{`+body+`}}}`)

			output := gather(t, stub.allCollectors(t))

			want := map[bool]string{false: "0", true: "1"}[test.truncated]
			for _, line := range []string{
				`spacelift_worker_pool_schedulable_runs_truncated{worker_pool_id="pool-big",worker_pool_name="big"} ` + want,
				`spacelift_public_worker_pool_schedulable_runs_truncated ` + want,
				`spacelift_worker_pool_schedulable_runs{state="READY",type="TRACKED",worker_pool_id="pool-big",worker_pool_name="big"} 250`,
				`spacelift_public_worker_pool_schedulable_runs{state="READY",type="TRACKED"} 250`,
			} {
				if !strings.Contains(output, line) {
					t.Errorf("missing %q in:\n%s", line, output)
				}
			}

			for _, family := range []string{
				"spacelift_worker_pool_oldest_schedulable_run_created_timestamp_seconds{",
				"spacelift_public_worker_pool_oldest_schedulable_run_created_timestamp_seconds ",
			} {
				if got := strings.Contains(output, family); got == test.truncated {
					t.Errorf("%s present = %t, want %t", strings.TrimRight(family, "{ "), got, !test.truncated)
				}
			}
		})
	}
}

// TestOptInCollectorFailurePolicy is TestMetricsHandlerPolicy for each opt-in
// collector, with every collector enabled: one opt-in failing or gated while
// the rest stay healthy. Strict mode fails the scrape and names it; partial
// mode serves everything else and reports it on its own health series, with
// its families absent rather than zeroed.
func TestOptInCollectorFailurePolicy(t *testing.T) {
	const (
		internalError = `{"errors":[{"message":"internal error"}]}`
		gatedError    = `{"errors":[{"message":"not available for machine sessions"}]}`
		// What a Self-Hosted release older than a field returns for it.
		missingField = `{"errors":[{"message":"Cannot query field \"searchSchedulableRuns\" on type \"WorkerPool\".","extensions":{"code":"GRAPHQL_VALIDATION_FAILED"}}]}`
	)

	for _, collector := range []struct {
		name, operation string
		// family is a series prefix only this collector emits.
		family string
	}{
		{name: "workerstatus", operation: "PrometheusExporter_WorkerStatus", family: "spacelift_worker_pool_workers_by_status"},
		{name: "stacks", operation: "PrometheusExporter_Stacks", family: "spacelift_stack_"},
		{name: "queue", operation: "PrometheusExporter_Queue", family: "spacelift_worker_pool_schedulable_runs"},
		{name: "publicqueue", operation: "PrometheusExporter_PublicQueue", family: "spacelift_public_worker_pool_schedulable_runs"},
	} {
		for _, test := range []struct {
			mode           string
			partialScrapes bool
			response       string
			wantStatus     int
			// wantSuccess and wantSupported are the failing collector's
			// health values; empty in strict mode, whose body is the error.
			wantSuccess, wantSupported string
		}{
			{mode: "strict failed", response: internalError, wantStatus: http.StatusInternalServerError},
			{mode: "strict gated", response: gatedError, wantStatus: http.StatusInternalServerError},
			{mode: "partial failed", partialScrapes: true, response: internalError, wantStatus: http.StatusOK, wantSuccess: "0", wantSupported: "1"},
			{mode: "partial gated", partialScrapes: true, response: gatedError, wantStatus: http.StatusOK, wantSuccess: "1", wantSupported: "0"},
			{mode: "strict missing field", response: missingField, wantStatus: http.StatusInternalServerError},
			{mode: "partial missing field", partialScrapes: true, response: missingField, wantStatus: http.StatusOK, wantSuccess: "1", wantSupported: "0"},
		} {
			t.Run(collector.name+"/"+test.mode, func(t *testing.T) {
				stub := newGraphQLStub(t, fixture(t, "saas"))
				stub.failOperation(collector.operation, test.response)

				registry := prometheus.NewPedanticRegistry()
				if err := registry.Register(stub.exporter(t, allCollectorsEnabled(), test.partialScrapes)); err != nil {
					t.Fatalf("registering collector: %v", err)
				}

				response := httptest.NewRecorder()
				newMetricsHandler(registry).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
				body := response.Body.String()

				if response.Code != test.wantStatus {
					t.Fatalf("HTTP status = %d, want %d; body:\n%s", response.Code, test.wantStatus, body)
				}

				if !test.partialScrapes {
					for _, want := range []string{"spacelift_error", collector.name + ": "} {
						if !strings.Contains(body, want) {
							t.Errorf("strict failure body is missing %q:\n%s", want, body)
						}
					}

					return
				}

				want := []string{
					`spacelift_scrape_collector_success{collector="` + collector.name + `"} ` + test.wantSuccess,
					`spacelift_scrape_collector_supported{collector="` + collector.name + `"} ` + test.wantSupported,
					"spacelift_worker_pool_runs_pending{",
				}
				for _, spec := range collectorSpecs {
					if spec.name != collector.name {
						want = append(want, `spacelift_scrape_collector_success{collector="`+spec.name+`"} 1`)
					}
				}
				for _, line := range want {
					if !strings.Contains(body, line) {
						t.Errorf("missing %q in:\n%s", line, body)
					}
				}

				if strings.Contains(body, "\n"+collector.family) {
					t.Errorf("%s emitted %s series although it did not succeed:\n%s", collector.name, collector.family, body)
				}
			})
		}
	}
}
