// Package metrics defines every Prometheus collector (SPEC §11). Labels never
// carry user identity or SQL.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Warewave-Technology/athens/internal/version"
)

var ns = version.MetricsNamespace

var (
	HTTPRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "http_requests_total", Help: "HTTP requests by route pattern, method and status code.",
	}, []string{"route", "method", "code"})
	HTTPDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: ns, Name: "http_request_duration_seconds", Help: "HTTP request latency by route pattern.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route"})
	Queries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "queries_total", Help: "SQL editor executions by target, class and result.",
	}, []string{"target", "class", "result"})
	QueryDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: ns, Name: "query_duration_seconds", Help: "SQL editor execution latency by class.",
		Buckets: []float64{.005, .01, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60},
	}, []string{"class"})
	DiscoveryTargets = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Name: "discovery_targets", Help: "Discovered targets by namespace and source.",
	}, []string{"namespace", "source"})
	DiscoveryErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "discovery_errors_total", Help: "Discovery problems by source and reason.",
	}, []string{"source", "reason"})
	PoolConns = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Name: "pool_conns", Help: "Pool connections by target and state (acquired, idle).",
	}, []string{"target", "state"})
	PoolCreate = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "pool_create_total", Help: "Pool creations by target and result.",
	}, []string{"target", "result"})
	AuthLogins = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "auth_logins_total", Help: "Login attempts by result.",
	}, []string{"result"})
	AuditWriteErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: ns, Name: "audit_write_errors_total", Help: "Audit records that could not be written to the store.",
	})
)

// Registry holds all athens collectors plus Go/process collectors.
var Registry = prometheus.NewRegistry()

func init() {
	Registry.MustRegister(
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		HTTPRequests, HTTPDuration, Queries, QueryDuration, DiscoveryTargets, DiscoveryErrors,
		PoolConns, PoolCreate, AuthLogins, AuditWriteErrors,
	)
}

// Handler serves the metrics endpoint.
func Handler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{})
}
