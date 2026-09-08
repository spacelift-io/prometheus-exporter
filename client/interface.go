package client

import (
	"context"
)

// Client abstracts away Spacelift's client API.
type Client interface {
	// Query executes a single GraphQL query request.
	Query(ctx context.Context, query interface{}, variables map[string]interface{}) error
}

// NamedClient extends Client with collector-specific GraphQL operation names.
// Keeping this separate preserves the original Client interface for external
// callers and mocks.
type NamedClient interface {
	Client

	// QueryNamed prefixes operation with "PrometheusExporter", so passing
	// "WorkerPools" sends "PrometheusExporterWorkerPools".
	QueryNamed(
		ctx context.Context,
		query interface{},
		variables map[string]interface{},
		operation string,
	) error
}
