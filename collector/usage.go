package collector

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// secondsPerMinute converts the API's minute-denominated usage into the seconds
// Prometheus expects as a base unit. The existing metric names already say
// _seconds, so this is not new behaviour.
const secondsPerMinute = 60

// Usage collects billing period usage.
//
// Current Spacelift SaaS allows machine sessions but requires read access to
// the root space. Self-Hosted releases older than August 2026 also reject
// machine sessions. This makes usage the most restricted collector here, so it
// gets its own document.
type Usage struct {
	periodStart        *prometheus.Desc
	periodEnd          *prometheus.Desc
	usedPrivateSeconds *prometheus.Desc
	usedPublicSeconds  *prometheus.Desc
	usedSeats          *prometheus.Desc
}

// NewUsage returns the billing usage collector.
func NewUsage() Collector {
	return &Usage{
		periodStart: prometheus.NewDesc(
			"spacelift_current_billing_period_start_timestamp_seconds",
			"The timestamp of the start of the current billing period",
			nil,
			nil),
		periodEnd: prometheus.NewDesc(
			"spacelift_current_billing_period_end_timestamp_seconds",
			"The timestamp of the end of the current billing period",
			nil,
			nil),
		usedPrivateSeconds: prometheus.NewDesc(
			"spacelift_current_billing_period_used_private_seconds",
			"The amount of private worker usage in the current billing period",
			nil,
			nil),
		usedPublicSeconds: prometheus.NewDesc(
			"spacelift_current_billing_period_used_public_seconds",
			"The amount of public worker usage in the current billing period",
			nil,
			nil),
		usedSeats: prometheus.NewDesc(
			"spacelift_current_billing_period_used_seats",
			"The number of seats used in the current billing period",
			nil,
			nil),
	}
}

// Name implements Collector.
func (c *Usage) Name() string { return "usage" }

// Describe implements Collector.
func (c *Usage) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.periodStart
	ch <- c.periodEnd
	ch <- c.usedPrivateSeconds
	ch <- c.usedPublicSeconds
	ch <- c.usedSeats
}

type usageQuery struct {
	Usage struct {
		BillingPeriodStart int `graphql:"billingPeriodStart"`
		BillingPeriodEnd   int `graphql:"billingPeriodEnd"`
		UsedPrivateMinutes int `graphql:"usedPrivateMinutes"`
		UsedPublicMinutes  int `graphql:"usedPublicMinutes"`
		UsedSeats          int `graphql:"usedSeats"`
	} `graphql:"usage"`
}

// Collect implements Collector.
func (c *Usage) Collect(ctx context.Context, api client.NamedClient) ([]prometheus.Metric, error) {
	var query usageQuery
	if err := api.QueryNamed(ctx, &query, nil, "Usage"); err != nil {
		return nil, classify(err, c.Name())
	}

	usage := query.Usage

	return []prometheus.Metric{
		prometheus.MustNewConstMetric(
			c.periodStart, prometheus.GaugeValue, float64(usage.BillingPeriodStart)),
		prometheus.MustNewConstMetric(
			c.periodEnd, prometheus.GaugeValue, float64(usage.BillingPeriodEnd)),
		prometheus.MustNewConstMetric(
			c.usedPrivateSeconds, prometheus.GaugeValue, float64(usage.UsedPrivateMinutes*secondsPerMinute)),
		prometheus.MustNewConstMetric(
			c.usedPublicSeconds, prometheus.GaugeValue, float64(usage.UsedPublicMinutes*secondsPerMinute)),
		prometheus.MustNewConstMetric(
			c.usedSeats, prometheus.GaugeValue, float64(usage.UsedSeats)),
	}, nil
}
