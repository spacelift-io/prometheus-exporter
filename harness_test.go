package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"

	"github.com/spacelift-io/prometheus-exporter/logging"
)

// updateGolden regenerates the .prom files under testdata/golden instead of
// asserting against them. Run with: go test ./... -update-golden
var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/golden/*.prom from the current output")

// fakeSession is a session.Session that hands out a static token pointing at a
// test server. It lets collector tests exercise the real client and the real
// GraphQL encoding without standing in an API-key exchange. The token refresh
// path is covered in the client package.
type fakeSession struct {
	endpoint string
}

func (s *fakeSession) BearerToken(context.Context) (string, error) { return "test-token", nil }

func (s *fakeSession) Endpoint() string { return s.endpoint }

func (s *fakeSession) RefreshToken(context.Context) error { return nil }

// graphqlStub is a stand-in for the Spacelift GraphQL API. It records every
// query body it receives and replies with a canned response, so tests can
// assert on the shape of the query as well as on the metrics produced from it.
type graphqlStub struct {
	server *httptest.Server

	// mutex guards queries and operationNames: collectors query concurrently.
	mutex sync.Mutex

	// response is a whole-account fixture. Each request receives the slice
	// of it that its document selected; see projectFixture.
	response string

	// overrides maps a GraphQL operation name to a response body that
	// replaces the fixture for that operation only, so a test can fail one
	// collector and leave the rest healthy.
	overrides map[string]string

	// queries holds the raw "query" string of every request received.
	queries []string

	// operationNames retains the envelope field that is not present in the
	// rendered query string.
	operationNames []string
}

// failOperation makes the named operation return the given body while every
// other collector keeps receiving the fixture. Without one document per
// collector there would be no way to fail one and not the others.
func (s *graphqlStub) failOperation(operation, response string) {
	if s.overrides == nil {
		s.overrides = map[string]string{}
	}

	s.overrides[operation] = response
}

// recorded returns copies of the queries and envelope operation names received
// so far, in arrival order.
func (s *graphqlStub) recorded() (queries, operationNames []string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	return slices.Clone(s.queries), slices.Clone(s.operationNames)
}

// operationOf extracts the operation name from a query document.
func operationOf(query string) string {
	name, _, _ := strings.Cut(strings.TrimPrefix(query, "query "), "{")

	return strings.TrimSpace(name)
}

// operationRootFields maps each collector's GraphQL operation name to the
// single root field it selects, so the stub can serve the right slice of a
// whole-account fixture the way the real server would. A new collector adds
// one entry.
var operationRootFields = map[string]string{
	"PrometheusExporter_PublicWorkerPool": "publicWorkerPool",
	"PrometheusExporter_WorkerPools":      "workerPools",
	"PrometheusExporter_Usage":            "usage",
	"PrometheusExporter_Aggregates":       "metrics",
}

// projectFixture narrows a whole-account fixture to the one root field a
// collector asked for.
//
// Fixtures describe an account, not a query, which keeps them readable and
// lets one file cover every collector. The GraphQL decoder rejects response
// fields the query did not select, so the stub does the narrowing a real
// server does. Errors are passed through unchanged: a fixture that carries an
// error fails every document, which is what TestCollectorSurfacesQueryErrors
// relies on.
func projectFixture(t *testing.T, response string, field string) string {
	t.Helper()

	var envelope struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors json.RawMessage            `json:"errors,omitempty"`
	}
	if err := json.Unmarshal([]byte(response), &envelope); err != nil {
		// Not a well-formed envelope: the test is asserting on a
		// malformed response, so pass it through untouched.
		return response
	}

	var projected map[string]json.RawMessage
	if envelope.Data != nil {
		projected = map[string]json.RawMessage{}
		if value, ok := envelope.Data[field]; ok {
			projected[field] = value
		}
	}

	out, err := json.Marshal(struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors json.RawMessage            `json:"errors,omitempty"`
	}{Data: projected, Errors: envelope.Errors})
	if err != nil {
		t.Fatalf("re-marshalling projected fixture: %v", err)
	}

	return string(out)
}

func newGraphQLStub(t *testing.T, response string) *graphqlStub {
	t.Helper()

	stub := &graphqlStub{response: response}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading stub request body: %v", err)
			return
		}

		var envelope struct {
			Query         string          `json:"query"`
			OperationName string          `json:"operationName"`
			Variables     json.RawMessage `json:"variables"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Errorf("unmarshalling stub request body %q: %v", body, err)
			return
		}
		stub.mutex.Lock()
		stub.queries = append(stub.queries, envelope.Query)
		stub.operationNames = append(stub.operationNames, envelope.OperationName)
		stub.mutex.Unlock()

		response, overridden := stub.overrides[envelope.OperationName]
		if !overridden {
			field, known := operationRootFields[envelope.OperationName]
			if !known {
				// Refuse to improvise: serving an empty response here would
				// let a collector missing from the map pass every test on
				// all-zero data.
				t.Errorf("stub received unknown operation %q; add it to operationRootFields", envelope.OperationName)
			}
			response = projectFixture(t, stub.response, field)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(stub.server.Close)

	return stub
}

// collector builds a real exporter over every collector, wired to the stub,
// with the default (strict) failure policy.
func (s *graphqlStub) collector(t *testing.T) prometheus.Collector {
	t.Helper()

	return s.collectorWithPartialScrapes(t, false)
}

// partialCollector is collector with --partial-scrapes enabled.
func (s *graphqlStub) partialCollector(t *testing.T) prometheus.Collector {
	t.Helper()

	return s.collectorWithPartialScrapes(t, true)
}

func (s *graphqlStub) collectorWithPartialScrapes(t *testing.T, partialScrapes bool) prometheus.Collector {
	t.Helper()

	ctx := logging.Init(context.Background(), true)
	exporter, err := newExporter(
		ctx, s.server.Client(), &fakeSession{endpoint: s.server.URL}, 5*time.Second, newCollectors(nil), partialScrapes)
	if err != nil {
		t.Fatalf("newExporter: %v", err)
	}

	return exporter
}

var descFQName = regexp.MustCompile(`fqName: "([^"]+)"`)

// fqName pulls the fully-qualified metric name out of a Desc's String()
// representation, which is the only exported way to inspect it.
func fqName(t *testing.T, desc string) string {
	t.Helper()

	match := descFQName.FindStringSubmatch(desc)
	if match == nil {
		t.Fatalf("could not extract fqName from descriptor %q", desc)
	}

	return match[1]
}

// gather registers the collector on a pedantic registry (which validates
// descriptors and label consistency) and returns the exposition-format output.
func gather(t *testing.T, collector prometheus.Collector) string {
	t.Helper()

	registry := prometheus.NewPedanticRegistry()
	if err := registry.Register(collector); err != nil {
		t.Fatalf("registering collector: %v", err)
	}

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gathering metrics: %v", err)
	}

	var out strings.Builder
	encoder := expfmt.NewEncoder(&out, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, family := range families {
		if err := encoder.Encode(family); err != nil {
			t.Fatalf("encoding metric family %q: %v", family.GetName(), err)
		}
	}

	return sanitize(out.String())
}

var (
	// The Go toolchain version leaks into spacelift_build_info, so the
	// golden files would churn on every Go upgrade.
	goversionLabel = regexp.MustCompile(`goversion="[^"]*"`)

	// Durations are wall-clock and differ on every run.
	scrapeDurationValue    = regexp.MustCompile(`(?m)^(spacelift_scrape_duration_seconds) .*$`)
	collectorDurationValue = regexp.MustCompile(`(?m)^(spacelift_scrape_collector_duration_seconds\{[^}]*\}) .*$`)
)

// sanitize replaces the two values that legitimately vary between runs, so a
// golden diff only ever reflects a real change to the metric surface.
func sanitize(in string) string {
	out := goversionLabel.ReplaceAllString(in, `goversion="<goversion>"`)
	out = scrapeDurationValue.ReplaceAllString(out, `$1 <duration>`)
	out = collectorDurationValue.ReplaceAllString(out, `$1 <duration>`)

	return out
}

// assertGolden compares got against testdata/golden/<name>.prom, or rewrites
// that file when -update-golden is set.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()

	path := filepath.Join("testdata", "golden", name+".prom")

	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating golden dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("writing golden file %s: %v", path, err)
		}
		t.Logf("wrote %s", path)

		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden file %s (re-run with -update-golden to create it): %v", path, err)
	}

	if got != string(want) {
		t.Errorf("metric output does not match %s\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}

// fixture reads a canned GraphQL response from testdata/fixtures.
func fixture(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join("testdata", "fixtures", name+".json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture %s: %v", path, err)
	}

	return string(body)
}
