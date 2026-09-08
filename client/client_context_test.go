package client

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func waitForClientRefreshTestValue[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for refresh result")
		var zero T

		return zero
	}
}

type contextRefreshSession struct {
	mutex        sync.Mutex
	token        string
	refreshes    int
	firstStarted chan struct{}
}

func (s *contextRefreshSession) BearerToken(context.Context) (string, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	return s.token, nil
}

func (*contextRefreshSession) Endpoint() string { return "" }

func (s *contextRefreshSession) RefreshToken(ctx context.Context) error {
	s.mutex.Lock()
	s.refreshes++
	first := s.refreshes == 1
	if !first {
		s.token = "fresh"
	}
	s.mutex.Unlock()

	if first {
		close(s.firstStarted)
		<-ctx.Done()

		return ctx.Err()
	}

	return nil
}

func TestRefreshWaitersUseTheirOwnContext(t *testing.T) {
	session := &contextRefreshSession{token: "stale", firstStarted: make(chan struct{})}
	refresh := &refreshState{}
	client := &client{session: session, refreshToken: "stale", refreshState: refresh}

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	leaderResult := make(chan error, 1)
	go func() { leaderResult <- client.refreshBearerToken(leaderCtx, "stale", refresh) }()
	waitForClientRefreshTestValue(t, session.firstStarted)

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterResult := make(chan error, 1)
	go func() { waiterResult <- client.refreshBearerToken(waiterCtx, "stale", refresh) }()
	cancelWaiter()
	if err := waitForClientRefreshTestValue(t, waiterResult); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v, want context.Canceled", err)
	}

	liveResult := make(chan error, 1)
	go func() { liveResult <- client.refreshBearerToken(context.Background(), "stale", refresh) }()
	cancelLeader()
	if err := waitForClientRefreshTestValue(t, leaderResult); !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error = %v, want context.Canceled", err)
	}
	if err := waitForClientRefreshTestValue(t, liveResult); err != nil {
		t.Fatalf("live waiter error = %v, want nil after retry", err)
	}

	session.mutex.Lock()
	defer session.mutex.Unlock()
	if session.refreshes != 2 || session.token != "fresh" {
		t.Fatalf("session after retry = (%d refreshes, %q), want (2, fresh)", session.refreshes, session.token)
	}
}
