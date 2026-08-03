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
	"strings"
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
// GraphQL encoding without standing in an API-key exchange.
type fakeSession struct {
	endpoint     string
	token        string
	refreshCalls int
}

func (s *fakeSession) BearerToken(context.Context) (string, error) {
	if s.token == "" {
		return "initial-token", nil
	}

	return s.token, nil
}

func (s *fakeSession) Endpoint() string { return s.endpoint }

func (s *fakeSession) RefreshToken(context.Context) error {
	s.refreshCalls++
	s.token = "refreshed-token"

	return nil
}

// graphqlStub is a stand-in for the Spacelift GraphQL API. It records every
// query body it receives and replies with a canned response, so tests can
// assert on the shape of the query as well as on the metrics produced from it.
type graphqlStub struct {
	server *httptest.Server

	// response is written verbatim as the HTTP body.
	response string

	// queries holds the raw "query" string of every request received.
	queries []string

	// operationNames and authorizationHeaders retain the envelope and request
	// metadata that are not present in the rendered query string.
	operationNames       []string
	authorizationHeaders []string
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
		stub.queries = append(stub.queries, envelope.Query)
		stub.operationNames = append(stub.operationNames, envelope.OperationName)
		stub.authorizationHeaders = append(stub.authorizationHeaders, r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, stub.response)
	}))
	t.Cleanup(stub.server.Close)

	return stub
}

// collector builds a real spaceliftCollector wired to the stub.
func (s *graphqlStub) collector(t *testing.T) prometheus.Collector {
	t.Helper()

	ctx := logging.Init(context.Background(), true)
	collector, err := newSpaceliftCollector(ctx, s.server.Client(), &fakeSession{endpoint: s.server.URL}, 5*time.Second)
	if err != nil {
		t.Fatalf("newSpaceliftCollector: %v", err)
	}

	return collector
}

// collectorWithSession builds a collector against an explicit session, so
// tests can observe token refreshes.
func collectorWithSession(t *testing.T, stub *graphqlStub, session *fakeSession) prometheus.Collector {
	t.Helper()

	ctx := logging.Init(context.Background(), true)
	collector, err := newSpaceliftCollector(ctx, stub.server.Client(), session, 5*time.Second)
	if err != nil {
		t.Fatalf("newSpaceliftCollector: %v", err)
	}

	return collector
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

// lastQuery returns the most recent GraphQL query body, with runs of
// whitespace collapsed so assertions can be written readably.
func (s *graphqlStub) lastQuery(t *testing.T) string {
	t.Helper()

	if len(s.queries) == 0 {
		t.Fatal("no GraphQL queries were recorded")
	}

	return regexp.MustCompile(`\s+`).ReplaceAllString(s.queries[len(s.queries)-1], " ")
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

	// Scrape duration is wall-clock and differs on every run.
	scrapeDurationValue = regexp.MustCompile(`(?m)^(spacelift_scrape_duration_seconds) .*$`)
)

// sanitize replaces the two values that legitimately vary between runs, so a
// golden diff only ever reflects a real change to the metric surface.
func sanitize(in string) string {
	out := goversionLabel.ReplaceAllString(in, `goversion="<goversion>"`)
	out = scrapeDurationValue.ReplaceAllString(out, `$1 <duration>`)

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
