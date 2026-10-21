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

// missingFieldError starts the validation error the API returns for a field
// its schema does not have, as on a Self-Hosted release older than the field.
// The document is otherwise valid, so the collector is not broken: this
// backend cannot supply its data. TestQueryShape pins every document, so a
// typo in one fails there rather than hiding behind this.
const missingFieldError = "Cannot query field"

// classify marks an API error as ErrNotSupported when the field is simply
// unavailable to this session or deployment, and leaves genuine failures
// alone. The original error is kept in the chain: it is the only place the
// API's own message survives, and the strict-mode failure body and the logs
// would otherwise report a bare sentinel.
func classify(err error) error {
	if err == nil {
		return nil
	}

	for _, unsupported := range []string{machineSessionError, missingFieldError} {
		if strings.Contains(err.Error(), unsupported) {
			return fmt.Errorf("%w: %v", ErrNotSupported, err)
		}
	}

	return err
}
