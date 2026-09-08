package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/hasura/go-graphql-client"

	"github.com/spacelift-io/prometheus-exporter/client/session"
	"github.com/spacelift-io/prometheus-exporter/logging"
)

// operationPrefix is prepended to every collector's operation name, so that
// Spacelift can attribute API load both to the exporter as a whole and to the
// specific collector responsible. It must be applied to retries too, otherwise
// the reissued request arrives anonymous and unattributable.
const operationPrefix = "PrometheusExporter"

type client struct {
	wraps        *http.Client
	session      session.Session
	refreshMutex sync.Mutex
	refreshToken string
	refreshState *refreshState
}

// refreshState is captured when a request obtains its bearer token. Requests in
// that wave share ordinary failures and byte-identical results. A live waiter
// retries when the leader's attempt ended only because its context expired.
type refreshState struct {
	started            bool
	complete           bool
	leaderContextEnded bool
	done               chan struct{}
	err                error
}

// New returns a new instance of a Spacelift Client.
func New(wraps *http.Client, session session.Session) Client {
	return &client{wraps: wraps, session: session}
}

// NewNamed returns a client that supports collector-specific GraphQL operation
// names while retaining the original Client API.
func NewNamed(wraps *http.Client, session session.Session) NamedClient {
	return &client{wraps: wraps, session: session}
}

func (c *client) Query(ctx context.Context, query interface{}, variables map[string]interface{}) error {
	return c.QueryNamed(ctx, query, variables, "")
}

func (c *client) QueryNamed(
	ctx context.Context,
	query interface{},
	variables map[string]interface{},
	operation string,
) error {
	name := graphql.OperationName(operationPrefix + operation)
	logger := logging.FromContext(ctx).Sugar()
	apiClient, token, refresh, err := c.apiClient(ctx)
	if err != nil {
		return err
	}

	err = apiClient.Query(ctx, query, variables, name)
	if err != nil && strings.Contains(err.Error(), "unauthorized") {
		logger.Warn("Server returned an unauthorized response - retrying request with a new token")
		if err := c.refreshBearerToken(ctx, token, refresh); err != nil {
			return err
		}

		// Try again in case refreshing the token fixes the problem
		apiClient, _, _, err = c.apiClient(ctx)
		if err != nil {
			return err
		}

		err = apiClient.Query(ctx, query, variables, name)
	}

	return err
}

// refreshBearerToken coalesces unauthorized responses that used the same
// refresh state while allowing each waiter to honor its own context.
func (c *client) refreshBearerToken(ctx context.Context, failedToken string, refresh *refreshState) error {
	for {
		c.refreshMutex.Lock()
		if refresh.complete {
			if err := ctx.Err(); err != nil {
				c.refreshMutex.Unlock()

				return err
			}
			if refresh.leaderContextEnded {
				refresh = c.refreshState
				c.refreshMutex.Unlock()

				continue
			}
			err := refresh.err
			c.refreshMutex.Unlock()

			return err
		}
		if refresh.started {
			done := refresh.done
			c.refreshMutex.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		refresh.started = true
		refresh.done = make(chan struct{})
		c.refreshMutex.Unlock()

		err := c.refreshSession(ctx, failedToken)

		// Requests created after this attempt get a fresh state, so a later
		// independent unauthorized response can retry even if this one failed.
		c.refreshMutex.Lock()
		refresh.err = err
		refresh.complete = true
		refresh.leaderContextEnded = leaderContextEnded(ctx, err)
		if c.refreshState == refresh {
			c.refreshState = &refreshState{}
		}
		close(refresh.done)
		c.refreshMutex.Unlock()

		return err
	}
}

func (c *client) refreshSession(ctx context.Context, failedToken string) error {
	currentToken, err := c.session.BearerToken(ctx)
	if err != nil {
		return fmt.Errorf("could not read current bearer token: %w", err)
	}
	if currentToken != failedToken {
		return nil
	}
	if err := c.session.RefreshToken(ctx); err != nil {
		return fmt.Errorf("could not refresh bearer token: %w", err)
	}

	return nil
}

func (c *client) apiClient(ctx context.Context) (*graphql.Client, string, *refreshState, error) {
	bearerToken, err := c.session.BearerToken(ctx)
	if err != nil {
		return nil, "", nil, err
	}
	refresh := c.refreshStateForToken(bearerToken)

	return graphql.NewClient(c.session.Endpoint(), c.wraps).WithRequestModifier(func(r *http.Request) {
		r.Header.Add("Spacelift-Client-Type", "prometheus-exporter")
		r.Header.Set("Authorization", fmt.Sprintf("Bearer %s", bearerToken))
	}), bearerToken, refresh, nil
}

func (c *client) refreshStateForToken(token string) *refreshState {
	c.refreshMutex.Lock()
	defer c.refreshMutex.Unlock()

	if c.refreshState == nil || c.refreshToken != token {
		c.refreshToken = token
		c.refreshState = &refreshState{}
	}

	return c.refreshState
}

func leaderContextEnded(ctx context.Context, err error) bool {
	ctxErr := ctx.Err()

	return ctxErr != nil && errors.Is(err, ctxErr)
}
