package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/shurcooL/graphql"
	"golang.org/x/oauth2"

	"github.com/spacelift-io/prometheus-exporter/client/session"
	"github.com/spacelift-io/prometheus-exporter/logging"
)

type client struct {
	wraps   *http.Client
	session session.Session
}

func New(wraps *http.Client, session session.Session) *client {
	return &client{wraps: wraps, session: session}
}

func (c *client) Mutate(ctx context.Context, mutation interface{}, variables map[string]interface{}) error {
	apiClient, err := c.apiClient(ctx)
	if err != nil {
		return err
	}

	err = apiClient.Mutate(ctx, mutation, variables)
	if err != nil && strings.Contains(err.Error(), "unauthorized") {
		logger := logging.FromContext(ctx).Sugar()
		logger.Warn("Server returned an unauthorized response - retrying request with a new token")
		c.session.RefreshToken(ctx)

		// Try again in case refreshing the token fixes the problem
		apiClient, err = c.apiClient(ctx)
	}

	return err
}

func (c *client) Query(ctx context.Context, query interface{}, variables map[string]interface{}) error {
	apiClient, err := c.apiClient(ctx)
	if err != nil {
		return err
	}

	err = apiClient.Query(ctx, query, variables)
	if err != nil && strings.Contains(err.Error(), "unauthorized") {
		logger := logging.FromContext(ctx).Sugar()
		logger.Warn("Server returned an unauthorized response - retrying request with a new token")
		c.session.RefreshToken(ctx)

		// Try again in case refreshing the token fixes the problem
		apiClient, err = c.apiClient(ctx)
	}

	return err
}

func (c *client) URL(format string, a ...interface{}) (string, error) {
	endpoint := c.session.Endpoint()

	endpointURL, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}

	endpointURL.Path = fmt.Sprintf(format, a...)

	return endpointURL.String(), nil
}

func (c *client) apiClient(ctx context.Context) (*graphql.Client, error) {
	bearerToken, err := c.session.BearerToken(ctx)
	if err != nil {
		return nil, err
	}

	return graphql.NewClient(c.session.Endpoint(), oauth2.NewClient
