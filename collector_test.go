package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/spacelift-io/prometheus-exporter/client/session"
	"github.com/spacelift-io/prometheus-exporter/logging"
)

func TestMetricLabels(t *testing.T) {
	// Create a session with dummy credentials
	ctx := context.Background()

	// Import the logging package init
	// For testing, we'll use a development logger
	ctx = logging.Init(ctx, true)

	mockSession, err := session.New(ctx, http.DefaultClient, "https://test.app.spacelift.io", "dummy-key", "dummy-secret")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	collector, err := newSpaceliftCollector(ctx, http.DefaultClient, mockSession, 5)
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}

	// Get the collector as a Prometheus collector
	c := collector.(*spaceliftCollector)

	// Verify currentStacksCountByState has 3 variable labels
	desc := c.currentStacksCountByState
	if desc == nil {
		t.Fatal("currentStacksCountByState descriptor is nil")
	}

	// Create a test metric to verify label count
	// The descriptor should expect exactly 3 labels: state, stack, space
	testMetric := prometheus.MustNewConstMetric(
		desc,
		prometheus.GaugeValue,
		1.0,
		"active",    // state label
		"my-stack",  // stack label
		"my-space",  // space label
	)

	if testMetric == nil {
		t.Fatal("Failed to create test metric with 3 labels")
	}

	// Verify currentResourcesCountByDrift has 3 variable labels
	descDrift := c.currentResourcesCountByDrift
	if descDrift == nil {
		t.Fatal("currentResourcesCountByDrift descriptor is nil")
	}

	testMetricDrift := prometheus.MustNewConstMetric(
		descDrift,
		prometheus.GaugeValue,
		5.0,
		"drifted",   // state label
		"my-stack",  // stack label
		"my-space",  // space label
	)

	if testMetricDrift == nil {
		t.Fatal("Failed to create drift test metric with 3 labels")
	}

	// Test with wrong number of labels (should panic)
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("Creating metric with 2 labels should panic, but it didn't")
		}
	}()

	// This should panic because we're only providing 2 labels when 3 are expected
	_ = prometheus.MustNewConstMetric(
		desc,
		prometheus.GaugeValue,
		1.0,
		"active",   // state label
		"my-stack", // stack label (missing space label)
	)
}

