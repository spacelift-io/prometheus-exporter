package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func waitForAPIKeyRefreshTestValue[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for API key refresh result")
		var zero T

		return zero
	}
}

// newExchangeStub is a stand-in for the apiKeyUser token exchange. The
// response for each request is produced from its 1-based sequence number, so a
// test controls exactly what the first exchange (construction) and every later
// exchange return.
func newExchangeStub(t *testing.T, respond func(seq int64) (jwt string, validUntil int64, status int)) (*httptest.Server, *int64) {
	t.Helper()

	var calls int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seq := atomic.AddInt64(&calls, 1)

		jwt, validUntil, status := respond(seq)
		if status != http.StatusOK {
			w.WriteHeader(status)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":{"apiKeyUser":{"jwt":%q,"validUntil":%d}}}`, jwt, validUntil)
	}))
	t.Cleanup(server.Close)

	return server, &calls
}

// TestConcurrentBearerTokenRefreshesOnce exercises the production apiKey type
// under the exact interleaving the exporter creates: several collectors
// sharing one session, all discovering a stale token at the same instant.
//
// This exercises the refresh-wave state in BearerToken. Every other concurrency
// test in this repository runs against a hand-rolled fake session, so without
// this test the code that actually ships is the one piece with no coverage.
func TestConcurrentBearerTokenRefreshesOnce(t *testing.T) {
	server, calls := newExchangeStub(t, func(seq int64) (string, int64, int) {
		if seq == 1 {
			// The construction-time exchange hands out a token that is
			// already stale, so every subsequent BearerToken call sees
			// isFresh() == false and enters the refresh path.
			return "stale-token", time.Now().Add(-time.Minute).Unix(), http.StatusOK
		}

		return "refreshed-token", time.Now().Add(time.Hour).Unix(), http.StatusOK
	})

	session, err := FromAPIKey(context.Background(), server.Client(), server.URL, "key-id", "key-secret")
	if err != nil {
		t.Fatalf("FromAPIKey: %v", err)
	}

	const goroutines = 32

	tokens := make([]string, goroutines)
	errs := make([]error, goroutines)

	var waitGroup sync.WaitGroup
	waitGroup.Add(goroutines)
	for i := range goroutines {
		go func() {
			defer waitGroup.Done()
			tokens[i], errs[i] = session.BearerToken(context.Background())
		}()
	}
	waitGroup.Wait()

	for i := range goroutines {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: BearerToken: %v", i, errs[i])
		}
		if tokens[i] != "refreshed-token" {
			t.Errorf("goroutine %d got token %q, want the refreshed one", i, tokens[i])
		}
	}

	// Construction plus exactly one coalesced refresh. Without refresh-wave
	// sharing this is up to 1+32 exchanges, each burning an API call and a
	// secret resolution.
	if got := atomic.LoadInt64(calls); got != 2 {
		t.Errorf("token exchange requests = %d, want 2 (construction + one coalesced refresh)", got)
	}
}

// TestBearerTokenRecoversAfterFailedExchange pins two properties of the
// refresh path: a failed exchange surfaces as an error to the caller rather
// than handing back the stale token, and the failure is not sticky — the next
// caller retries and succeeds.
func TestBearerTokenRecoversAfterFailedExchange(t *testing.T) {
	server, calls := newExchangeStub(t, func(seq int64) (string, int64, int) {
		switch seq {
		case 1:
			return "stale-token", time.Now().Add(-time.Minute).Unix(), http.StatusOK
		case 2:
			return "", 0, http.StatusInternalServerError
		default:
			return "refreshed-token", time.Now().Add(time.Hour).Unix(), http.StatusOK
		}
	})

	session, err := FromAPIKey(context.Background(), server.Client(), server.URL, "key-id", "key-secret")
	if err != nil {
		t.Fatalf("FromAPIKey: %v", err)
	}

	apiKey := session.(*apiKey)
	observed := apiKey.currentRefreshState()
	_, firstErr := session.BearerToken(context.Background())
	if firstErr == nil {
		t.Fatal("BearerToken succeeded although the exchange returned 500; a stale token must not be handed out silently")
	}
	sharedErr := apiKey.refresh(context.Background(), false, observed)
	if sharedErr == nil || sharedErr.Error() != firstErr.Error() {
		t.Errorf("shared refresh error = %v, want %v", sharedErr, firstErr)
	}
	if got := atomic.LoadInt64(calls); got != 2 {
		t.Errorf("token exchanges after shared failure = %d, want 2", got)
	}

	token, err := session.BearerToken(context.Background())
	if err != nil {
		t.Fatalf("BearerToken after a failed exchange: %v", err)
	}
	if token != "refreshed-token" {
		t.Errorf("token after recovery = %q, want the refreshed one", token)
	}

	if got := atomic.LoadInt64(calls); got != 3 {
		t.Errorf("token exchange requests = %d, want 3 (construction, failed refresh, successful retry)", got)
	}
}

func TestRefreshWaveSharesShortLivedResult(t *testing.T) {
	server, calls := newExchangeStub(t, func(seq int64) (string, int64, int) {
		switch seq {
		case 1:
			return "stale-token", time.Now().Add(-time.Minute).Unix(), http.StatusOK
		case 2:
			return "short-lived-token", time.Now().Add(10 * time.Second).Unix(), http.StatusOK
		default:
			return "refreshed-token", time.Now().Add(time.Hour).Unix(), http.StatusOK
		}
	})

	session, err := FromAPIKey(context.Background(), server.Client(), server.URL, "key-id", "key-secret")
	if err != nil {
		t.Fatalf("FromAPIKey: %v", err)
	}

	apiKey := session.(*apiKey)
	observed := apiKey.currentRefreshState()
	if err := apiKey.refresh(context.Background(), false, observed); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if err := apiKey.refresh(context.Background(), false, observed); err != nil {
		t.Fatalf("shared refresh: %v", err)
	}
	if got := atomic.LoadInt64(calls); got != 2 {
		t.Fatalf("token exchanges = %d, want construction plus one shared exchange", got)
	}

	token, err := session.BearerToken(context.Background())
	if err != nil || token != "refreshed-token" {
		t.Fatalf("later BearerToken = (%q, %v), want (refreshed-token, nil)", token, err)
	}
	if got := atomic.LoadInt64(calls); got != 3 {
		t.Errorf("token exchanges after later retry = %d, want 3", got)
	}
}

func TestRefreshWaveSharesInternalDeadline(t *testing.T) {
	var calls int64
	deadlineErr := fmt.Errorf("HTTP client timeout: %w", context.DeadlineExceeded)
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		atomic.AddInt64(&calls, 1)

		return nil, deadlineErr
	})}
	apiKey := &apiKey{
		apiToken: apiToken{
			client:          httpClient,
			endpoint:        "http://spacelift.test",
			timer:           time.Now,
			tokenValidUntil: time.Now().Add(-time.Minute),
		},
		keyID:  "key-id",
		secret: StaticSecret("key-secret"),
	}
	observed := apiKey.currentRefreshState()

	for range 2 {
		if err := apiKey.refresh(context.Background(), false, observed); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shared refresh error = %v, want wrapped context.DeadlineExceeded", err)
		}
	}
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("token exchanges = %d, want one shared internal timeout", got)
	}
	if apiKey.currentRefreshState().started {
		t.Fatal("internal deadline started a new refresh despite the leader context remaining live")
	}
}

func TestRefreshWaitersUseTheirOwnContext(t *testing.T) {
	var calls int64
	refreshStarted := make(chan struct{})
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seq := atomic.AddInt64(&calls, 1)
		if seq == 2 {
			close(refreshStarted)
			<-r.Context().Done()

			return nil, r.Context().Err()
		}

		jwt, validUntil := "stale-token", time.Now().Add(-time.Minute).Unix()
		if seq > 2 {
			jwt, validUntil = "refreshed-token", time.Now().Add(time.Hour).Unix()
		}
		body := fmt.Sprintf(`{"data":{"apiKeyUser":{"jwt":%q,"validUntil":%d}}}`, jwt, validUntil)

		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})}

	session, err := FromAPIKey(context.Background(), httpClient, "http://spacelift.test", "key-id", "key-secret")
	if err != nil {
		t.Fatalf("FromAPIKey: %v", err)
	}

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	leaderResult := make(chan error, 1)
	go func() {
		_, err := session.BearerToken(leaderCtx)
		leaderResult <- err
	}()
	waitForAPIKeyRefreshTestValue(t, refreshStarted)

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	cancelWaiter()
	waiterResult := make(chan error, 1)
	go func() {
		_, err := session.BearerToken(waiterCtx)
		waiterResult <- err
	}()
	if err := waitForAPIKeyRefreshTestValue(t, waiterResult); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v, want context.Canceled", err)
	}

	liveResult := make(chan struct {
		token string
		err   error
	}, 1)
	go func() {
		token, err := session.BearerToken(context.Background())
		liveResult <- struct {
			token string
			err   error
		}{token: token, err: err}
	}()
	cancelLeader()
	if err := waitForAPIKeyRefreshTestValue(t, leaderResult); !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error = %v, want context.Canceled", err)
	}
	if result := waitForAPIKeyRefreshTestValue(t, liveResult); result.err != nil || result.token != "refreshed-token" {
		t.Fatalf("live waiter = (%q, %v), want (refreshed-token, nil)", result.token, result.err)
	}
	if got := atomic.LoadInt64(&calls); got != 3 {
		t.Fatalf("token exchanges = %d, want construction, canceled leader, and live retry", got)
	}
}

// TestRefreshTokenAlwaysExchanges documents intended behaviour: unlike
// BearerToken, a sequential explicit RefreshToken always exchanges, even when
// the token is fresh. Overlapping refresh calls may share an in-flight exchange
// because they are responding to the same invalid-token window.
func TestRefreshTokenAlwaysExchanges(t *testing.T) {
	server, calls := newExchangeStub(t, func(int64) (string, int64, int) {
		return "token", time.Now().Add(time.Hour).Unix(), http.StatusOK
	})

	session, err := FromAPIKey(context.Background(), server.Client(), server.URL, "key-id", "key-secret")
	if err != nil {
		t.Fatalf("FromAPIKey: %v", err)
	}

	for i := range 2 {
		if err := session.RefreshToken(context.Background()); err != nil {
			t.Fatalf("RefreshToken %d: %v", i+1, err)
		}
	}

	if got := atomic.LoadInt64(calls); got != 3 {
		t.Errorf("token exchange requests = %d, want 3 (construction + one per explicit refresh)", got)
	}
}
