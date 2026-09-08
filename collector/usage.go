package collector

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spacelift-io/prometheus-exporter/client"
)

// usage collects the account's current billing period usage.
type usage struct {
	periodStart        *prometheus.Desc
	periodEnd          *prometheus.Desc
	usedPrivateSeconds *prometheus.Desc
	usedPublicSeconds  *prometheus.Desc
	usedSeats          *prometheus.Desc
}

// NewUsage returns the billing usage collector.
func NewUsage() Collector {
	return &usage{
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

func (c *usage) Name() string { return "usage" }

func (c *usage) Describe(ch chan<- *prometheus.Desc) {
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

func (c *usage) Collect(ctx context.Context, api client.Client) ([]prometheus.Metric, error) {
	var query usageQuery
	if err := api.QueryNamed(ctx, &query, nil, "Usage"); err != nil {
		return nil, err
	}

	current := query.Usage

	return []prometheus.Metric{
		prometheus.MustNewConstMetric(c.periodStart, prometheus.GaugeValue, float64(current.BillingPeriodStart)),
		prometheus.MustNewConstMetric(c.periodEnd, prometheus.GaugeValue, float64(current.BillingPeriodEnd)),
		prometheus.MustNewConstMetric(c.usedPrivateSeconds, prometheus.GaugeValue, float64(current.UsedPrivateMinutes*60)),
		prometheus.MustNewConstMetric(c.usedPublicSeconds, prometheus.GaugeValue, float64(current.UsedPublicMinutes*60)),
		prometheus.MustNewConstMetric(c.usedSeats, prometheus.GaugeValue, float64(current.UsedSeats)),
	}, nil
}
