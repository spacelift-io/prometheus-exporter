package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hasura/go-graphql-client"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// staticSession hands out a fixed token and swaps it for "fresh" when asked to
// refresh, so tests can observe both the retry and the token it carries.
type staticSession struct {
	endpoint     string
	token        string
	refreshCalls int
}

func (s *staticSession) BearerToken(context.Context) (string, error) { return s.token, nil }

func (s *staticSession) Endpoint() string { return s.endpoint }

func (s *staticSession) RefreshToken(context.Context) error {
	s.refreshCalls++
	s.token = "fresh"

	return nil
}

type recordedRequest struct {
	authorization string
	operationName string
	queryPrefix   string
}

// newRecordingServer stands in for the Spacelift GraphQL API. It records every
// request's Authorization header, envelope operationName and the start of the
// query document, answers "Bearer stale" with an unauthorized error and
// anything else with a viewer payload.
func newRecordingServer(t *testing.T) (*httptest.Server, *[]recordedRequest) {
	t.Helper()

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
		requests = append(requests, recordedRequest{
			authorization: authorization,
			operationName: envelope.OperationName,
			queryPrefix:   queryPrefix,
		})

		w.Header().Set("Content-Type", "application/json")
		if authorization == "Bearer stale" {
			_, _ = fmt.Fprint(w, `{"errors":[{"message":"unauthorized"}]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":{"viewer":{"id":"viewer"}}}`)
	}))
	t.Cleanup(server.Close)

	return server, &requests
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
	server, requests := newRecordingServer(t)
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
	if !reflect.DeepEqual(*requests, want) {
		t.Fatalf("requests = %#v, want %#v", *requests, want)
	}
	if session.refreshCalls != 1 {
		t.Errorf("RefreshToken() calls = %d, want 1", session.refreshCalls)
	}
	if query.Viewer.ID != "viewer" {
		t.Errorf("decoded viewer ID = %q, want viewer", query.Viewer.ID)
	}
}

// TestQueryKeepsTheUnsuffixedOperationName pins that the original Query method
// still sends the bare exporter-wide name, so existing attribution in
// Spacelift's APM does not change for callers that have not adopted QueryNamed.
func TestQueryKeepsTheUnsuffixedOperationName(t *testing.T) {
	server, requests := newRecordingServer(t)
	session := &staticSession{endpoint: server.URL, token: "valid"}

	var query viewerQuery
	if err := client.New(server.Client(), session).Query(context.Background(), &query, nil); err != nil {
		t.Fatalf("Query() error = %v", err)
	}

	want := []recordedRequest{
		{authorization: "Bearer valid", operationName: "PrometheusExporter", queryPrefix: "query PrometheusExporter"},
	}
	if !reflect.DeepEqual(*requests, want) {
		t.Fatalf("requests = %#v, want %#v", *requests, want)
	}
}
