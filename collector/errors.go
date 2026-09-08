package collector

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNotSupported marks a collector whose data is not available on this
// deployment, tier or API key. It is reported as
// spacelift_scrape_collector_supported=0 rather than as a failure: the
// collector is fine, the data simply is not there for it to read.
var ErrNotSupported = errors.New("not supported on this Spacelift deployment")

// machineSessionError is the message the API returns for fields a machine
// (API key) session may not read. Spacelift SaaS stopped gating the fields
// this exporter reads in August 2026, but Self-Hosted releases older than that
// still gate publicWorkerPool, usage and metrics.
//
// Matching on the message is unpleasant, but the GraphQL response carries no
// machine-readable error code, and the alternative of failing the collector is
// worse: on a gated backend the ungated collectors still work.
const machineSessionError = "not available for machine sessions"

// classify marks an API error as ErrNotSupported when the field is simply
// unavailable to this session, and leaves genuine failures alone. The original
// error is kept in the chain: it is the only place the API's own message
// survives, and the strict-mode failure body and the logs would otherwise
// report a bare sentinel.
func classify(err error) error {
	if err != nil && strings.Contains(err.Error(), machineSessionError) {
		return fmt.Errorf("%w: %v", ErrNotSupported, err)
	}

	return err
}
