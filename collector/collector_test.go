package collector

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

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
