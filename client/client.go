package client

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/hasura/go-graphql-client"

	"github.com/spacelift-io/prometheus-exporter/client/session"
	"github.com/spacelift-io/prometheus-exporter/logging"
)

// operationPrefix starts the operation name of every GraphQL request, so that
// Spacelift can attribute API load to the exporter as a whole and, through the
// suffix passed to QueryNamed, to the part of it that issued the request. It
// must be applied to retries too, otherwise the reissued request arrives
// anonymous and unattributable.
const operationPrefix = "PrometheusExporter"

// operationName renders the full GraphQL operation name. The underscore keeps
// the exporter prefix and the caller's operation readable as two words in
// Spacelift's logs; a GraphQL operation name may not contain a dot or dash.
func operationName(operation string) string {
	if operation == "" {
		return operationPrefix
	}

	return operationPrefix + "_" + operation
}

type client struct {
	wraps   *http.Client
	session session.Session

	// refreshMutex serializes the refresh decision, so that concurrent
	// requests rejected with the same stale token trigger one refresh.
	refreshMutex sync.Mutex
}

// New returns a new instance of a Spacelift Client.
func New(wraps *http.Client, session session.Session) Client {
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
	name := graphql.OperationName(operationName(operation))
	logger := logging.FromContext(ctx).Sugar()
	apiClient, token, err := c.apiClient(ctx)
	if err != nil {
		return err
	}

	err = apiClient.Query(ctx, query, variables, name)
	if err != nil && strings.Contains(err.Error(), "unauthorized") {
		logger.Warn("Server returned an unauthorized response - retrying request with a new token")
		if err := c.refreshIfStillStale(ctx, token); err != nil {
			return err
		}

		// Try again in case refreshing the token fixes the problem
		apiClient, _, err = c.apiClient(ctx)
		if err != nil {
			return err
		}

		err = apiClient.Query(ctx, query, variables, name)
	}

	return err
}

// refreshIfStillStale refreshes the session unless another request has already
// replaced the token that was just rejected, in which case the retry can use
// the replacement as it is.
func (c *client) refreshIfStillStale(ctx context.Context, rejected string) error {
	c.refreshMutex.Lock()
	defer c.refreshMutex.Unlock()

	current, err := c.session.BearerToken(ctx)
	if err != nil {
		return err
	}
	if current != rejected {
		return nil
	}

	return c.session.RefreshToken(ctx)
}

func (c *client) apiClient(ctx context.Context) (*graphql.Client, string, error) {
	bearerToken, err := c.session.BearerToken(ctx)
	if err != nil {
		return nil, "", err
	}

	return graphql.NewClient(c.session.Endpoint(), c.wraps).WithRequestModifier(func(r *http.Request) {
		r.Header.Add("Spacelift-Client-Type", "prometheus-exporter")
		r.Header.Set("Authorization", fmt.Sprintf("Bearer %s", bearerToken))
	}), bearerToken, nil
}
