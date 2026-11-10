package collector

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// ErrNotSupported marks a collector whose data is not available on this
// deployment, tier or API key. It is reported as
// spacelift_scrape_collector_supported=0 rather than as a failure: the
// collector is fine, the data simply is not there for it to read.
var ErrNotSupported = errors.New("not supported on this Spacelift deployment")

// VersionGated is implemented by a collector whose document selects fields
// that an older backend's schema may lack. When every field the API reports
// missing is named here, the collector reports unsupported rather than failed;
// other validation messages in the same response do not change that. Any
// other missing field, such as a typo, stays a failure.
type VersionGated interface {
	// VersionGatedFields returns the names of those fields.
	VersionGatedFields() []string
}

// machineSessionError is the message the API returns for fields a machine
// (API key) session may not read. Spacelift SaaS stopped gating the fields
// this exporter reads in August 2026, but Self-Hosted releases older than that
// still gate publicWorkerPool, usage and metrics.
//
// Matching on the message is unpleasant, but the GraphQL response carries no
// machine-readable error code, and the alternative of failing the collector is
// worse: on a gated backend the ungated collectors still work.
const machineSessionError = "not available for machine sessions"

// missingFieldError starts the message the API returns, with HTTP 200, for a
// document that selects a field its schema does not have. The message names
// that field first and may end with a "Did you mean" list of other fields, so
// only the name right after this prefix is the missing one.
const missingFieldError = "Cannot query field"

var missingField = regexp.MustCompile(missingFieldError + ` "([^"]+)"`)

// classify marks an API error as ErrNotSupported when the data is simply
// unavailable to this session or deployment, and leaves genuine failures
// alone. A missing field counts as unavailable only when c declares it through
// VersionGated, and every missing field the error names must be declared. The
// original error is kept in the chain: it is the only place the API's own
// message survives, and the strict-mode failure body and the logs would
// otherwise report a bare sentinel.
func classify(c Collector, err error) error {
	if err == nil {
		return nil
	}

	message := err.Error()
	if strings.Contains(message, machineSessionError) || versionGated(c, message) {
		return fmt.Errorf("%w: %v", ErrNotSupported, err)
	}

	return err
}

// versionGated reports whether message names at least one missing field and c
// declares every field it names.
func versionGated(c Collector, message string) bool {
	gated, ok := c.(VersionGated)
	if !ok {
		return false
	}

	matches := missingField.FindAllStringSubmatch(message, -1)
	if len(matches) == 0 {
		return false
	}

	declared := gated.VersionGatedFields()
	for _, match := range matches {
		if !slices.Contains(declared, match[1]) {
			return false
		}
	}

	return true
}
