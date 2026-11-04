// Package gateway is the HTTP layer: routing, middleware, error format and metrics. Business rules live in the domain packages.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"edu-agent-jiuwen-server/internal/account"
	"edu-agent-jiuwen-server/internal/platform"
	"edu-agent-jiuwen-server/migrations"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	config   platform.Config
	db       *pgxpool.Pool
	mux      *http.ServeMux
	patterns []string
	limits   limiter
	accounts *account.Service
}

func New(config platform.Config, db *pgxpool.Pool) *Server {
	s := &Server{config: config, db: db, mux: http.NewServeMux(), accounts: account.New(db)}
	s.route("GET /livez", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
	})
	s.route("GET /readyz", s.ready)
	s.registerAuth()
	s.registerSchools()
	return s
}

// Patterns lists every registered route; the contract test checks each one is in api/openapi.json.
func (s *Server) Patterns() []string { return slices.Clone(s.patterns) }

func (s *Server) route(pattern string, handler http.HandlerFunc) {
	s.patterns = append(s.patterns, pattern)
	s.mux.HandleFunc(pattern, handler)
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if s.db == nil || migrations.Ready(ctx, s.db) != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

// writeError writes { "error": { "code", "message" }, "request_id" }. code is stable and also a metric label; message is Chinese and shown as is.
func writeError(w http.ResponseWriter, status int, code, message string) {
	markError(w, code)
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}, "request_id": w.Header().Get("X-Request-ID")})
}

// fail maps domain errors to responses; anything unexpected is logged and reported as unavailable.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	message := func(fallback string) string {
		if _, detail, ok := strings.Cut(err.Error(), ": "); ok {
			return detail
		}
		return fallback
	}
	switch {
	case errors.Is(err, account.ErrRejected):
		writeError(w, 400, "INVALID_REQUEST", message("请求内容不正确"))
	case errors.Is(err, account.ErrInvalid):
		writeError(w, 401, "UNAUTHENTICATED", "登录已失效，请重新登录")
	case errors.Is(err, account.ErrForbidden):
		writeError(w, 403, "FORBIDDEN", "没有权限进行这个操作")
	case errors.Is(err, account.ErrNotFound):
		writeError(w, 404, "NOT_FOUND", "要找的内容不存在")
	case errors.Is(err, account.ErrConflict):
		writeError(w, 409, "CONFLICT", message("和已有数据冲突"))
	default:
		slog.Error("request failed", "service", "edu-agent-jiuwen-server", "route", r.Pattern, "error", err.Error())
		writeError(w, 503, "UNAVAILABLE", "服务暂时不可用，请稍后重试")
	}
}

// decode reads a small JSON body and rejects unknown fields, so a client typo fails loudly.
func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	reader := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	reader.DisallowUnknownFields()
	if err := reader.Decode(into); err != nil {
		writeError(w, 400, "INVALID_REQUEST", "请求内容不是有效的 JSON，或含有不认识的字段")
		return false
	}
	return true
}

type responseWriter struct {
	http.ResponseWriter
	status    int
	errorCode string
}

func (w *responseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	httpInFlight.Inc()
	defer httpInFlight.Dec()
	id := platform.ID("req")
	w.Header().Set("X-Request-ID", id)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	record := &responseWriter{ResponseWriter: w}
	route := s.serve(record, r)
	status := record.status
	if status == 0 {
		status = http.StatusOK
	}
	httpRequests.WithLabelValues(route, strconv.Itoa(status), record.errorCode).Inc()
	httpDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
	slog.Info("request", "service", "edu-agent-jiuwen-server", "request_id", id, "route", route,
		"status", status, "latency_ms", time.Since(start).Milliseconds())
}

// serve runs the checks every request passes, then the route; it returns the route label for metrics.
func (s *Server) serve(w *responseWriter, r *http.Request) string {
	if preflight := s.cors(w, r); preflight {
		w.WriteHeader(http.StatusNoContent)
		return "preflight"
	}
	if !s.limits.allow("ip:"+ipBlock(s.clientIP(r)), 300, time.Minute) {
		writeError(w, 429, "RATE_LIMITED", "请求太频繁，请稍后再试")
		return "rate_limited"
	}
	if version := r.Header.Get("X-Client-Version"); s.config.MinClientVersion != "" && platform.OlderThan(version, s.config.MinClientVersion) {
		writeError(w, 426, "CLIENT_TOO_OLD", "客户端版本过低，请升级后再使用")
		return "client_too_old"
	}
	s.mux.ServeHTTP(w, r)
	if r.Pattern == "" {
		return "unmatched"
	}
	return r.Pattern
}

// cors allows the configured web origins with cookies, and desktop loopback origins without them.
// It reports whether the request was a preflight that has been answered.
func (s *Server) cors(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
	if origin == "" {
		return preflight
	}
	header := w.Header()
	header.Add("Vary", "Origin")
	switch {
	case s.webOrigin(origin):
		header.Set("Access-Control-Allow-Origin", origin)
		header.Set("Access-Control-Allow-Credentials", "true")
	case s.config.AllowLoopbackOrigins && loopbackOrigin(origin):
		header.Set("Access-Control-Allow-Origin", origin)
	default:
		return preflight
	}
	if preflight {
		header.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE")
		header.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, If-Match, X-Client-Version")
		header.Set("Access-Control-Max-Age", "600")
	}
	return preflight
}

func (s *Server) webOrigin(origin string) bool { return slices.Contains(s.config.WebOrigins, origin) }

func loopbackOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.Path != "" {
		return false
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

func (s *Server) clientIP(r *http.Request) string {
	peer := remoteIP(r.RemoteAddr)
	if ip := net.ParseIP(peer); s.config.TrustProxy && ip != nil && ip.IsLoopback() {
		if real := net.ParseIP(r.Header.Get("X-Real-IP")); real != nil {
			return real.String()
		}
	}
	return peer
}
