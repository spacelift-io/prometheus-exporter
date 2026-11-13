package collector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// countingCollector counts its requests and answers each one with err.
func countingCollector(err error) (*testCollector, *atomic.Int32) {
	var requests atomic.Int32
	var c *testCollector
	c = newTestCollector("counted", func(context.Context) ([]prometheus.Metric, error) {
		requests.Add(1)
		if err != nil {
			return nil, err
		}

		return []prometheus.Metric{prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, 1)}, nil
	})

	return c, &requests
}

// cached wraps c in a cache that reads the time from *at, so a test moves the
// clock by hand.
func cached(c Collector, ttl time.Duration, at *time.Time) *cachingCollector {
	cache := WithCacheTTL(c, ttl).(*cachingCollector)
	cache.now = func() time.Time { return *at }

	return cache
}

func TestWithCacheTTLZeroLeavesTheCollectorAlone(t *testing.T) {
	c, _ := countingCollector(nil)

	for _, ttl := range []time.Duration{0, -time.Second} {
		if got := WithCacheTTL(c, ttl); got != Collector(c) {
			t.Errorf("WithCacheTTL(c, %s) wrapped the collector; a non-positive TTL must leave it as it is", ttl)
		}
	}
}

// TestCacheQueriesAtMostOncePerTTL is the point of the cache: however often it
// is scraped, the API sees one request per TTL.
func TestCacheQueriesAtMostOncePerTTL(t *testing.T) {
	at := time.Unix(0, 0)
	inner, requests := countingCollector(nil)
	cache := cached(inner, time.Minute, &at)

	for range 4 {
		if metrics, err := cache.Collect(context.Background(), nil); err != nil || len(metrics) != 1 {
			t.Fatalf("Collect = %d metrics, %v; want the cached metric", len(metrics), err)
		}
		at = at.Add(10 * time.Second)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("4 scrapes within the TTL issued %d requests, want 1", got)
	}

	at = at.Add(20 * time.Second) // 60s since the request: expired.
	if _, err := cache.Collect(context.Background(), nil); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("a scrape after the TTL issued %d requests in all, want 2", got)
	}
}

// TestCacheErrorLifetime keeps a failure from outliving errorTTL, so one bad
// request cannot fail every scrape for a long TTL, while an unsupported result
// is an answer and is kept like a success.
func TestCacheErrorLifetime(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ttl      time.Duration
		err      error
		lifetime time.Duration
	}{
		{"failure under a long TTL", time.Hour, errors.New("502 Bad Gateway"), errorTTL},
		{"failure under a short TTL", 10 * time.Second, errors.New("502 Bad Gateway"), 10 * time.Second},
		{"unsupported", time.Hour, fmt.Errorf("usage: %s", machineSessionError), time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Unix(0, 0)
			inner, requests := countingCollector(tc.err)
			cache := cached(inner, tc.ttl, &at)

			for _, step := range []time.Duration{0, tc.lifetime - time.Nanosecond} {
				at = at.Add(step)
				if _, err := cache.Collect(context.Background(), nil); !errors.Is(err, tc.err) {
					t.Fatalf("Collect error = %v, want %v", err, tc.err)
				}
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("scrapes within %s issued %d requests, want 1", tc.lifetime, got)
			}

			at = at.Add(time.Nanosecond)
			_, _ = cache.Collect(context.Background(), nil)
			if got := requests.Load(); got != 2 {
				t.Fatalf("a scrape %s after the failure issued %d requests in all, want 2", tc.lifetime, got)
			}
		})
	}
}

// TestCacheKeepsVersionGatedFields keeps a declared missing field reading as
// unsupported once the collector is cached: the exporter classifies the
// wrapper, and the cache keeps the answer for the full TTL. An undeclared
// missing field stays a failure and is retried after errorTTL.
func TestCacheKeepsVersionGatedFields(t *testing.T) {
	missing := apiErrors(`Cannot query field "searchSchedulableRuns" on type "WorkerPool".`)

	for _, tc := range []struct {
		name        string
		fields      []string
		unsupported bool
		lifetime    time.Duration
	}{
		{"declared field", []string{"searchSchedulableRuns"}, true, time.Hour},
		{"undeclared field", nil, false, errorTTL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Unix(0, 0)
			inner, requests := countingCollector(missing)
			cache := cached(gatedTestCollector{inner, tc.fields}, time.Hour, &at)

			_, err := cache.Collect(context.Background(), nil)
			if got := errors.Is(classify(cache, err), ErrNotSupported); got != tc.unsupported {
				t.Fatalf("classify(cache, %v) unsupported = %t, want %t", err, got, tc.unsupported)
			}

			at = at.Add(tc.lifetime - time.Nanosecond)
			_, _ = cache.Collect(context.Background(), nil)
			if got := requests.Load(); got != 1 {
				t.Fatalf("scrapes within %s issued %d requests, want 1", tc.lifetime, got)
			}

			at = at.Add(time.Nanosecond)
			_, _ = cache.Collect(context.Background(), nil)
			if got := requests.Load(); got != 2 {
				t.Fatalf("a scrape %s after the first issued %d requests in all, want 2", tc.lifetime, got)
			}
		})
	}
}

// gatedTestCollector declares fields as version-gated for a test collector.
type gatedTestCollector struct {
	*testCollector
	fields []string
}

func (c gatedTestCollector) VersionGatedFields() []string { return c.fields }

// TestCacheIgnoresTheCallersDeadline keeps one scrape's expired deadline from
// failing the scrapes after it.
func TestCacheIgnoresTheCallersDeadline(t *testing.T) {
	at := time.Unix(0, 0)
	var requests atomic.Int32
	inner := newTestCollector("deadline", func(ctx context.Context) ([]prometheus.Metric, error) {
		requests.Add(1)

		return nil, ctx.Err()
	})
	cache := cached(inner, time.Minute, &at)

	expired, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	if _, err := cache.Collect(expired, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Collect with an expired deadline: error = %v, want %v", err, context.DeadlineExceeded)
	}
	for range 2 {
		if _, err := cache.Collect(context.Background(), nil); err != nil {
			t.Fatalf("Collect after an expired deadline: %v", err)
		}
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("issued %d requests, want 2: one for the expired scrape, then one cached success", got)
	}
}

// TestCacheRecoversPanics caches a panicking collector as a failure, so it is
// not retried on every scrape.
func TestCacheRecoversPanics(t *testing.T) {
	at := time.Unix(0, 0)
	var requests atomic.Int32
	inner := newTestCollector("panicky", func(context.Context) ([]prometheus.Metric, error) {
		requests.Add(1)
		panic("boom")
	})
	cache := cached(inner, time.Hour, &at)

	for range 2 {
		if _, err := cache.Collect(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "panicked: boom") {
			t.Fatalf("Collect error = %v, want the recovered panic", err)
		}
	}
	at = at.Add(errorTTL)
	_, _ = cache.Collect(context.Background(), nil)
	if got := requests.Load(); got != 2 {
		t.Fatalf("issued %d requests, want 2: one cached panic, then a retry after %s", got, errorTTL)
	}
}

// TestCacheIsSingleFlight covers several scrapers arriving together, such as an
// HA pair of Prometheus servers: they share one request.
func TestCacheIsSingleFlight(t *testing.T) {
	at := time.Unix(0, 0)
	var requests atomic.Int32
	release := make(chan struct{})
	inner := newTestCollector("slow", func(context.Context) ([]prometheus.Metric, error) {
		requests.Add(1)
		<-release

		return nil, nil
	})
	cache := cached(inner, time.Minute, &at)

	const scrapers = 8
	var arrived, waitGroup sync.WaitGroup
	arrived.Add(scrapers)
	for range scrapers {
		waitGroup.Go(func() {
			arrived.Done()
			_, _ = cache.Collect(context.Background(), nil)
		})
	}
	arrived.Wait()
	time.Sleep(50 * time.Millisecond) // let every scraper reach the cache
	close(release)
	waitGroup.Wait()

	if got := requests.Load(); got != 1 {
		t.Fatalf("%d concurrent scrapes issued %d requests, want 1", scrapers, got)
	}
}
