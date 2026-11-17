package collector

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hasura/go-graphql-client"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/spacelift-io/prometheus-exporter/client"
)

type testCollector struct {
	name    string
	desc    *prometheus.Desc
	collect func(context.Context) ([]prometheus.Metric, error)
}

func newTestCollector(name string, collect func(context.Context) ([]prometheus.Metric, error)) *testCollector {
	return &testCollector{
		name:    name,
		desc:    prometheus.NewDesc("test_"+name, "Test metric", nil, nil),
		collect: collect,
	}
}

func (c *testCollector) Name() string { return c.name }

func (c *testCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *testCollector) Collect(ctx context.Context, _ client.Client) ([]prometheus.Metric, error) {
	return c.collect(ctx)
}

func gather(timeout time.Duration, collectors ...Collector) error {
	exporter := New(context.Background(), zap.NewNop().Sugar(), nil, timeout, BuildInfo{}, collectors, false)

	registry := prometheus.NewPedanticRegistry()
	if err := registry.Register(exporter); err != nil {
		return err
	}
	_, err := registry.Gather()

	return err
}

// TestCollectorsRunConcurrently holds every collector until all of them have
// started, which can only complete if they run at the same time.
func TestCollectorsRunConcurrently(t *testing.T) {
	const count = 4

	started := make(chan struct{}, count)
	release := make(chan struct{})
	collectors := make([]Collector, 0, count)
	for i := range count {
		collectors = append(collectors, newTestCollector(string(rune('a'+i)), func(context.Context) ([]prometheus.Metric, error) {
			started <- struct{}{}
			<-release

			return nil, nil
		}))
	}

	done := make(chan error, 1)
	go func() { done <- gather(time.Second, collectors...) }()

	for range count {
		select {
		case <-started:
		case <-time.After(500 * time.Millisecond):
			close(release)
			<-done
			t.Fatal("not every collector had started before the first one was allowed to finish")
		}
	}
	close(release)

	if err := <-done; err != nil {
		t.Fatalf("gathering metrics: %v", err)
	}
}

// TestScrapeDeadlineBoundsTheWholeScrape checks that one deadline covers all
// collectors together rather than each in turn.
func TestScrapeDeadlineBoundsTheWholeScrape(t *testing.T) {
	const timeout = 200 * time.Millisecond

	collectors := make([]Collector, 0, 4)
	for i := range 4 {
		collectors = append(collectors, newTestCollector(string(rune('a'+i)), func(ctx context.Context) ([]prometheus.Metric, error) {
			<-ctx.Done()

			return nil, ctx.Err()
		}))
	}

	start := time.Now()
	err := gather(timeout, collectors...)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("every collector timed out, want a failed scrape")
	}
	if elapsed >= 2*timeout {
		t.Fatalf("scrape took %s with a %s deadline; collectors did not run concurrently", elapsed, timeout)
	}
}

// TestCollectorPanicFailsTheScrapeNotTheProcess turns a panicking collector
// into a failed scrape that names it.
func TestCollectorPanicFailsTheScrapeNotTheProcess(t *testing.T) {
	panicking := newTestCollector("panicking", func(context.Context) ([]prometheus.Metric, error) {
		panic("boom")
	})

	err := gather(time.Second, panicking)
	if err == nil || !strings.Contains(err.Error(), "panicking: collector panicked: boom") {
		t.Fatalf("Gather() error = %v, want the recovered panic naming the collector", err)
	}
}

// versionGatedTestCollector is a testCollector that also declares fields as
// version-gated.
type versionGatedTestCollector struct {
	*testCollector
	fields []string
}

func (c versionGatedTestCollector) VersionGatedFields() []string { return c.fields }

// TestVersionGatedMissingFieldReadsAsUnsupported runs the exporter over a
// collector whose declared field the API reports missing, a collector that
// gets the same error without declaring the field, and a healthy collector.
func TestVersionGatedMissingFieldReadsAsUnsupported(t *testing.T) {
	const message = `Cannot query field "x" on type "T".`

	missing := func(context.Context) ([]prometheus.Metric, error) {
		return nil, graphql.Errors{{Message: message}}
	}
	gated := versionGatedTestCollector{testCollector: newTestCollector("gated", missing), fields: []string{"x"}}
	plain := newTestCollector("plain", missing)
	healthy := newTestCollector("healthy", nil)
	healthy.collect = func(context.Context) ([]prometheus.Metric, error) {
		return []prometheus.Metric{prometheus.MustNewConstMetric(healthy.desc, prometheus.GaugeValue, 1)}, nil
	}

	t.Run("partial", func(t *testing.T) {
		core, logs := observer.New(zap.DebugLevel)
		exporter := New(context.Background(), zap.New(core).Sugar(), nil, time.Second, BuildInfo{},
			[]Collector{gated, plain, healthy}, true)

		registry := prometheus.NewPedanticRegistry()
		if err := registry.Register(exporter); err != nil {
			t.Fatalf("registering exporter: %v", err)
		}
		families, err := registry.Gather()
		if err != nil {
			t.Fatalf("Gather() error = %v, want the healthy collector served", err)
		}

		// gauge returns the named gauge, picking the series by its collector
		// label when collector is not empty.
		gauge := func(name, collector string) (float64, bool) {
			for _, family := range families {
				if family.GetName() != name {
					continue
				}
				for _, metric := range family.GetMetric() {
					if collector == "" {
						return metric.GetGauge().GetValue(), true
					}
					for _, label := range metric.GetLabel() {
						if label.GetName() == "collector" && label.GetValue() == collector {
							return metric.GetGauge().GetValue(), true
						}
					}
				}
			}

			return 0, false
		}

		for _, want := range []struct {
			metric, collector string
			value             float64
		}{
			{"spacelift_scrape_collector_success", "gated", 1},
			{"spacelift_scrape_collector_supported", "gated", 0},
			{"spacelift_scrape_collector_success", "plain", 0},
			{"spacelift_scrape_collector_supported", "plain", 1},
			{"test_healthy", "", 1},
		} {
			got, ok := gauge(want.metric, want.collector)
			if !ok {
				t.Errorf("%s{collector=%q} not served", want.metric, want.collector)
				continue
			}
			if got != want.value {
				t.Errorf("%s{collector=%q} = %v, want %v", want.metric, want.collector, got, want.value)
			}
		}

		warnings := logs.FilterLevelExact(zap.WarnLevel).All()
		if len(warnings) != 1 {
			t.Fatalf("got %d warning log entries, want 1: %v", len(warnings), warnings)
		}
		fields := warnings[0].ContextMap()
		if fields["collector"] != "gated" || !strings.Contains(fmt.Sprint(fields["error"]), message) {
			t.Errorf("warning fields = %v, want the gated collector and the API message", fields)
		}
	})

	t.Run("strict", func(t *testing.T) {
		err := gather(time.Second, gated, healthy)
		if err == nil || !strings.Contains(err.Error(), message) {
			t.Fatalf("Gather() error = %v, want a failure carrying the API message", err)
		}
	})
}
