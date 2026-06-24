package session

import (
	"context"
	"net/http"

	"github.com/pkg/errors"
)

// New creates a session from a static API key ID and secret.
func New(ctx context.Context, client *http.Client, endpoint, keyID, keySecret string) (Session, error) {
	return NewWithSecretProvider(ctx, client, endpoint, keyID, StaticSecret(keySecret))
}

// NewWithSecretProvider creates a session whose API key secret is resolved
// through the provider on every token refresh. This lets callers supply a
// secret that rotates on disk (for example, a Kubernetes projected
// service-account token used with a Spacelift OIDC API key).
func NewWithSecretProvider(ctx context.Context, client *http.Client, endpoint, keyID string, secret SecretProvider) (Session, error) {
	session, err := FromAPIKeyProvider(ctx, client, endpoint, keyID, secret)
	if err != nil {
		return nil, errors.Wrap(err, "could not create session from Spacelift API key")
	}

	return session, nil
}
