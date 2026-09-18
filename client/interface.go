package client

import (
	"context"
)

// Client abstracts away Spacelift's client API.
type Client interface {
	// Query executes a single GraphQL query request.
	Query(ctx context.Context, query interface{}, variables map[string]interface{}) error

	// QueryNamed executes a single GraphQL query request whose operation name
	// is "PrometheusExporter_" followed by operation, so that Spacelift can
	// attribute API load to the part of the exporter that issued it. Passing
	// "WorkerPools" sends the request as "PrometheusExporter_WorkerPools".
	QueryNamed(ctx context.Context, query interface{}, variables map[string]interface{}, operation string) error
}
