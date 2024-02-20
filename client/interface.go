package client

import (
	"context"
)

// Client abstracts away Spacelift's client API.
type Client interface {
	// Query executes a single GraphQL query request.
	Query(context.Context, interface{}, map[string]interface{}) error
}
