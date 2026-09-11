// Package metrics defines the Prometheus series exported on /metrics.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const namespace = "core_gateway"

var (
	RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "requests_total",
		Help:      "Requests handled by the gateway, per region/method/status.",
	}, []string{"region", "method", "code"})

	RequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "request_duration_seconds",
		Help:      "End-to-end request latency, per region/method.",
		Buckets:   append(prometheus.DefBuckets, 30, 60, 300, 600),
	}, []string{"region", "method"})

	InflightRequests = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "inflight_requests",
		Help:      "Requests currently in flight, per region.",
	}, []string{"region"})

	UpstreamErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "upstream_errors_total",
		Help:      "Requests that failed before reaching the regional apiserver.",
	}, []string{"region"})

	RegionHealthy = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "region_healthy",
		Help:      "Last probed health of a regional apiserver (1 healthy, 0 not).",
	}, []string{"name"})
)
