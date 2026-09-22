package session

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newExchangeStub stands in for the apiKeyUser token exchange. The response to
// each exchange is produced from its 1-based sequence number, so a test controls
// what the construction-time exchange and every later one return.
func newExchangeStub(t *testing.T, respond func(seq int64) (jwt string, validUntil time.Time, status int)) (*httptest.Server, *atomic.Int64) {
	t.Helper()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jwt, validUntil, status := respond(calls.Add(1))
		if status != http.StatusOK {
			w.WriteHeader(status)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":{"apiKeyUser":{"jwt":%q,"validUntil":%d}}}`, jwt, validUntil.Unix())
	}))
	t.Cleanup(server.Close)

	return server, &calls
}

// TestConcurrentBearerTokenCallersExchangeOnce covers collectors sharing one
// session and all finding the token stale at the same instant. One exchange
// must serve all of them.
func TestConcurrentBearerTokenCallersExchangeOnce(t *testing.T) {
	server, calls := newExchangeStub(t, func(seq int64) (string, time.Time, int) {
		if seq == 1 {
			// The construction-time token is already stale, so every
			// BearerToken call below enters the refresh path.
			return "stale", time.Now().Add(-time.Minute), http.StatusOK
		}

		return "refreshed", time.Now().Add(time.Hour), http.StatusOK
	})

	session, err := FromAPIKey(context.Background(), server.Client(), server.URL, "key-id", "key-secret")
	if err != nil {
		t.Fatalf("FromAPIKey: %v", err)
	}

	const callers = 32
	tokens := make([]string, callers)
	errs := make([]error, callers)
	var waitGroup sync.WaitGroup
	for i := range callers {
		waitGroup.Go(func() { tokens[i], errs[i] = session.BearerToken(context.Background()) })
	}
	waitGroup.Wait()

	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: BearerToken: %v", i, errs[i])
		}
		if tokens[i] != "refreshed" {
			t.Errorf("caller %d got token %q, want refreshed", i, tokens[i])
		}
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("token exchanges = %d, want 2 (construction plus one refresh)", got)
	}
}

// TestBearerTokenRecoversAfterFailedExchange pins that a failed exchange is
// reported to the caller rather than papered over with the stale token, and
// that the next caller tries again.
func TestBearerTokenRecoversAfterFailedExchange(t *testing.T) {
	server, calls := newExchangeStub(t, func(seq int64) (string, time.Time, int) {
		switch seq {
		case 1:
			return "stale", time.Now().Add(-time.Minute), http.StatusOK
		case 2:
			return "", time.Time{}, http.StatusInternalServerError
		default:
			return "refreshed", time.Now().Add(time.Hour), http.StatusOK
		}
	})

	session, err := FromAPIKey(context.Background(), server.Client(), server.URL, "key-id", "key-secret")
	if err != nil {
		t.Fatalf("FromAPIKey: %v", err)
	}

	if token, err := session.BearerToken(context.Background()); err == nil {
		t.Fatalf("BearerToken() = %q, want an error from the failed exchange", token)
	}
	token, err := session.BearerToken(context.Background())
	if err != nil {
		t.Fatalf("BearerToken() after a failed exchange: %v", err)
	}
	if token != "refreshed" {
		t.Errorf("BearerToken() = %q, want refreshed", token)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("token exchanges = %d, want 3", got)
	}
}
