package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spacelift-io/prometheus-exporter/collector"
)

// TestCollectGoldenAllCollectors gathers every collector, the opt-in ones
// included, on one pedantic registry, so a family or label set that collides
// across collectors fails here. The golden holds only the opt-in collectors'
// own families: that file is where their metric surface is reviewed. The rest
// of the output must equal a default scrape, so enabling an opt-in collector
// never changes a default series.
//
// new-account is a brand-new account: one registered pool with no workers yet.
//
// Regenerate with: go test . -update-golden
func TestCollectGoldenAllCollectors(t *testing.T) {
	var optInHealth []string
	for _, spec := range collectorSpecs {
		if !spec.defaultEnabled {
			optInHealth = append(optInHealth, `{collector="`+spec.name+`"}`)
		}
	}

	for _, shape := range []string{"saas", "self-hosted", "new-account"} {
		t.Run(shape, func(t *testing.T) {
			stub := newGraphQLStub(t, fixture(t, shape))
			optIn, rest := splitFamilies(gather(t, stub.allCollectors(t)), optInFamilies(t))
			assertGolden(t, shape+"-optin", optIn)

			var defaults strings.Builder
			for line := range strings.Lines(rest) {
				if !slices.ContainsFunc(optInHealth, func(health string) bool { return strings.Contains(line, health) }) {
					defaults.WriteString(line)
				}
			}
			if want := gather(t, stub.collector(t)); defaults.String() != want {
				t.Errorf("enabling the opt-in collectors changed the default series\nwant:\n%s\n got:\n%s", want, defaults.String())
			}
		})
	}
}

// TestNewAccountAllCollectors states the empty-account contract: series that
// describe a pool are emitted for an empty one, with zero values rather than
// gaps, and families with nothing to count are absent rather than zeroed.
func TestNewAccountAllCollectors(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "new-account"))
	output := gather(t, stub.allCollectors(t))

	const pool = `worker_pool_id="01HQZXE1B2C3D4E5F6G7H8J9K0",worker_pool_name="first-pool"`
	present := []string{
		`spacelift_worker_pool_runs_pending{` + pool + `} 0`,
		`spacelift_worker_pool_workers{` + pool + `} 0`,
		`spacelift_worker_pool_workers_busy{` + pool + `} 0`,
		`spacelift_worker_pool_workers_drained{` + pool + `} 0`,
	}
	for _, status := range []string{"busy", "drained", "draining", "idle"} {
		present = append(present, `spacelift_worker_pool_workers_by_status{status="`+status+`",`+pool+`} 0`)
	}
	for _, line := range present {
		if !strings.Contains(output, line) {
			t.Errorf("missing %q in:\n%s", line, output)
		}
	}

	absent := []string{
		"spacelift_current_stacks_count_by_state{",
		"spacelift_stack_info{",
		"spacelift_stack_state{",
		"spacelift_stack_state_set_timestamp_seconds{",
		"spacelift_stack_disabled{",
		"spacelift_stack_locked{",
	}
	for _, series := range absent {
		if strings.Contains(output, series) {
			t.Errorf("%s is emitted for an account with nothing to count:\n%s", series, output)
		}
	}
}

// TestWorkerStatusMatchesWorkerPools pins how the status breakdown relates to
// the default pool series, for every pool in every shape. The fixtures answer
// both requests with the same workers, so draining plus drained equals
// spacelift_worker_pool_workers_drained, and busy plus draining equals
// spacelift_worker_pool_workers_busy when the API's busyWorkers count agrees
// with the workers' busy flags, as it does in every fixture.
func TestWorkerStatusMatchesWorkerPools(t *testing.T) {
	for _, shape := range []string{"saas", "self-hosted", "new-account"} {
		t.Run(shape, func(t *testing.T) {
			stub := newGraphQLStub(t, fixture(t, shape))
			output := gather(t, stub.allCollectors(t))

			values := map[string]float64{}
			for line := range strings.Lines(output) {
				series, value, _ := strings.Cut(strings.TrimSpace(line), " ")
				if number, err := strconv.ParseFloat(value, 64); err == nil {
					values[series] = number
				}
			}

			pools := 0
			for series, drained := range values {
				pool, ok := strings.CutPrefix(series, "spacelift_worker_pool_workers_drained{")
				if !ok {
					continue
				}
				pools++

				status := func(name string) float64 {
					series := `spacelift_worker_pool_workers_by_status{status="` + name + `",` + pool
					if _, ok := values[series]; !ok {
						t.Errorf("missing %s", series)
					}
					return values[series]
				}
				if got := status("draining") + status("drained"); got != drained {
					t.Errorf("draining + drained = %v, want spacelift_worker_pool_workers_drained{%s = %v", got, pool, drained)
				}
				if got, want := status("busy")+status("draining"), values["spacelift_worker_pool_workers_busy{"+pool]; got != want {
					t.Errorf("busy + draining = %v, want spacelift_worker_pool_workers_busy{%s = %v", got, pool, want)
				}
			}
			if pools == 0 {
				t.Fatalf("no spacelift_worker_pool_workers_drained series in:\n%s", output)
			}
		})
	}
}

// TestOptInCollectorFailurePolicy is TestMetricsHandlerPolicy for each opt-in
// collector, with every collector enabled: one opt-in failing or gated while
// the rest stay healthy. Strict mode fails the scrape and names it; partial
// mode serves everything else and reports it on its own health series, with
// its families absent rather than zeroed. A field missing from the schema is
// unsupported only when the collector declares it version-gated, and a
// failure otherwise.
func TestOptInCollectorFailurePolicy(t *testing.T) {
	const (
		internalError = `{"errors":[{"message":"internal error"}]}`
		gatedError    = `{"errors":[{"message":"not available for machine sessions"}]}`
	)

	// Every opt-in collector needs a row.
	optIn := map[string]struct {
		operation string
		// missingField, on type onType, is a field the document selects;
		// gated is whether the collector declares it version-gated.
		missingField, onType string
		gated                bool
	}{
		"workerstatus": {operation: "PrometheusExporter_WorkerStatus", missingField: "busy", onType: "Worker"},
		"stacks":       {operation: "PrometheusExporter_Stacks", missingField: "stateSetAt", onType: "Stack"},
	}

	for _, spec := range collectorSpecs {
		if spec.defaultEnabled {
			continue
		}
		row, ok := optIn[spec.name]
		if !ok {
			t.Errorf("opt-in collector %s has no row", spec.name)
			continue
		}

		// The shape the live API returns, with HTTP 200, for a document
		// selecting a field its schema lacks.
		missingField := fmt.Sprintf(`{"errors":[{"message":"Cannot query field \"%s\" on type \"%s\".","locations":[{"line":1,"column":1}]}]}`, row.missingField, row.onType)
		missingSuccess, missingSupported := "0", "1"
		if row.gated {
			missingSuccess, missingSupported = "1", "0"
		}

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
			{mode: "partial missing field", partialScrapes: true, response: missingField, wantStatus: http.StatusOK, wantSuccess: missingSuccess, wantSupported: missingSupported},
		} {
			t.Run(spec.name+"/"+test.mode, func(t *testing.T) {
				stub := newGraphQLStub(t, fixture(t, "saas"))
				stub.failOperation(row.operation, test.response)

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

				want := []string{"spacelift_error", spec.name + ": "}
				if test.partialScrapes {
					want = []string{
						`spacelift_scrape_collector_success{collector="` + spec.name + `"} ` + test.wantSuccess,
						`spacelift_scrape_collector_supported{collector="` + spec.name + `"} ` + test.wantSupported,
						"spacelift_worker_pool_runs_pending{",
					}
					for _, other := range collectorSpecs {
						if other.name != spec.name {
							want = append(want, `spacelift_scrape_collector_success{collector="`+other.name+`"} 1`)
						}
					}
				}
				for _, line := range want {
					if !strings.Contains(body, line) {
						t.Errorf("missing %q in:\n%s", line, body)
					}
				}

				for _, family := range describedFamilies(t, spec.build()) {
					if strings.Contains(body, "# HELP "+family+" ") {
						t.Errorf("%s emitted %s although it did not succeed:\n%s", spec.name, family, body)
					}
				}
			})
		}
	}
}

// optInFamilies returns the names of the metric families the opt-in
// collectors describe.
func optInFamilies(t *testing.T) []string {
	t.Helper()

	var families []string
	for _, spec := range collectorSpecs {
		if !spec.defaultEnabled {
			families = append(families, describedFamilies(t, spec.build())...)
		}
	}

	return families
}

// describedFamilies returns the names of the metric families c describes.
func describedFamilies(t *testing.T, c collector.Collector) []string {
	t.Helper()

	described := make(chan *prometheus.Desc, 64)
	c.Describe(described)
	close(described)

	var families []string
	for desc := range described {
		families = append(families, fqName(t, desc.String()))
	}

	return families
}

// splitFamilies splits exposition text into the families named in names and
// the rest. A family runs from its HELP line to the next family's.
func splitFamilies(exposition string, names []string) (named, rest string) {
	var in, out strings.Builder

	target := &out
	for line := range strings.Lines(exposition) {
		if help, ok := strings.CutPrefix(line, "# HELP "); ok {
			name, _, _ := strings.Cut(help, " ")
			target = &out
			if slices.Contains(names, name) {
				target = &in
			}
		}
		target.WriteString(line)
	}

	return in.String(), out.String()
}
