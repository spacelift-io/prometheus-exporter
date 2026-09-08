package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hasura/go-graphql-client"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// staticSession hands out a fixed token and swaps it for "fresh" when asked to
// refresh, so tests can observe both the retry and the token it carries.
type staticSession struct {
	mutex        sync.Mutex
	endpoint     string
	token        string
	refreshCalls int
}

func (s *staticSession) BearerToken(context.Context) (string, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	return s.token, nil
}

func (s *staticSession) Endpoint() string { return s.endpoint }

func (s *staticSession) RefreshToken(context.Context) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.refreshCalls++
	s.token = "fresh"

	return nil
}

func (s *staticSession) refreshes() int {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	return s.refreshCalls
}

type recordedRequest struct {
	authorization string
	operationName string
	queryPrefix   string
}

// newRecordingServer stands in for the Spacelift GraphQL API. It records every
// request's Authorization header, envelope operationName and the start of the
// query document, answers "Bearer stale" with an unauthorized error (after
// calling onStale, if given, so a test can hold concurrent stale requests) and
// anything else with a viewer payload.
func newRecordingServer(t *testing.T, onStale func()) (*httptest.Server, func() []recordedRequest) {
	t.Helper()

	var mutex sync.Mutex
	var requests []recordedRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Query         string `json:"query"`
			OperationName string `json:"operationName"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Errorf("decoding request: %v", err)
			return
		}

		authorization := r.Header.Get("Authorization")
		queryPrefix, _, _ := strings.Cut(envelope.Query, "{")
		mutex.Lock()
		requests = append(requests, recordedRequest{
			authorization: authorization,
			operationName: envelope.OperationName,
			queryPrefix:   queryPrefix,
		})
		mutex.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if authorization == "Bearer stale" {
			if onStale != nil {
				onStale()
			}
			_, _ = fmt.Fprint(w, `{"errors":[{"message":"unauthorized"}]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":{"viewer":{"id":"viewer"}}}`)
	}))
	t.Cleanup(server.Close)

	recorded := func() []recordedRequest {
		mutex.Lock()
		defer mutex.Unlock()

		return append([]recordedRequest(nil), requests...)
	}

	return server, recorded
}

type viewerQuery struct {
	Viewer struct {
		ID graphql.ID
	}
}

// TestQueryNamedRetryUsesRefreshedTokenAndKeepsOperationName covers the retry
// path: an unauthorized response refreshes the session once and reissues the
// request with the new bearer token, and both attempts carry the operation
// name in the envelope and in the document, so neither reaches Spacelift as
// an anonymous query.
func TestQueryNamedRetryUsesRefreshedTokenAndKeepsOperationName(t *testing.T) {
	server, recorded := newRecordingServer(t, nil)
	session := &staticSession{endpoint: server.URL, token: "stale"}

	var query viewerQuery
	if err := client.New(server.Client(), session).QueryNamed(
		context.Background(), &query, nil, "WorkerPools",
	); err != nil {
		t.Fatalf("QueryNamed() error = %v", err)
	}

	want := []recordedRequest{
		{authorization: "Bearer stale", operationName: "PrometheusExporter_WorkerPools", queryPrefix: "query PrometheusExporter_WorkerPools"},
		{authorization: "Bearer fresh", operationName: "PrometheusExporter_WorkerPools", queryPrefix: "query PrometheusExporter_WorkerPools"},
	}
	if got := recorded(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %#v, want %#v", got, want)
	}
	if session.refreshes() != 1 {
		t.Errorf("RefreshToken() calls = %d, want 1", session.refreshes())
	}
	if query.Viewer.ID != "viewer" {
		t.Errorf("decoded viewer ID = %q, want viewer", query.Viewer.ID)
	}
}

// TestQueryKeepsTheUnsuffixedOperationName pins that the original Query method
// still sends the bare exporter-wide name, so existing attribution in
// Spacelift's APM does not change for callers that have not adopted QueryNamed.
func TestQueryKeepsTheUnsuffixedOperationName(t *testing.T) {
	server, recorded := newRecordingServer(t, nil)
	session := &staticSession{endpoint: server.URL, token: "valid"}

	var query viewerQuery
	if err := client.New(server.Client(), session).Query(context.Background(), &query, nil); err != nil {
		t.Fatalf("Query() error = %v", err)
	}

	want := []recordedRequest{
		{authorization: "Bearer valid", operationName: "PrometheusExporter", queryPrefix: "query PrometheusExporter"},
	}
	if got := recorded(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %#v, want %#v", got, want)
	}
}

// TestConcurrentUnauthorizedResponsesRefreshOnce is the interleaving concurrent
// collectors create: several requests go out with the same token, the server
// rejects them all, and every one of them wants a refresh. The session must be
// refreshed once, and every retry must carry the refreshed token.
func TestConcurrentUnauthorizedResponsesRefreshOnce(t *testing.T) {
	const queries = 4

	// Hold every stale request until all of them have reached the server, so
	// the unauthorized responses arrive together rather than one at a time.
	var arrived sync.WaitGroup
	arrived.Add(queries)
	server, recorded := newRecordingServer(t, func() {
		arrived.Done()
		arrived.Wait()
	})
	session := &staticSession{endpoint: server.URL, token: "stale"}
	api := client.New(server.Client(), session)

	errs := make(chan error, queries)
	for range queries {
		go func() {
			var query viewerQuery
			errs <- api.QueryNamed(context.Background(), &query, nil, "WorkerPools")
		}()
	}
	for range queries {
		select {
		case err := <-errs:
			if err != nil {
				t.Errorf("QueryNamed() error = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the concurrent queries")
		}
	}

	if session.refreshes() != 1 {
		t.Errorf("RefreshToken() calls = %d, want 1", session.refreshes())
	}

	stale, fresh := 0, 0
	for _, request := range recorded() {
		switch request.authorization {
		case "Bearer stale":
			stale++
		case "Bearer fresh":
			fresh++
		default:
			t.Errorf("unexpected Authorization header %q", request.authorization)
		}
	}
	if stale != queries || fresh != queries {
		t.Errorf("requests with stale token = %d, with fresh token = %d, want %d each", stale, fresh, queries)
	}
}
