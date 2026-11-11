package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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

	// mutex guards queries, operationNames and variables: collectors query
	// concurrently. response and overrides are only read by the handler, so
	// set them before the first scrape.
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

	// variables holds the compacted "variables" JSON of every request, in
	// arrival order alongside queries, and "" for a request that sent none.
	variables []string
}

// failOperation makes the named operation return the given body while every
// other collector keeps receiving the fixture. Without one document per
// collector there would be no way to fail one and not the others. The body is
// served as it is, so this is also the way to serve a malformed response.
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

// recordedVariables returns the variables every request sent, grouped by
// envelope operation name, each list in arrival order.
func (s *graphqlStub) recordedVariables() map[string][]string {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	out := map[string][]string{}
	for i, operation := range s.operationNames {
		out[operation] = append(out[operation], s.variables[i])
	}

	return out
}

// operationOf extracts the operation name from a query document. The name ends
// at the variable declarations, if any, or at the selection set.
func operationOf(query string) string {
	name, _, _ := strings.Cut(strings.TrimPrefix(query, "query "), "{")
	name, _, _ = strings.Cut(name, "(")

	return strings.TrimSpace(name)
}

// selection is one field of a parsed GraphQL selection set. key is the name
// the field takes in the response, which is its alias when it has one.
// arguments is the raw text between the field's parentheses, if any.
type selection struct {
	key       string
	field     string
	arguments string
	children  []selection
}

// parseSelections parses the selection set of a minified query document, as
// the GraphQL client renders it: fields, aliases, argument lists, kept as raw
// text, and nested selection sets. It is not a GraphQL parser; it reads only
// the documents this exporter sends.
func parseSelections(query string) ([]selection, error) {
	start := strings.Index(query, "{")
	if start < 0 {
		return nil, fmt.Errorf("query has no selection set: %q", query)
	}

	selections, rest, err := parseSelectionSet(query[start+1:])
	if err != nil {
		return nil, fmt.Errorf("parsing %q: %w", query, err)
	}
	if strings.TrimSpace(rest) != "" {
		return nil, fmt.Errorf("trailing input %q after the selection set of %q", rest, query)
	}

	return selections, nil
}

// parseSelectionSet parses fields up to the closing brace of the current set
// and returns them with the input that follows that brace.
func parseSelectionSet(in string) ([]selection, string, error) {
	var out []selection
	for {
		in = strings.TrimLeft(in, " ,\n\t")
		if in == "" {
			return nil, "", errors.New("unterminated selection set")
		}
		if in[0] == '}' {
			return out, in[1:], nil
		}

		end := strings.IndexAny(in, " ,:({}")
		if end <= 0 {
			return nil, "", fmt.Errorf("expected a field name at %q", in)
		}
		sel := selection{key: in[:end], field: in[:end]}
		in = strings.TrimLeft(in[end:], " ")

		if in != "" && in[0] == ':' {
			in = strings.TrimLeft(in[1:], " ")
			end = strings.IndexAny(in, " ,({}")
			if end <= 0 {
				return nil, "", fmt.Errorf("expected a field name after the alias %q", sel.key)
			}
			sel.field = in[:end]
			in = strings.TrimLeft(in[end:], " ")
		}

		if in != "" && in[0] == '(' {
			end = strings.Index(in, ")")
			if end < 0 {
				return nil, "", fmt.Errorf("unterminated argument list for %q", sel.field)
			}
			sel.arguments = in[1:end]
			in = strings.TrimLeft(in[end+1:], " ")
		}

		if in != "" && in[0] == '{' {
			var err error
			if sel.children, in, err = parseSelectionSet(in[1:]); err != nil {
				return nil, "", err
			}
		}

		out = append(out, sel)
	}
}

// reporter is the part of testing.T the stub reports through. The stub runs on
// the server's goroutine, where only non-fatal reporting is allowed.
type reporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// projectFixture narrows a whole-account fixture to exactly what a document
// selected.
//
// Fixtures describe an account, not a query, which keeps them readable and
// lets one file cover every collector. The GraphQL decoder rejects response
// fields the query did not select, so the stub narrows the fixture to the
// selection, including for collectors that select the same root field with
// different shapes. A selected field missing from an object the fixture holds
// fails the test: serving it as absent would let a collector pass on zero
// values the fixture never meant to provide. So does an object selected
// without subfields. Nothing is checked beneath a null or inside an empty
// list, so a selected path is only checked by a fixture that holds a non-null,
// non-empty value along it.
//
// An alias is looked up in the fixture first, then the field it renames, so a
// fixture can give each alias its own data. Two selections of one field with
// different arguments that are both served from the field name fail the test,
// since they would be served the same data. That includes a selection without
// an alias, which is always served from the field name.
//
// Errors and extensions are passed through unchanged: a fixture that carries
// an error fails every document, which is what
// TestCollectorSurfacesQueryErrors relies on. A fixture that is not a GraphQL
// envelope fails the test: malformed JSON, a top-level key other than data,
// errors and extensions, or neither data nor errors. failOperation serves a
// malformed body when a test needs one.
func projectFixture(t reporter, response string, query string) string {
	t.Helper()

	var keys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(response), &keys); err != nil {
		t.Errorf("fixture is not a GraphQL envelope: %v", err)
		return response
	}

	var envelope struct {
		Data       map[string]json.RawMessage `json:"data"`
		Errors     json.RawMessage            `json:"errors,omitempty"`
		Extensions json.RawMessage            `json:"extensions,omitempty"`
	}
	decoder := json.NewDecoder(strings.NewReader(response))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		t.Errorf("fixture is not a GraphQL envelope: %v", err)
		return response
	}

	_, hasData := keys["data"]
	_, hasErrors := keys["errors"]
	if !hasData && !hasErrors {
		t.Errorf("fixture is not a GraphQL envelope: it holds neither data nor errors")
		return response
	}

	var projected json.RawMessage
	if envelope.Data != nil {
		data, err := json.Marshal(envelope.Data)
		if err != nil {
			t.Errorf("re-marshalling fixture data: %v", err)
			return response
		}
		selections, err := parseSelections(query)
		if err != nil {
			t.Errorf("stub: %v", err)
			return response
		}
		projected = projectValue(t, data, selections, operationOf(query))
	}

	out, err := json.Marshal(struct {
		Data       json.RawMessage `json:"data"`
		Errors     json.RawMessage `json:"errors,omitempty"`
		Extensions json.RawMessage `json:"extensions,omitempty"`
	}{Data: projected, Errors: envelope.Errors, Extensions: envelope.Extensions})
	if err != nil {
		t.Errorf("re-marshalling projected fixture: %v", err)
		return response
	}

	return string(out)
}

// projectValue applies selections to one fixture value. Lists are projected
// element by element, and null is a leaf whatever was selected under it.
func projectValue(t reporter, value json.RawMessage, selections []selection, path string) json.RawMessage {
	t.Helper()

	trimmed := strings.TrimSpace(string(value))
	switch {
	case trimmed == "null":
		return value

	case len(selections) == 0:
		if holdsObject(value) {
			t.Errorf("fixture %s is an object but the document selects it without subfields", path)
		}

		return value

	case strings.HasPrefix(trimmed, "["):
		var items []json.RawMessage
		if err := json.Unmarshal(value, &items); err != nil {
			t.Errorf("fixture %s: %v", path, err)
			return value
		}
		for i := range items {
			items[i] = projectValue(t, items[i], selections, path)
		}

		return mustMarshal(t, items)

	default:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(value, &object); err != nil {
			t.Errorf("fixture %s: selection set on a non-object: %v", path, err)
			return value
		}

		out := make(map[string]json.RawMessage, len(selections))

		// byField holds every selection served from its field name rather
		// than from its own alias, unaliased ones included.
		var byField []selection
		for _, sel := range selections {
			field, aliasHit := object[sel.key]
			if !aliasHit || sel.key == sel.field {
				var ok bool
				if field, ok = object[sel.field]; !ok {
					t.Errorf("fixture lacks %s.%s, which the document selects", path, sel.field)
					continue
				}
				for _, other := range byField {
					if other.field == sel.field && other.arguments != sel.arguments {
						t.Errorf("fixture %s.%s would serve %s and %s the same data, but they pass different arguments; key the fixture by alias",
							path, sel.field, other.key, sel.key)
					}
				}
				byField = append(byField, sel)
			}
			out[sel.key] = projectValue(t, field, sel.children, path+"."+sel.field)
		}

		return mustMarshal(t, out)
	}
}

// holdsObject reports whether a fixture value is an object or a list holding
// one, which a document must select with subfields.
func holdsObject(value json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(value))
	if strings.HasPrefix(trimmed, "{") {
		return true
	}

	var items []json.RawMessage
	if !strings.HasPrefix(trimmed, "[") || json.Unmarshal(value, &items) != nil {
		return false
	}

	return slices.ContainsFunc(items, holdsObject)
}

func mustMarshal(t reporter, value any) json.RawMessage {
	t.Helper()

	out, err := json.Marshal(value)
	if err != nil {
		t.Errorf("marshalling projected fixture: %v", err)
	}

	return out
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
		var variables bytes.Buffer
		if len(envelope.Variables) > 0 && string(envelope.Variables) != "null" {
			if err := json.Compact(&variables, envelope.Variables); err != nil {
				t.Errorf("compacting stub request variables %s: %v", envelope.Variables, err)
			}
		}
		stub.mutex.Lock()
		stub.queries = append(stub.queries, envelope.Query)
		stub.operationNames = append(stub.operationNames, envelope.OperationName)
		stub.variables = append(stub.variables, variables.String())
		stub.mutex.Unlock()

		response, overridden := stub.overrides[envelope.OperationName]
		if !overridden {
			if name := operationOf(envelope.Query); name != envelope.OperationName {
				t.Errorf("document is named %q but the envelope operationName is %q", name, envelope.OperationName)
			}
			response = projectFixture(t, stub.response, envelope.Query)
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

	return s.exporter(t, nil, partialScrapes)
}

func (s *graphqlStub) exporter(t *testing.T, enabled map[string]bool, partialScrapes bool) prometheus.Collector {
	t.Helper()

	ctx := logging.Init(context.Background(), true)
	exporter, err := newExporter(
		ctx, s.server.Client(), &fakeSession{endpoint: s.server.URL}, 5*time.Second, newCollectors(enabled), partialScrapes)
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

func TestParseSelections(t *testing.T) {
	got, err := parseSelections(`query Op($input:SearchInput!){pools:workerPools{id,search(input: $input, first: 2){edges{node{id}}},n},usage{seats}}`)
	if err != nil {
		t.Fatalf("parseSelections: %v", err)
	}

	want := []selection{
		{key: "pools", field: "workerPools", children: []selection{
			{key: "id", field: "id"},
			{key: "search", field: "search", arguments: "input: $input, first: 2", children: []selection{
				{key: "edges", field: "edges", children: []selection{
					{key: "node", field: "node", children: []selection{{key: "id", field: "id"}}},
				}},
			}},
			{key: "n", field: "n"},
		}},
		{key: "usage", field: "usage", children: []selection{{key: "seats", field: "seats"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseSelections =\n%+v\nwant\n%+v", got, want)
	}

	for _, bad := range []string{`query Op`, `query Op{a{b}`, `query Op{a(x:1{b}}`} {
		if _, err := parseSelections(bad); err == nil {
			t.Errorf("parseSelections(%q) succeeded, want an error", bad)
		}
	}
}

// errorRecorder is a reporter that keeps errors instead of failing the test.
type errorRecorder struct{ errors []string }

func (r *errorRecorder) Helper() {}

func (r *errorRecorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func TestProjectFixture(t *testing.T) {
	const fixture = `{"data":{"pools":[{"id":"a","name":"x","workers":[{"busy":true,"id":"w"}]},{"id":"b","name":"y","workers":null}],"other":1}}`

	recorder := &errorRecorder{}
	got := projectFixture(recorder, fixture, `query Op{pools{id,workers{busy}}}`)
	if len(recorder.errors) > 0 {
		t.Fatalf("unexpected errors: %v", recorder.errors)
	}
	if want := `{"data":{"pools":[{"id":"a","workers":[{"busy":true}]},{"id":"b","workers":null}]}}`; got != want {
		t.Errorf("projection = %s, want %s", got, want)
	}

	recorder = &errorRecorder{}
	projectFixture(recorder, fixture, `query Op{pools{id,workers{drained}}}`)
	if len(recorder.errors) != 1 || !strings.Contains(recorder.errors[0], "Op.pools.workers.drained") {
		t.Errorf("a selected field missing from the fixture reported %v, want one error naming Op.pools.workers.drained", recorder.errors)
	}

	// workers holds objects, so selecting it as a leaf is not a valid
	// document. Pool b's null workers is a leaf and reports nothing.
	recorder = &errorRecorder{}
	projectFixture(recorder, fixture, `query Op{pools{workers}}`)
	if len(recorder.errors) != 1 || !strings.Contains(recorder.errors[0], "Op.pools.workers") {
		t.Errorf("an object selected without subfields reported %v, want one error naming Op.pools.workers", recorder.errors)
	}

	// Aliases of one field with the same arguments share the field's data.
	recorder = &errorRecorder{}
	got = projectFixture(recorder, fixture, `query Op{a:pools{id},b:pools{name}}`)
	if len(recorder.errors) > 0 {
		t.Fatalf("unexpected errors: %v", recorder.errors)
	}
	if want := `{"data":{"a":[{"id":"a"},{"id":"b"}],"b":[{"name":"x"},{"name":"y"}]}}`; got != want {
		t.Errorf("aliased projection = %s, want %s", got, want)
	}

	// A fixture keyed by alias gives each alias its own data.
	const byAlias = `{"data":{"ready":{"n":1},"running":{"n":2}}}`
	const twoStates = `query Op{ready:runs(state:READY){n},running:runs(state:RUNNING){n}}`
	recorder = &errorRecorder{}
	got = projectFixture(recorder, byAlias, twoStates)
	if len(recorder.errors) > 0 {
		t.Fatalf("unexpected errors: %v", recorder.errors)
	}
	if got != byAlias {
		t.Errorf("per-alias projection = %s, want %s", got, byAlias)
	}

	// Keyed by field, both aliases would get the same data although their
	// arguments differ.
	recorder = &errorRecorder{}
	projectFixture(recorder, `{"data":{"runs":{"n":1}}}`, twoStates)
	if len(recorder.errors) != 1 || !strings.Contains(recorder.errors[0], "Op.runs") {
		t.Errorf("aliases with different arguments served from one field reported %v, want one error naming Op.runs", recorder.errors)
	}

	// The same holds when one of the two keeps the plain field name, in
	// either order.
	for _, query := range []string{
		`query Op{runs(state:READY){n},running:runs(state:RUNNING){n}}`,
		`query Op{running:runs(state:RUNNING){n},runs(state:READY){n}}`,
	} {
		recorder = &errorRecorder{}
		projectFixture(recorder, `{"data":{"runs":{"n":1}}}`, query)
		if len(recorder.errors) != 1 || !strings.Contains(recorder.errors[0], "Op.runs") {
			t.Errorf("%s served from one field reported %v, want one error naming Op.runs", query, recorder.errors)
		}
	}

	// A fixture that is not an envelope is a broken test, not a response
	// under test; failOperation is how a test serves a malformed body.
	for _, broken := range []string{
		`{"data":{"pools":[]},}`,
		`{}`,
		`{"dat":{}}`,
		`{"data":{},"error":[]}`,
	} {
		recorder = &errorRecorder{}
		projectFixture(recorder, broken, `query Op{pools{id}}`)
		if len(recorder.errors) != 1 || !strings.Contains(recorder.errors[0], "not a GraphQL envelope") {
			t.Errorf("fixture %s reported %v, want one error saying it is not a GraphQL envelope", broken, recorder.errors)
		}
	}
}

// TestGraphQLStubRecordsVariables checks that the stub keeps the variables of
// every request, compacted and grouped by operation, so a later request does
// not overwrite an earlier one.
func TestGraphQLStubRecordsVariables(t *testing.T) {
	stub := newGraphQLStub(t, `{"data":{"pools":[{"id":"a"}]}}`)

	for _, variables := range []string{`{"a": 1}`, `{ "a" : 2 }`} {
		body := `{"query":"query Op{pools{id}}","operationName":"Op","variables":` + variables + `}`
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, stub.server.URL, strings.NewReader(body))
		if err != nil {
			t.Fatalf("building request: %v", err)
		}
		request.Header.Set("Content-Type", "application/json")

		response, err := stub.server.Client().Do(request)
		if err != nil {
			t.Fatalf("posting to the stub: %v", err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}

	got := stub.recordedVariables()
	want := map[string][]string{"Op": {`{"a":1}`, `{"a":2}`}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("recordedVariables() = %v, want %v", got, want)
	}
}
