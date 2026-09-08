package session

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/hasura/go-graphql-client"
)

// SecretProvider returns the API key secret to use when exchanging credentials
// for a bearer token. It is invoked on every token exchange, allowing callers
// to supply rotating secrets such as projected Kubernetes service-account
// tokens used with Spacelift's OIDC API keys.
type SecretProvider func() (string, error)

// StaticSecret wraps a fixed secret in a SecretProvider.
func StaticSecret(secret string) SecretProvider {
	return func() (string, error) { return secret, nil }
}

// FromAPIKey builds a Spacelift session from a combination of endpoint, API key
// ID and a static API key secret.
func FromAPIKey(ctx context.Context, client *http.Client, endpoint, keyID, keySecret string) (Session, error) {
	return FromAPIKeyProvider(ctx, client, endpoint, keyID, StaticSecret(keySecret))
}

// FromAPIKeyProvider builds a Spacelift session that resolves the API key
// secret through the provider on every token exchange.
func FromAPIKeyProvider(ctx context.Context, client *http.Client, endpoint, keyID string, secret SecretProvider) (Session, error) {
	if secret == nil {
		return nil, fmt.Errorf("API key secret provider must not be nil")
	}

	out := &apiKey{
		apiToken: apiToken{
			client:   client,
			endpoint: endpoint,
			timer:    time.Now,
		},
		keyID:  keyID,
		secret: secret,
	}

	if err := out.exchange(ctx); err != nil {
		return nil, err
	}

	return out, nil
}

type apiKey struct {
	apiToken
	keyID        string
	secret       SecretProvider
	refreshMutex sync.Mutex
	refreshState *apiKeyRefreshState
}

// apiKeyRefreshState identifies one refresh wave. Callers capture the current
// state before checking token freshness, so a caller that observed an old wave
// can consume its result even if the exchange completed before it reached the
// refresh lock.
type apiKeyRefreshState struct {
	started            bool
	complete           bool
	leaderContextEnded bool
	done               chan struct{}
	err                error
}

func (g *apiKey) BearerToken(ctx context.Context) (string, error) {
	refresh := g.currentRefreshState()
	if !g.isFresh() {
		if err := g.refresh(ctx, false, refresh); err != nil {
			return "", err
		}
	}

	return g.apiToken.BearerToken(ctx)
}

func (g *apiKey) RefreshToken(ctx context.Context) error {
	return g.refresh(ctx, true, g.currentRefreshState())
}

func (g *apiKey) currentRefreshState() *apiKeyRefreshState {
	g.refreshMutex.Lock()
	defer g.refreshMutex.Unlock()

	if g.refreshState == nil {
		g.refreshState = &apiKeyRefreshState{}
	}

	return g.refreshState
}

// refresh shares one exchange result with callers that captured the same state.
// Waiters honor their own contexts and retry when only the leader's context
// ended. Completion rotates the current state for later callers.
func (g *apiKey) refresh(ctx context.Context, force bool, refresh *apiKeyRefreshState) error {
	for {
		g.refreshMutex.Lock()
		if refresh.complete {
			if err := ctx.Err(); err != nil {
				g.refreshMutex.Unlock()

				return err
			}
			if refresh.leaderContextEnded {
				refresh = g.refreshState
				g.refreshMutex.Unlock()

				continue
			}
			err := refresh.err
			g.refreshMutex.Unlock()

			return err
		}
		if refresh.started {
			done := refresh.done
			g.refreshMutex.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		// Another caller may have refreshed before this caller acquired the lock.
		if !force && g.isFresh() {
			g.refreshMutex.Unlock()
			return nil
		}

		refresh.started = true
		refresh.done = make(chan struct{})
		g.refreshMutex.Unlock()

		err := g.exchange(ctx)

		g.refreshMutex.Lock()
		refresh.err = err
		refresh.complete = true
		refresh.leaderContextEnded = leaderContextEnded(ctx, err)
		if g.refreshState == refresh {
			g.refreshState = &apiKeyRefreshState{}
		}
		close(refresh.done)
		g.refreshMutex.Unlock()

		return err
	}
}

func leaderContextEnded(ctx context.Context, err error) bool {
	ctxErr := ctx.Err()

	return ctxErr != nil && errors.Is(err, ctxErr)
}

func (g *apiKey) exchange(ctx context.Context) error {
	secret, err := g.secret()
	if err != nil {
		return fmt.Errorf("could not resolve API key secret: %w", err)
	}

	var mutation struct {
		APIKeyUser user `graphql:"apiKeyUser(id: $id, secret: $secret)"`
	}

	variables := map[string]interface{}{
		"id":     graphql.ID(g.keyID),
		"secret": graphql.String(secret),
	}

	if err := g.mutate(ctx, &mutation, variables); err != nil {
		return fmt.Errorf("could not exchange API key and secret for token: %w", err)
	}

	g.setJWT(&mutation.APIKeyUser)

	return nil
}
