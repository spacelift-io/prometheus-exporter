package collector

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// errorTTL caps how long a failed request is served from cache. A transient
// failure, such as an HTTP 502, then fails scrapes for at most this long
// rather than for the whole TTL. It is fixed rather than an option.
const errorTTL = 30 * time.Second

// maxStale is how long past its TTL the last successful outcome may still be
// served while refreshes fail. Beyond it a failed refresh is returned as the
// error, so an outage is not hidden for ever.
const maxStale = 10 * time.Minute

// cacheAgeDesc describes the age of the data a cached collector served. Every
// cached collector shares it and tells itself apart by the collector label.
var cacheAgeDesc = prometheus.NewDesc(
	"spacelift_scrape_collector_cache_age_seconds",
	"Age of the data a cached collector served on the last scrape, 0 right after a request to the Spacelift API.",
	[]string{"collector"},
	nil)

// WithCacheTTL serves c's last outcome to every scrape, however often the
// exporter is scraped and by however many scrapers, instead of querying the
// API each time. A ttl of zero or less returns c unchanged, so the default
// behaviour is untouched.
//
// How long an outcome is served, timed from when its request returned,
// depends on what it was:
//   - a success or an unsupported result is served for ttl;
//   - a failure is served for min(ttl, 30s), and the next scrape after that
//     queries again;
//   - a failure that came with the request's own context ended (the scrape's
//     deadline passed) is not kept, so the next scrape queries again.
//
// A failed refresh does not evict good data: while the last success is no
// more than ttl plus 10 minutes old, it is served in place of the failure,
// and refreshes are retried every min(ttl, 30s) as above. Past that, the
// failure is returned. An unsupported result is never covered this way.
//
// Scrapes that arrive while a request is in flight wait for it and are
// served its outcome, however long it took. A scrape whose own context ends
// first returns its context's error and leaves the request running for the
// others. The request runs detached from the scrape that started it, bounded
// by that scrape's deadline when it has one.
//
// On success the collector also emits
// spacelift_scrape_collector_cache_age_seconds, the age of the data served.
// spacelift_scrape_collector_duration_seconds then reads near zero for a
// scrape served from cache.
//
// The client passed to Collect is not part of the cache: one cache serves
// one client, as the exporter has.
//
// It is meant for collectors whose data is slow-moving and tolerates
// staleness, such as aggregates.
func WithCacheTTL(c Collector, ttl time.Duration) Collector {
	if ttl <= 0 {
		return c
	}

	return &cachingCollector{Collector: c, ttl: ttl, now: time.Now}
}

// cachingCollector wraps a collector with a TTL cache of its last outcome.
type cachingCollector struct {
	Collector
	ttl time.Duration
	now func() time.Time

	// mutex guards the fields below. It is never held across a request.
	mutex sync.Mutex
	// entry is the outcome being served, if any.
	entry *cacheEntry
	// good is the last success, served in place of a failed refresh.
	good *cacheEntry
	// call is the request in flight, if any. Scrapes that find no fresh
	// entry join it rather than issuing their own.
	call *cacheCall
}

// cacheEntry is the outcome of one API request and how long it may be served.
//
// Its metrics are shared by every scrape it serves and must not be modified.
// Const metrics are immutable, and the slice is clipped so that appending to
// it copies.
type cacheEntry struct {
	metrics     []prometheus.Metric
	err         error
	unsupported bool
	fetchedAt   time.Time
	ttl         time.Duration
}

// cacheCall is one request in flight. Its outcome is set before done closes.
type cacheCall struct {
	done    chan struct{}
	metrics []prometheus.Metric
	err     error

	// joined counts the scrapes waiting on the call, for tests.
	joined int
}

// Describe implements Collector.
func (c *cachingCollector) Describe(ch chan<- *prometheus.Desc) {
	c.Collector.Describe(ch)
	ch <- cacheAgeDesc
}

// Collect implements Collector. It serves the cached outcome while it is
// fresh, and otherwise waits for a request, joining the one in flight or
// starting one.
func (c *cachingCollector) Collect(ctx context.Context, api client.Client) ([]prometheus.Metric, error) {
	c.mutex.Lock()
	if now := c.now(); c.entry != nil && now.Sub(c.entry.fetchedAt) < c.entry.ttl {
		defer c.mutex.Unlock()

		return c.serve(c.entry, now)
	}
	if err := ctx.Err(); err != nil {
		c.mutex.Unlock()

		return nil, err
	}
	call := c.call
	if call == nil {
		call = &cacheCall{done: make(chan struct{})}
		c.call = call
		go c.fetch(ctx, api, call)
	}
	call.joined++
	c.mutex.Unlock()

	select {
	case <-call.done:
		return call.metrics, call.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// VersionGatedFields passes on the fields the wrapped collector declares
// through VersionGated, so a missing declared field still reads as
// unsupported once the collector is cached. It returns nil when the wrapped
// collector declares none, and an empty list matches no missing field.
func (c *cachingCollector) VersionGatedFields() []string {
	if gated, ok := c.Collector.(VersionGated); ok {
		return gated.VersionGatedFields()
	}

	return nil
}

// fetch runs one request for call and stores its outcome.
//
// The request does not end with the scrape that started it, so the scrapes
// waiting on it are not failed by that scrape giving up. It still ends at
// that scrape's deadline, if any, so it stays bounded.
//
// A success or an unsupported result is kept for the full TTL. Any other
// error is kept for at most errorTTL, and not at all when the request's own
// context ended: caching it would fail the next scrapes, whose deadlines have
// not passed. Every scrape waiting on the call is served the same outcome
// either way, and the next scrape to arrive issues a new request.
//
// The unsupported check classifies against the wrapped collector, so the
// fields it declares through VersionGated are honoured.
func (c *cachingCollector) fetch(scrapeCtx context.Context, api client.Client, call *cacheCall) {
	ctx := context.WithoutCancel(scrapeCtx)
	if deadline, ok := scrapeCtx.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}

	metrics, err := c.request(ctx, api)
	ended := ctx.Err() != nil

	c.mutex.Lock()
	defer c.mutex.Unlock()

	now := c.now()
	entry := &cacheEntry{metrics: slices.Clip(metrics), err: err, fetchedAt: now, ttl: c.ttl}
	switch {
	case err == nil:
		c.entry, c.good = entry, entry
	case errors.Is(classify(c.Collector, err), ErrNotSupported):
		entry.metrics, entry.unsupported = nil, true
		c.entry, c.good = entry, nil
	case ended:
		entry.metrics = nil
	default:
		entry.metrics, entry.ttl = nil, min(c.ttl, errorTTL)
		c.entry = entry
	}

	call.metrics, call.err = c.serve(entry, now)
	c.call = nil
	close(call.done)
}

// serve returns what a scrape at now is served for entry: the last success in
// place of a failure while it is within maxStale past its TTL, and the age of
// the data alongside a success. The caller holds the mutex.
func (c *cachingCollector) serve(entry *cacheEntry, now time.Time) ([]prometheus.Metric, error) {
	if entry.err != nil && !entry.unsupported && c.good != nil && now.Sub(c.good.fetchedAt) <= c.ttl+maxStale {
		entry = c.good
	}
	if entry.err != nil {
		return nil, entry.err
	}

	// entry.metrics is clipped, so this append copies and the cached slice is
	// never written to.
	return append(entry.metrics, prometheus.MustNewConstMetric(
		cacheAgeDesc, prometheus.GaugeValue, now.Sub(entry.fetchedAt).Seconds(), c.Name())), nil
}

// request runs the wrapped collector once, turning a panic into an error so
// that it is cached like any other failure instead of being retried on every
// scrape, and so that it cannot take the process down from the request's own
// goroutine.
func (c *cachingCollector) request(ctx context.Context, api client.Client) (metrics []prometheus.Metric, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			metrics, err = nil, fmt.Errorf("collector panicked: %v", recovered)
		}
	}()

	return c.Collector.Collect(ctx, api)
}
