package collector

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

func newTestCollector(
	name string,
	collect func(context.Context) ([]prometheus.Metric, error),
) *testCollector {
	return &testCollector{
		name:    name,
		desc:    prometheus.NewDesc("test_"+name, "Test metric", nil, nil),
		collect: collect,
	}
}

func (c *testCollector) Name() string { return c.name }

func (c *testCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *testCollector) Collect(
	ctx context.Context,
	_ client.NamedClient,
) ([]prometheus.Metric, error) {
	return c.collect(ctx)
}

func newTestExporter(timeout time.Duration, collectors []Collector, partialScrapes bool) *Exporter {
	return New(
		context.Background(),
		zap.NewNop().Sugar(),
		nil,
		timeout,
		BuildInfo{},
		collectors,
		partialScrapes,
	)
}

func gatherExporter(exporter *Exporter) error {
	registry := prometheus.NewPedanticRegistry()
	if err := registry.Register(exporter); err != nil {
		return err
	}

	_, err := registry.Gather()
	return err
}

func TestCollectorsStartConcurrently(t *testing.T) {
	const collectorCount = 4

	started := make(chan string, collectorCount)
	release := make(chan struct{})
	collectors := make([]Collector, 0, collectorCount)
	for i := range collectorCount {
		name := string(rune('a' + i))
		collector := newTestCollector(name, func(context.Context) ([]prometheus.Metric, error) {
			started <- name
			<-release

			return []prometheus.Metric{
				prometheus.MustNewConstMetric(
					prometheus.NewDesc("test_"+name, "Test metric", nil, nil),
					prometheus.GaugeValue,
					1,
				),
			}, nil
		})
		collectors = append(collectors, collector)
	}

	done := make(chan error, 1)
	go func() {
		done <- gatherExporter(newTestExporter(time.Second, collectors, false))
	}()

	for range collectorCount {
		select {
		case <-started:
		case <-time.After(500 * time.Millisecond):
			close(release)
			<-done
			t.Fatal("not every collector started before the first collector was released")
		}
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("gathering metrics: %v", err)
	}
}

func TestScrapeDeadlineBoundsWholeScrape(t *testing.T) {
	const timeout = 200 * time.Millisecond

	collectors := make([]Collector, 0, 4)
	for i := range 4 {
		name := string(rune('a' + i))
		collectors = append(collectors, newTestCollector(
			name,
			func(ctx context.Context) ([]prometheus.Metric, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		))
	}

	start := time.Now()
	err := gatherExporter(newTestExporter(timeout, collectors, true))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a total collector failure should fail the scrape even in partial mode")
	}
	if elapsed >= 3*timeout {
		t.Fatalf("scrape took %s, want less than %s", elapsed, 3*timeout)
	}
}

func TestUnsupportedCollectorsAreHealthyInPartialMode(t *testing.T) {
	unsupported := newTestCollector("unsupported", func(context.Context) ([]prometheus.Metric, error) {
		return nil, ErrNotSupported
	})

	if err := gatherExporter(newTestExporter(time.Second, []Collector{unsupported}, false)); err == nil {
		t.Fatal("strict mode should preserve the legacy error for an unsupported collector")
	}
	if err := gatherExporter(newTestExporter(time.Second, []Collector{unsupported}, true)); err != nil {
		t.Fatalf("partial mode rejected an unsupported but healthy collector: %v", err)
	}
}

func TestUnsupportedCollectorLoggingMatchesScrapeMode(t *testing.T) {
	for _, test := range []struct {
		name           string
		partialScrapes bool
	}{
		{name: "strict"},
		{name: "partial", partialScrapes: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			core, logs := observer.New(zap.DebugLevel)
			unsupported := newTestCollector("unsupported", func(context.Context) ([]prometheus.Metric, error) {
				return nil, ErrNotSupported
			})
			exporter := New(
				context.Background(),
				zap.New(core).Sugar(),
				nil,
				time.Second,
				BuildInfo{},
				[]Collector{unsupported},
				test.partialScrapes,
			)

			_ = gatherExporter(exporter)

			entries := logs.FilterMessage("Collector is not supported on this deployment").All()
			if len(entries) != 1 {
				t.Fatalf("unsupported collector log entries = %d, want 1", len(entries))
			}
			wantLevel := zap.ErrorLevel
			if test.partialScrapes {
				wantLevel = zap.DebugLevel
			}
			if got, want := entries[0].Level, wantLevel; got != want {
				t.Fatalf("unsupported collector log level = %s, want %s", got, want)
			}
		})
	}
}

func TestCollectorPanicFailsScrapeInsteadOfCrashingProcess(t *testing.T) {
	panicking := newTestCollector("panicking", func(context.Context) ([]prometheus.Metric, error) {
		panic("boom")
	})

	err := gatherExporter(newTestExporter(time.Second, []Collector{panicking}, false))
	if err == nil || !strings.Contains(err.Error(), "panicking collector panicked: boom") {
		t.Fatalf("Gather() error = %v, want recovered collector panic", err)
	}
}

// TestStrictModeGatherErrorNamesTheFailure restores a guarantee the exporter
// has always had: when a strict-mode scrape fails, the error promhttp writes
// into the HTTP 500 body carries the spacelift_error family name and the name
// of the collector that broke. That text is the only failure diagnostics an
// operator gets in strict mode, since the gather's series are discarded, so it
// is a contract worth pinning.
func TestStrictModeGatherErrorNamesTheFailure(t *testing.T) {
	healthy := newTestCollector("healthy", func(context.Context) ([]prometheus.Metric, error) {
		return nil, nil
	})
	failed := newTestCollector("aggregates", func(context.Context) ([]prometheus.Metric, error) {
		return nil, errors.New("aggregates: internal error")
	})

	err := gatherExporter(newTestExporter(time.Second, []Collector{healthy, failed}, false))
	if err == nil {
		t.Fatal("strict mode Gather() succeeded with a failing collector, want the legacy failure contract")
	}

	for _, want := range []string{"spacelift_error", "aggregates"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("strict-mode Gather() error does not name %q, so the 500 body cannot identify the failure: %v", want, err)
		}
	}
}
