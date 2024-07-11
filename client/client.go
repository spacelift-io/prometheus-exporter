package client

import (
	"context"
	"net/http"
	"strings"

	"github.com/hasura/go-graphql-client"
	"golang.org/x/oauth2"

	"github.com/spacelift-io/prometheus-exporter/client/session"
	"github.com/spacelift-io/prometheus-exporter/logging"
)

type client struct {
	wraps   *http.Client
	session session.Session
}

// New returns a new instance of a Spacelift Client.
func New(wraps *http.Client, session session.Session) Client {
	return &client{wraps: wraps, session: session}
}

func (c *client) Query(ctx context.Context, query interface{}, variables map[string]interface{}) error {
	logger := logging.FromContext(ctx).Sugar()
	apiClient, err := c.apiClient(ctx)
	if err != nil {
		return err
	}

	apiClient.WithRequestModifier(func(req *http.Request) {
		req.Header.Add("Spacelift-Client-Type", "prometheus-exporter")
	})

	err = apiClient.Query(ctx, query, variables)
	if err != nil && strings.Contains(err.Error(), "unauthorized") {
		logger.Warn("Server returned an unauthorized response - retrying request with a new token")
		c.session.RefreshToken(ctx)

		// Try again in case refreshing the token fixes the problem
		apiClient, err = c.apiClient(ctx)
		if err != nil {
			return err
		}

		err = apiClient.Query(ctx, query, variables)
	}

	return err
}

func (c *client) apiClient(ctx context.Context) (*graphql.Client, error) {
	bearerToken, err := c.session.BearerToken(ctx)
	if err != nil {
		return nil, err
	}

	return graphql.NewClient(c.session.Endpoint(), oauth2.NewClient(
		context.WithValue(ctx, oauth2.HTTPClient, c.wraps), oauth2.StaticTokenSource(
			&oauth2.Token{AccessToken: bearerToken},
		),
	)), nil
}
