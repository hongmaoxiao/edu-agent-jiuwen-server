package platform

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment, Listen, DatabaseURL string
	// 只听本机的指标端口（EDU_METRICS_LISTEN），给采集代理读；设为空则关闭。
	MetricsListen string
	// 网页版的来源（EDU_WEB_ORIGINS，逗号分隔）：允许带 Cookie 跨域，刷新 Cookie 也只认这些来源。
	WebOrigins []string
	// 桌面版前端在 http://127.0.0.1:<随机端口>，只用 Authorization 头，不带 Cookie（EDU_ALLOW_LOOPBACK_ORIGINS）。
	AllowLoopbackOrigins bool
	// 刷新 Cookie 的域（EDU_COOKIE_DOMAIN）。线上留空：cookie 只属于 agent-api.hrtiger.cn，页面 agent.hrtiger.cn 与它同站，照样能带上。
	CookieDomain string
	// 低于这个版本的客户端返回 426（EDU_MIN_CLIENT_VERSION，形如 1.2.0）；为空时不检查。
	MinClientVersion string
	// 部署在本机反向代理之后（EDU_TRUST_PROXY=loopback）：来自回环地址的请求用 X-Real-IP 作客户端地址。
	TrustProxy bool
	// 每人一个 WorkSwarm 容器（docs/plan/02-backend.md §10）。EDU_WORKSPACE_BROKER 是 cmd/broker 的 Unix socket，为空则不开。
	WorkspaceBroker string
	// nginx auth_request 用的内部端口（EDU_INTERNAL_LISTEN），只听本机，和对外的 API 端口分开。
	InternalListen string
	// 分给各账号的宿主机端口段（EDU_WORKSPACE_PORTS，如 57000-57999），一人一个，永久不变。
	WorkspaceFirstPort, WorkspaceLastPort int
	// 同时运行的容器上限（EDU_WORKSPACE_LIMIT），超出的人稍后再进。内测给保守值。
	WorkspaceLimit int
	// 多久没人用就停容器（EDU_WORKSPACE_IDLE，如 30m），数据保留。
	WorkspaceIdle time.Duration
}

func env(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func LoadConfig() (Config, error) {
	c := Config{
		Environment:          env("EDU_ENV", "production"),
		Listen:               env("EDU_LISTEN", "127.0.0.1:4020"),
		DatabaseURL:          os.Getenv("DATABASE_URL"),
		MetricsListen:        env("EDU_METRICS_LISTEN", "127.0.0.1:4029"),
		AllowLoopbackOrigins: env("EDU_ALLOW_LOOPBACK_ORIGINS", "true") == "true",
		CookieDomain:         env("EDU_COOKIE_DOMAIN", ""),
		MinClientVersion:     env("EDU_MIN_CLIENT_VERSION", ""),
		TrustProxy:           env("EDU_TRUST_PROXY", "") == "loopback",
	}
	switch c.Environment {
	case "production", "development", "test":
	default:
		return c, fmt.Errorf("EDU_ENV must be production, development or test")
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	for _, origin := range strings.Split(env("EDU_WEB_ORIGINS", ""), ",") {
		if origin = strings.TrimSpace(origin); origin == "" {
			continue
		}
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || u.Path != "" || (u.Scheme != "https" && !(c.Environment != "production" && u.Scheme == "http")) {
			return c, fmt.Errorf("EDU_WEB_ORIGINS entries must be https origins without a path")
		}
		c.WebOrigins = append(c.WebOrigins, origin)
	}
	if c.MinClientVersion != "" && !ValidVersion(c.MinClientVersion) {
		return c, fmt.Errorf("EDU_MIN_CLIENT_VERSION must look like 1.2.0")
	}
	return c, c.loadWorkspace()
}

func (c *Config) loadWorkspace() error {
	c.WorkspaceBroker = os.Getenv("EDU_WORKSPACE_BROKER")
	if c.WorkspaceBroker == "" {
		return nil
	}
	c.InternalListen = env("EDU_INTERNAL_LISTEN", "127.0.0.1:4021")
	first, last, ok := strings.Cut(env("EDU_WORKSPACE_PORTS", "57000-57999"), "-")
	var err error
	if c.WorkspaceFirstPort, err = strconv.Atoi(first); err != nil || !ok {
		return fmt.Errorf("EDU_WORKSPACE_PORTS must look like 57000-57999")
	}
	if c.WorkspaceLastPort, err = strconv.Atoi(last); err != nil || c.WorkspaceFirstPort < 1024 || c.WorkspaceLastPort > 65535 || c.WorkspaceFirstPort > c.WorkspaceLastPort {
		return fmt.Errorf("EDU_WORKSPACE_PORTS must look like 57000-57999")
	}
	if c.WorkspaceLimit, err = strconv.Atoi(env("EDU_WORKSPACE_LIMIT", "16")); err != nil || c.WorkspaceLimit < 1 {
		return fmt.Errorf("EDU_WORKSPACE_LIMIT must be a positive number")
	}
	if c.WorkspaceIdle, err = time.ParseDuration(env("EDU_WORKSPACE_IDLE", "30m")); err != nil || c.WorkspaceIdle < time.Minute {
		return fmt.Errorf("EDU_WORKSPACE_IDLE must be a duration of at least 1m")
	}
	return nil
}

// SecureCookies is false only for local http development.
func (c Config) SecureCookies() bool { return c.Environment == "production" }
