// Command broker starts and stops per-account WorkSwarm containers for the server (internal/broker).
// It listens on a Unix socket that only the server's group may open, and is the only process with Docker access.
//
//	EDU_BROKER_SOCKET          socket to listen on (default /run/edu-agent-broker/broker.sock)
//	DOCKER_SOCKET              Docker Engine socket (default /var/run/docker.sock)
//	EDU_BROKER_IMAGE           client image
//	EDU_BROKER_GATEWAY_TOKEN_FILE  file holding the model gateway token
//	EDU_BROKER_COURSE_CONTAINER    course service container joined to each account network as course
//	EDU_BROKER_ORIGIN_HOSTS    WorkSwarm's allowed WebSocket origin hosts (default agent.hrtiger.cn,127.0.0.1,localhost)
//	EDU_BROKER_SUBNETS         range carved into one /28 per account (default 10.231.0.0/16)
//	EDU_WORKSPACE_PORTS        same port range as the server (default 57000-57999)
//	EDU_BROKER_MEMORY_MB, EDU_BROKER_CPUS, EDU_BROKER_PIDS  limits per container (default 4096, 2, 512)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"edu-agent-jiuwen-server/internal/broker"
	"edu-agent-jiuwen-server/internal/workspace"
)

func env(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func number(key, fallback string) (int64, error) {
	value, err := strconv.ParseInt(env(key, fallback), 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive number", key)
	}
	return value, nil
}

func config() (broker.Config, error) {
	var c broker.Config
	token, err := os.ReadFile(os.Getenv("EDU_BROKER_GATEWAY_TOKEN_FILE"))
	if err != nil {
		return c, errors.New("EDU_BROKER_GATEWAY_TOKEN_FILE must name a readable file")
	}
	subnets, err := netip.ParsePrefix(env("EDU_BROKER_SUBNETS", "10.231.0.0/16"))
	if err != nil {
		return c, errors.New("EDU_BROKER_SUBNETS must be a CIDR range")
	}
	first, last, _ := strings.Cut(env("EDU_WORKSPACE_PORTS", "57000-57999"), "-")
	c = broker.Config{
		Image: os.Getenv("EDU_BROKER_IMAGE"), GatewayToken: strings.TrimSpace(string(token)),
		CourseContainer: os.Getenv("EDU_BROKER_COURSE_CONTAINER"),
		OriginHosts:     env("EDU_BROKER_ORIGIN_HOSTS", "agent.hrtiger.cn,127.0.0.1,localhost"),
		Subnets:         subnets, ReadyPath: "/api/v1/auth/status", ReadyTimeout: 150 * time.Second,
	}
	if c.FirstPort, err = strconv.Atoi(first); err != nil {
		return c, errors.New("EDU_WORKSPACE_PORTS must look like 57000-57999")
	}
	if c.LastPort, err = strconv.Atoi(last); err != nil {
		return c, errors.New("EDU_WORKSPACE_PORTS must look like 57000-57999")
	}
	memory, err := number("EDU_BROKER_MEMORY_MB", "4096")
	if err != nil {
		return c, err
	}
	cpus, err := number("EDU_BROKER_CPUS", "2")
	if err != nil {
		return c, err
	}
	if c.Pids, err = number("EDU_BROKER_PIDS", "512"); err != nil {
		return c, err
	}
	c.Memory, c.NanoCPUs = memory<<20, cpus*1_000_000_000
	return c, nil
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	c, err := config()
	if err != nil {
		slog.Error("configuration rejected", "service", "edu-agent-broker", "reason", err.Error())
		os.Exit(1)
	}
	b, err := broker.New(c, env("DOCKER_SOCKET", "/var/run/docker.sock"))
	if err != nil {
		slog.Error("configuration rejected", "service", "edu-agent-broker", "reason", err.Error())
		os.Exit(1)
	}
	socket := env("EDU_BROKER_SOCKET", "/run/edu-agent-broker/broker.sock")
	os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		slog.Error("cannot listen", "service", "edu-agent-broker", "socket", socket, "reason", err.Error())
		os.Exit(1)
	}
	// 只有和 broker 同组的用户（服务端 edu-agent）能连；目录权限由 systemd 的 RuntimeDirectory 管。
	if err := os.Chmod(socket, 0o660); err != nil {
		slog.Error("cannot restrict socket", "service", "edu-agent-broker", "reason", err.Error())
		os.Exit(1)
	}
	server := &http.Server{Handler: handler(b), ReadHeaderTimeout: 5 * time.Second}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-ctx.Done()
		shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		server.Shutdown(shutdown)
	}()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := b.Reconnect(ctx); err != nil {
					slog.Error("course reconnect failed", "service", "edu-agent-broker", "reason", err.Error())
				}
			}
		}
	}()
	slog.Info("listening", "service", "edu-agent-broker", "socket", socket)
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("broker stopped", "service", "edu-agent-broker", "reason", err.Error())
		os.Exit(1)
	}
}

func handler(b *broker.Broker) http.Handler {
	mux := http.NewServeMux()
	action := func(name string, run func(context.Context, string, int) error) {
		mux.HandleFunc("POST /"+name, func(w http.ResponseWriter, r *http.Request) {
			var request workspace.Request
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if err := run(r.Context(), request.AccountID, request.Port); err != nil {
				slog.Error(name+" failed", "service", "edu-agent-broker", "account", request.AccountID, "reason", err.Error())
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
	}
	action("ensure", b.Ensure)
	action("stop", b.Stop)
	return mux
}
