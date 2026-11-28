package main

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/spacelift-io/prometheus-exporter/collector"
)

// TestCacheTTLFlags runs the cache TTL flags through urfave/cli, by flag and by
// environment variable, alongside the collector toggles they sit next to.
func TestCacheTTLFlags(t *testing.T) {
	zero := map[string]time.Duration{}
	for _, spec := range collectorSpecs {
		zero[spec.name] = 0
	}
	with := func(name string, ttl time.Duration) map[string]time.Duration {
		out := maps.Clone(zero)
		out[name] = ttl

		return out
	}

	for _, test := range []struct {
		name        string
		args        []string
		env         map[string]string
		want        map[string]time.Duration
		wantIgnored []string
		wantErr     string
	}{
		{name: "defaults to off", want: zero},
		{
			name: "flag",
			args: []string{"--collector.aggregates.cache-ttl=5m"},
			want: with("aggregates", 5*time.Minute),
		},
		{
			name: "environment variable",
			env:  map[string]string{"SPACELIFT_PROMEX_COLLECTOR_USAGE_CACHE_TTL": "1h"},
			want: with("usage", time.Hour),
		},
		{
			name: "next to the inverse toggle",
			args: []string{"--no-collector.usage", "--collector.aggregates.cache-ttl=30s"},
			want: with("aggregates", 30*time.Second),
		},
		{
			name:        "on a disabled collector",
			args:        []string{"--no-collector.aggregates", "--collector.aggregates.cache-ttl=5m"},
			want:        with("aggregates", 5*time.Minute),
			wantIgnored: []string{"aggregates"},
		},
		{
			name:    "negative is rejected",
			args:    []string{"--collector.aggregates.cache-ttl=-1s"},
			wantErr: "--collector.aggregates.cache-ttl must not be negative",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			for key, value := range test.env {
				t.Setenv(key, value)
			}

			var got map[string]time.Duration
			var gotIgnored []string
			var gotErr error
			command := &cli.Command{
				Name:  "test",
				Flags: collectorCLIFlags(),
				Action: func(_ context.Context, cmd *cli.Command) error {
					got, gotErr = collectorCacheTTLs(cmd)
					gotIgnored = ignoredCacheTTLs(collectorSelection(cmd), got)
					return nil
				},
			}
			if err := command.Run(context.Background(), append([]string{"test"}, test.args...)); err != nil {
				t.Fatalf("running command: %v", err)
			}

			if test.wantErr != "" {
				if gotErr == nil || !strings.Contains(gotErr.Error(), test.wantErr) {
					t.Fatalf("error = %v, want %q", gotErr, test.wantErr)
				}
				return
			}
			if gotErr != nil {
				t.Fatalf("collectorCacheTTLs: %v", gotErr)
			}
			if !maps.Equal(got, test.want) {
				t.Errorf("cache TTLs = %v, want %v", got, test.want)
			}
			if !slices.Equal(gotIgnored, test.wantIgnored) {
				t.Errorf("ignored cache TTLs = %v, want %v", gotIgnored, test.wantIgnored)
			}
		})
	}
}

// TestCacheTTLBoundsAPIRequests scrapes an exporter whose aggregates collector
// has a cache TTL three times: the `metrics` document is requested once and
// served from cache after that, while uncached collectors query every scrape.
func TestCacheTTLBoundsAPIRequests(t *testing.T) {
	stub := newGraphQLStub(t, fixture(t, "saas"))
	exporter := stub.cachedCollector(t, map[string]time.Duration{"aggregates": time.Hour})

	var outputs []string
	for range 3 {
		outputs = append(outputs, gather(t, exporter))
	}

	_, operationNames := stub.recorded()
	requests := map[string]int{}
	for _, name := range operationNames {
		requests[name]++
	}
	for operation, want := range map[string]int{
		"PrometheusExporter_Aggregates":       1,
		"PrometheusExporter_WorkerPools":      3,
		"PrometheusExporter_Usage":            3,
		"PrometheusExporter_PublicWorkerPool": 3,
	} {
		if got := requests[operation]; got != want {
			t.Errorf("3 scrapes issued %d %s requests, want %d", got, operation, want)
		}
	}

	// Every scrape, cached or not, serves the aggregates and reports the
	// collector healthy, with a cache age only for the cached collector.
	for index, output := range outputs {
		for _, want := range []string{
			"spacelift_current_stacks_count_by_state{",
			`spacelift_scrape_collector_success{collector="aggregates"} 1`,
			`spacelift_scrape_collector_cache_age_seconds{collector="aggregates"} `,
		} {
			if !strings.Contains(output, want) {
				t.Errorf("scrape %d is missing %q:\n%s", index+1, want, output)
			}
		}
		if strings.Contains(output, `spacelift_scrape_collector_cache_age_seconds{collector="usage"}`) {
			t.Errorf("scrape %d reported a cache age for an uncached collector", index+1)
		}
	}
}

// TestZeroCacheTTLKeepsTheGoldenSurface pins that wiring the default TTL of 0
// through withCacheTTLs changes nothing: same output, a request per scrape.
func TestZeroCacheTTLKeepsTheGoldenSurface(t *testing.T) {
	zero := map[string]time.Duration{}
	for _, spec := range collectorSpecs {
		zero[spec.name] = 0
	}
	stub := newGraphQLStub(t, fixture(t, "saas"))
	exporter := stub.cachedCollector(t, zero)

	assertGolden(t, "saas", gather(t, exporter))
	gather(t, exporter)

	if queries, _ := stub.recorded(); len(queries) != 2*len(newCollectors(nil)) {
		t.Errorf("2 scrapes with a zero TTL issued %d requests, want %d", len(queries), 2*len(newCollectors(nil)))
	}
}

// gatedAggregates declares fields as version-gated on a real collector.
type gatedAggregates struct {
	collector.Collector
	fields []string
}

func (c *gatedAggregates) VersionGatedFields() []string { return c.fields }

// TestCacheTTLKeepsVersionGatedFields wraps a collector that declares
// version-gated fields the way the cache-ttl flag does, and checks the wrapper
// still declares them, so a missing declared field reads as unsupported rather
// than failed once the collector is cached.
func TestCacheTTLKeepsVersionGatedFields(t *testing.T) {
	fields := []string{"searchSchedulableRuns", "searchSchedulableRunsSuggestions"}
	inner := &gatedAggregates{collector.NewAggregates(), fields}

	wrapped := withCacheTTLs([]collector.Collector{inner}, map[string]time.Duration{inner.Name(): time.Hour})[0]
	if wrapped == collector.Collector(inner) {
		t.Fatal("a collector with a positive TTL was returned unwrapped")
	}
	gated, ok := wrapped.(collector.VersionGated)
	if !ok {
		t.Fatal("the cached collector no longer implements collector.VersionGated")
	}
	if got := gated.VersionGatedFields(); !slices.Equal(got, fields) {
		t.Errorf("cached VersionGatedFields() = %v, want %v", got, fields)
	}
}
