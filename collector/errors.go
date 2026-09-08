package collector

import (
	"fmt"
	"strings"
)

// machineSessionError is the message the API returns for fields a machine
// (API key) session may not read. Spacelift SaaS stopped gating the fields this
// exporter reads in August 2026 (backend #16058: publicWorkerPool, usage,
// metrics), but Self-Hosted releases older than that still gate them, and other
// fields (`stacks`, `searchStacksSuggestions`, `Stack.state`) remain gated
// everywhere.
//
// Matching on the message is unpleasant, but the GraphQL response carries no
// machine-readable error code, and the alternative — failing the collector — is
// worse, because on a gated backend the ungated collectors still work.
const machineSessionError = "not available for machine sessions"

// classify converts an API error into ErrNotSupported when the field is simply
// unavailable to this session, and leaves genuine failures alone.
func classify(err error, name string) error {
	if err == nil {
		return nil
	}

	if strings.Contains(err.Error(), machineSessionError) {
		return fmt.Errorf("%s: %w", name, ErrNotSupported)
	}

	return fmt.Errorf("%s: %w", name, err)
}
