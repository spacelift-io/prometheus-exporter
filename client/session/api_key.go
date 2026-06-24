package session

import (
	"context"
	"fmt"
	"net/http"
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
	keyID  string
	secret SecretProvider
}

func (g *apiKey) BearerToken(ctx context.Context) (string, error) {
	if !g.isFresh() {
		if err := g.exchange(ctx); err != nil {
			return "", err
		}
	}

	return g.apiToken.BearerToken(ctx)
}

func (g *apiKey) RefreshToken(ctx context.Context) error {
	return g.exchange(ctx)
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
