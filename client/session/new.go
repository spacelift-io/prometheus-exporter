package session

import (
	"context"
	"net/http"

	"github.com/pkg/errors"
)

// New creates a session using the default chain of credentials sources:
// first the environment, then the current credentials file.
func New(ctx context.Context, client *http.Client, endpoint, keyID, keySecret string) (Session, error) {
	session, err := FromAPIKey(ctx, client, endpoint, keyID, keySecret)
	if err != nil {
		return nil, errors.Wrap(err, "could not create session from Spacelift API key")
	}

	return session, nil
}
