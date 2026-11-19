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
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
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

// fakeClock is a clock a test moves by hand. It is safe to read from the
// cache's request goroutine.
type fakeClock struct {
	mutex sync.Mutex
	at    time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{at: time.Unix(0, 0)} }

func (f *fakeClock) Now() time.Time {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	return f.at
}

func (f *fakeClock) Add(d time.Duration) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	f.at = f.at.Add(d)
}

// cached wraps c in a cache that reads the time from clock.
func cached(c Collector, ttl time.Duration, clock *fakeClock) *cachingCollector {
	cache := WithCacheTTL(c, ttl).(*cachingCollector)
	cache.now = clock.Now

	return cache
}

// waitForJoined waits until n scrapes wait on the request in flight.
func waitForJoined(t *testing.T, cache *cachingCollector, n int) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for {
		cache.mutex.Lock()
		joined := 0
		if cache.call != nil {
			joined = cache.call.joined
		}
		cache.mutex.Unlock()

		if joined == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d scrapes joined the request in flight, want %d", joined, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// cacheAge returns the cache age metric's value among metrics.
func cacheAge(t *testing.T, metrics []prometheus.Metric) float64 {
	t.Helper()

	for _, metric := range metrics {
		if metric.Desc() == cacheAgeDesc {
			return testutil.ToFloat64(oneMetric{metric})
		}
	}
	t.Fatalf("no cache age metric among %d metrics", len(metrics))

	return 0
}

// oneMetric is a prometheus.Collector of a single metric.
type oneMetric struct{ prometheus.Metric }

func (m oneMetric) Describe(ch chan<- *prometheus.Desc) { ch <- m.Desc() }
func (m oneMetric) Collect(ch chan<- prometheus.Metric) { ch <- m.Metric }

// pastDeadlineContext reports a deadline that has already passed, while its
// own Done never closes. It stands for a scrape whose deadline passes while
// its request is in flight: the cache starts the request, and the request's
// context, which takes the deadline, has ended.
type pastDeadlineContext struct{ context.Context }

func (pastDeadlineContext) Deadline() (time.Time, bool) { return time.Unix(0, 0), true }

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
	clock := newFakeClock()
	inner, requests := countingCollector(nil)
	cache := cached(inner, time.Minute, clock)

	for range 4 {
		if metrics, err := cache.Collect(context.Background(), nil); err != nil || len(metrics) != 2 {
			t.Fatalf("Collect = %d metrics, %v; want the cached metric and its age", len(metrics), err)
		}
		clock.Add(10 * time.Second)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("4 scrapes within the TTL issued %d requests, want 1", got)
	}

	clock.Add(20 * time.Second) // 60s since the request: expired.
	if _, err := cache.Collect(context.Background(), nil); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("a scrape after the TTL issued %d requests in all, want 2", got)
	}
}

// TestCacheServesCopies keeps one scrape from writing into the slice that the
// next scrape is served.
func TestCacheServesCopies(t *testing.T) {
	clock := newFakeClock()
	inner, _ := countingCollector(nil)
	cache := cached(inner, time.Minute, clock)

	first, _ := cache.Collect(context.Background(), nil)
	second, _ := cache.Collect(context.Background(), nil)
	if &first[0] == &second[0] {
		t.Fatal("two scrapes were served the same slice")
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
			clock := newFakeClock()
			inner, requests := countingCollector(tc.err)
			cache := cached(inner, tc.ttl, clock)

			for _, step := range []time.Duration{0, tc.lifetime - time.Nanosecond} {
				clock.Add(step)
				if _, err := cache.Collect(context.Background(), nil); !errors.Is(err, tc.err) {
					t.Fatalf("Collect error = %v, want %v", err, tc.err)
				}
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("scrapes within %s issued %d requests, want 1", tc.lifetime, got)
			}

			clock.Add(time.Nanosecond)
			_, _ = cache.Collect(context.Background(), nil)
			if got := requests.Load(); got != 2 {
				t.Fatalf("a scrape %s after the failure issued %d requests in all, want 2", tc.lifetime, got)
			}
		})
	}
}

// TestCacheServesStaleOnFailure serves the last good metrics while refreshes
// fail, retrying every errorTTL, until they are maxStale past their TTL.
func TestCacheServesStaleOnFailure(t *testing.T) {
	const ttl = time.Minute
	clock := newFakeClock()
	var requests atomic.Int32
	var failing atomic.Bool
	var inner *testCollector
	inner = newTestCollector("flaky", func(context.Context) ([]prometheus.Metric, error) {
		requests.Add(1)
		if failing.Load() {
			return nil, errors.New("502 Bad Gateway")
		}

		return []prometheus.Metric{prometheus.MustNewConstMetric(inner.desc, prometheus.GaugeValue, 1)}, nil
	})
	cache := cached(inner, ttl, clock)

	if _, err := cache.Collect(context.Background(), nil); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	failing.Store(true)

	// Each step lands on a scrape that either refreshes (and fails) or is
	// served the kept failure, and every one is covered by the stale data.
	for elapsed := ttl; elapsed <= ttl+maxStale; elapsed += errorTTL / 2 {
		clock.Add(elapsed - clock.Now().Sub(time.Unix(0, 0)))
		metrics, err := cache.Collect(context.Background(), nil)
		if err != nil || len(metrics) != 2 {
			t.Fatalf("at %s: Collect = %d metrics, %v; want the stale metrics", elapsed, len(metrics), err)
		}
		if got := cacheAge(t, metrics); got != elapsed.Seconds() {
			t.Fatalf("at %s: cache age = %vs, want %vs", elapsed, got, elapsed.Seconds())
		}
	}
	// One success, then a refresh every errorTTL over ttl..ttl+maxStale.
	if got, want := requests.Load(), int32(1+maxStale/errorTTL+1); got != want {
		t.Fatalf("issued %d requests, want %d", got, want)
	}

	clock.Add(errorTTL) // past maxStale, and the kept failure has expired
	if _, err := cache.Collect(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("Collect past maxStale: error = %v, want the failure", err)
	}
}

// TestCacheDoesNotServeStaleWhenUnsupported returns an unsupported result
// rather than covering it with old data: it is an answer, not an outage.
func TestCacheDoesNotServeStaleWhenUnsupported(t *testing.T) {
	clock := newFakeClock()
	unsupported := fmt.Errorf("usage: %s", machineSessionError)
	var gone atomic.Bool
	var inner *testCollector
	inner = newTestCollector("revoked", func(context.Context) ([]prometheus.Metric, error) {
		if gone.Load() {
			return nil, unsupported
		}

		return []prometheus.Metric{prometheus.MustNewConstMetric(inner.desc, prometheus.GaugeValue, 1)}, nil
	})
	cache := cached(inner, time.Minute, clock)

	_, _ = cache.Collect(context.Background(), nil)
	gone.Store(true)
	clock.Add(time.Minute)
	if _, err := cache.Collect(context.Background(), nil); !errors.Is(err, unsupported) {
		t.Fatalf("Collect error = %v, want %v", err, unsupported)
	}
}

// TestCacheAgeMetric reports how old the served data is.
func TestCacheAgeMetric(t *testing.T) {
	clock := newFakeClock()
	inner, _ := countingCollector(nil)
	cache := cached(inner, time.Minute, clock)

	for _, want := range []float64{0, 25, 50} {
		metrics, err := cache.Collect(context.Background(), nil)
		if err != nil {
			t.Fatalf("Collect: %v", err)
		}
		if got := cacheAge(t, metrics); got != want {
			t.Fatalf("cache age = %vs, want %vs", got, want)
		}
		clock.Add(25 * time.Second)
	}
}

// TestCachedCollectorThroughTheExporter checks the cache age metric against
// the exporter: it is described, unique, and lint-clean.
func TestCachedCollectorThroughTheExporter(t *testing.T) {
	inner, _ := countingCollector(nil)
	cache := WithCacheTTL(inner, time.Minute)

	if err := gather(time.Second, cache); err != nil {
		t.Fatalf("gather: %v", err)
	}

	exporter := New(context.Background(), zap.NewNop().Sugar(), nil, time.Second, BuildInfo{}, []Collector{cache}, false)
	problems, err := testutil.CollectAndLint(exporter, "spacelift_scrape_collector_cache_age_seconds")
	if err != nil {
		t.Fatalf("CollectAndLint: %v", err)
	}
	for _, problem := range problems {
		t.Errorf("lint: %s: %s", problem.Metric, problem.Text)
	}
}

// TestCacheKeepsVersionGatedFields keeps a declared missing field reading as
// unsupported once the collector is cached: the exporter classifies the
// wrapper, and the cache keeps the answer for the full TTL. An undeclared
// missing field stays a failure and is retried after errorTTL, whether the
// collector declares other fields or none at all.
func TestCacheKeepsVersionGatedFields(t *testing.T) {
	missing := apiErrors(`Cannot query field "searchSchedulableRuns" on type "WorkerPool".`)

	for _, tc := range []struct {
		name        string
		wrap        func(*testCollector) Collector
		unsupported bool
		lifetime    time.Duration
	}{
		{
			"declared field",
			func(c *testCollector) Collector { return gatedTestCollector{c, []string{"searchSchedulableRuns"}} },
			true, time.Hour,
		},
		{
			"undeclared field",
			func(c *testCollector) Collector { return gatedTestCollector{c, nil} },
			false, errorTTL,
		},
		{
			"collector without VersionGated",
			func(c *testCollector) Collector { return c },
			false, errorTTL,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := newFakeClock()
			inner, requests := countingCollector(missing)
			cache := cached(tc.wrap(inner), time.Hour, clock)

			_, err := cache.Collect(context.Background(), nil)
			if got := errors.Is(classify(cache, err), ErrNotSupported); got != tc.unsupported {
				t.Fatalf("classify(cache, %v) unsupported = %t, want %t", err, got, tc.unsupported)
			}

			clock.Add(tc.lifetime - time.Nanosecond)
			_, _ = cache.Collect(context.Background(), nil)
			if got := requests.Load(); got != 1 {
				t.Fatalf("scrapes within %s issued %d requests, want 1", tc.lifetime, got)
			}

			clock.Add(time.Nanosecond)
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

// TestCacheDoesNotKeepFailuresFromEndedContexts keeps one scrape's passed
// deadline from failing the scrapes after it.
func TestCacheDoesNotKeepFailuresFromEndedContexts(t *testing.T) {
	clock := newFakeClock()
	var requests atomic.Int32
	inner := newTestCollector("deadline", func(ctx context.Context) ([]prometheus.Metric, error) {
		requests.Add(1)

		return nil, ctx.Err()
	})
	cache := cached(inner, time.Minute, clock)

	if _, err := cache.Collect(pastDeadlineContext{context.Background()}, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Collect past its deadline: error = %v, want %v", err, context.DeadlineExceeded)
	}
	for range 2 {
		if _, err := cache.Collect(context.Background(), nil); err != nil {
			t.Fatalf("Collect after a passed deadline: %v", err)
		}
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("issued %d requests, want 2: one past its deadline, then one cached success", got)
	}
}

// TestCacheSkipsEndedScrapes sends no request for a scrape whose context has
// already ended.
func TestCacheSkipsEndedScrapes(t *testing.T) {
	clock := newFakeClock()
	inner, requests := countingCollector(nil)
	cache := cached(inner, time.Minute, clock)

	ended, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cache.Collect(ended, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Collect with an ended context: error = %v, want %v", err, context.Canceled)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("issued %d requests for an ended scrape, want 0", got)
	}
}

// TestCacheRecoversPanics caches a panicking collector as a failure, so it is
// not retried on every scrape.
func TestCacheRecoversPanics(t *testing.T) {
	clock := newFakeClock()
	var requests atomic.Int32
	inner := newTestCollector("panicky", func(context.Context) ([]prometheus.Metric, error) {
		requests.Add(1)
		panic("boom")
	})
	cache := cached(inner, time.Hour, clock)

	for range 2 {
		if _, err := cache.Collect(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "panicked: boom") {
			t.Fatalf("Collect error = %v, want the recovered panic", err)
		}
	}
	clock.Add(errorTTL)
	_, _ = cache.Collect(context.Background(), nil)
	if got := requests.Load(); got != 2 {
		t.Fatalf("issued %d requests, want 2: one cached panic, then a retry after %s", got, errorTTL)
	}
}

// TestCacheIsSingleFlight covers several scrapers arriving together, such as an
// HA pair of Prometheus servers: they share one request.
func TestCacheIsSingleFlight(t *testing.T) {
	clock := newFakeClock()
	var requests atomic.Int32
	release := make(chan struct{})
	inner := newTestCollector("slow", func(context.Context) ([]prometheus.Metric, error) {
		requests.Add(1)
		<-release

		return nil, nil
	})
	cache := cached(inner, time.Minute, clock)

	const scrapers = 8
	var waitGroup sync.WaitGroup
	for range scrapers {
		waitGroup.Go(func() { _, _ = cache.Collect(context.Background(), nil) })
	}
	waitForJoined(t, cache, scrapers)
	close(release)
	waitGroup.Wait()

	if got := requests.Load(); got != 1 {
		t.Fatalf("%d concurrent scrapes issued %d requests, want 1", scrapers, got)
	}
}

// TestCacheSlowRequestIsSharedAndKept covers a request that outlasts its TTL:
// every scrape waiting on it is served its outcome, and the outcome is timed
// from when the request returned, so it is kept rather than already expired.
func TestCacheSlowRequestIsSharedAndKept(t *testing.T) {
	const ttl = 10 * time.Second
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"success", nil},
		{"failure", errors.New("502 Bad Gateway")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := newFakeClock()
			var requests atomic.Int32
			release := make(chan struct{})
			var inner *testCollector
			inner = newTestCollector("slow", func(context.Context) ([]prometheus.Metric, error) {
				requests.Add(1)
				clock.Add(3 * ttl)
				<-release
				if tc.err != nil {
					return nil, tc.err
				}

				return []prometheus.Metric{prometheus.MustNewConstMetric(inner.desc, prometheus.GaugeValue, 1)}, nil
			})
			cache := cached(inner, ttl, clock)

			const scrapers = 4
			errs := make([]error, scrapers)
			var waitGroup sync.WaitGroup
			for index := range scrapers {
				waitGroup.Go(func() { _, errs[index] = cache.Collect(context.Background(), nil) })
			}
			waitForJoined(t, cache, scrapers)
			close(release)
			waitGroup.Wait()

			for index, err := range errs {
				if !errors.Is(err, tc.err) {
					t.Errorf("scraper %d: error = %v, want %v", index, err, tc.err)
				}
			}
			if _, err := cache.Collect(context.Background(), nil); !errors.Is(err, tc.err) {
				t.Errorf("Collect after the slow request: error = %v, want %v", err, tc.err)
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("issued %d requests, want 1", got)
			}
		})
	}
}

// TestCacheWaiterGivesUp lets a scrape whose context ends while a request is
// in flight return at once, even the one that started the request, without
// ending the request for the scrapes still waiting.
func TestCacheWaiterGivesUp(t *testing.T) {
	clock := newFakeClock()
	var requests atomic.Int32
	release := make(chan struct{})
	var inner *testCollector
	inner = newTestCollector("slow", func(ctx context.Context) ([]prometheus.Metric, error) {
		requests.Add(1)
		<-release
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		return []prometheus.Metric{prometheus.MustNewConstMetric(inner.desc, prometheus.GaugeValue, 1)}, nil
	})
	cache := cached(inner, time.Minute, clock)

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	defer cancelLeader()
	defer cancelWaiter()

	leader := make(chan error, 1)
	go func() {
		_, err := cache.Collect(leaderCtx, nil)
		leader <- err
	}()
	waitForJoined(t, cache, 1)

	waiter := make(chan error, 1)
	go func() {
		_, err := cache.Collect(waiterCtx, nil)
		waiter <- err
	}()
	patient := make(chan error, 1)
	go func() {
		_, err := cache.Collect(context.Background(), nil)
		patient <- err
	}()
	waitForJoined(t, cache, 3)

	cancelLeader()
	cancelWaiter()
	for name, done := range map[string]chan error{"leader": leader, "waiter": waiter} {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("%s: error = %v, want %v", name, err, context.Canceled)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s did not return when its context was cancelled", name)
		}
	}

	close(release)
	if err := <-patient; err != nil {
		t.Fatalf("scrape still waiting: %v", err)
	}
	if _, err := cache.Collect(context.Background(), nil); err != nil {
		t.Fatalf("Collect after the request: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("issued %d requests, want 1, kept for the scrapes after it", got)
	}
}

// TestCacheDroppedFailureIsShared covers a request whose deadline passes: it
// is not kept, but every scrape waiting on it is served the same failure
// rather than each issuing a request in turn. The next scrape issues a new
// one.
func TestCacheDroppedFailureIsShared(t *testing.T) {
	clock := newFakeClock()
	var requests atomic.Int32
	release := make(chan struct{})
	var inner *testCollector
	inner = newTestCollector("slow", func(ctx context.Context) ([]prometheus.Metric, error) {
		if requests.Add(1) == 1 {
			<-release

			return nil, ctx.Err()
		}

		return []prometheus.Metric{prometheus.MustNewConstMetric(inner.desc, prometheus.GaugeValue, 1)}, nil
	})
	cache := cached(inner, time.Minute, clock)

	const scrapers = 4
	errs := make([]error, scrapers)
	var waitGroup sync.WaitGroup
	waitGroup.Go(func() { _, errs[0] = cache.Collect(pastDeadlineContext{context.Background()}, nil) })
	waitForJoined(t, cache, 1)
	for index := 1; index < scrapers; index++ {
		waitGroup.Go(func() { _, errs[index] = cache.Collect(context.Background(), nil) })
	}
	waitForJoined(t, cache, scrapers)
	close(release)
	waitGroup.Wait()

	for index, err := range errs {
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("scraper %d: error = %v, want %v", index, err, context.DeadlineExceeded)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("%d scrapes on one request issued %d requests, want 1", scrapers, got)
	}

	if _, err := cache.Collect(context.Background(), nil); err != nil {
		t.Fatalf("Collect after the dropped failure: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("issued %d requests in all, want 2: the dropped failure is not kept", got)
	}
}
