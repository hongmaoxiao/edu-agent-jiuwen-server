package gateway

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus 指标，做法同 deepfate-server ADR-0024：标签只用路由模板、状态码、稳定错误码和结果，
// 不放账号、学校、IP、路径参数或请求内容。按学校、学生看的用量查 PostgreSQL，不查这里。
var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "edu_http_requests_total",
		Help: "HTTP requests by route pattern (unmatched, rate_limited), status and stable error code (empty on success).",
	}, []string{"route", "status", "error"})
	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "edu_http_request_duration_seconds",
		Help:    "Latency of non-streaming HTTP requests by route pattern.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route"})
	httpInFlight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "edu_http_requests_in_flight",
		Help: "HTTP requests being served.",
	})
	logins = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "edu_logins_total",
		Help: "Sign-in attempts by result: success, invalid, rate_limited.",
	}, []string{"result"})
)

func init() {
	prometheus.MustRegister(collectors.NewBuildInfoCollector())
}

// markError records the stable error code of this response for the request counter.
func markError(w http.ResponseWriter, code string) {
	if record, ok := w.(*responseWriter); ok {
		record.errorCode = code
	}
}

// RegisterDBMetrics exposes pool state: waiting for a connection is the first sign the database is saturated.
func RegisterDBMetrics(db *pgxpool.Pool) {
	gauge := func(name, help string, value func(*pgxpool.Stat) float64) {
		promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: "edu_db_pool_" + name, Help: help}, func() float64 { return value(db.Stat()) })
	}
	counter := func(name, help string, value func(*pgxpool.Stat) float64) {
		promauto.NewCounterFunc(prometheus.CounterOpts{Name: "edu_db_pool_" + name, Help: help}, func() float64 { return value(db.Stat()) })
	}
	gauge("acquired_connections", "Connections in use.", func(s *pgxpool.Stat) float64 { return float64(s.AcquiredConns()) })
	gauge("idle_connections", "Idle connections.", func(s *pgxpool.Stat) float64 { return float64(s.IdleConns()) })
	gauge("max_connections", "Pool size limit.", func(s *pgxpool.Stat) float64 { return float64(s.MaxConns()) })
	counter("acquires_total", "Connection acquisitions.", func(s *pgxpool.Stat) float64 { return float64(s.AcquireCount()) })
	counter("empty_acquires_total", "Acquisitions that had to wait for a connection.", func(s *pgxpool.Stat) float64 { return float64(s.EmptyAcquireCount()) })
	counter("acquire_wait_seconds_total", "Time spent acquiring connections.", func(s *pgxpool.Stat) float64 { return s.AcquireDuration().Seconds() })
}
