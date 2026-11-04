package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"edu-agent-jiuwen-server/internal/gateway"
	"edu-agent-jiuwen-server/internal/platform"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	config, err := platform.LoadConfig()
	if err != nil {
		slog.Error("configuration rejected", "reason", err.Error())
		os.Exit(1)
	}
	db, err := pgxpool.New(context.Background(), config.DatabaseURL)
	if err != nil {
		slog.Error("database configuration rejected")
		os.Exit(1)
	}
	defer db.Close()
	app := gateway.New(config, db)
	gateway.RegisterDBMetrics(db)
	// 指标只在本机端口给采集代理读，不经 nginx；设为空则关闭。端口起不来只记日志，不影响对外服务。
	if config.MetricsListen != "" {
		metrics := &http.Server{Addr: config.MetricsListen, Handler: promhttp.Handler(), ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := metrics.ListenAndServe(); err != nil {
				slog.Error("metrics listener stopped", "service", "edu-agent-jiuwen-server", "address", config.MetricsListen)
			}
		}()
	}
	server := &http.Server{
		Addr: config.Listen, Handler: app,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-ctx.Done()
		shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if server.Shutdown(shutdown) != nil {
			server.Close()
		}
	}()
	slog.Info("listening", "service", "edu-agent-jiuwen-server", "address", config.Listen)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("HTTP server stopped unexpectedly", "reason", err.Error())
		os.Exit(1)
	}
}
