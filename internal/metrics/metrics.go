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

	// NamespaceResolution counts project→namespace lookups: cache hit/miss/db
	// error, per region.
	NamespaceResolution = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "namespace_resolution_total",
		Help:      "Namespace resolution outcomes (hit, miss, error), per region.",
	}, []string{"region", "result"})

	// NamespaceResolutionDuration observes lookup latency (cache + DB), per
	// region.
	NamespaceResolutionDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "namespace_resolution_duration_seconds",
		Help:      "Namespace resolution latency, per region.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"region"})

	// AuthRejects counts requests rejected by the enforcer or service-key
	// middleware, per reason.
	AuthRejects = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "auth_rejects_total",
		Help:      "Requests rejected by auth middleware (enforcer, service_key), per reason.",
	}, []string{"reason"})

	// ProvisioningOps counts provisioning fan-out outcomes: ready (success),
	// failed (upstream error, stays provisioning), rejected (invalid payload).
	ProvisioningOps = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "provisioning_ops_total",
		Help:      "Provisioning fan-out outcomes (ready, failed, rejected), per region.",
	}, []string{"region", "outcome"})
)
