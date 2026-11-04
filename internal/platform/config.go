package platform

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	Environment, Listen, DatabaseURL string
	// 只听本机的指标端口（EDU_METRICS_LISTEN），给采集代理读；设为空则关闭。
	MetricsListen string
	// 网页版的来源（EDU_WEB_ORIGINS，逗号分隔）：允许带 Cookie 跨域，刷新 Cookie 也只认这些来源。
	WebOrigins []string
	// 桌面版前端在 http://127.0.0.1:<随机端口>，只用 Authorization 头，不带 Cookie（EDU_ALLOW_LOOPBACK_ORIGINS）。
	AllowLoopbackOrigins bool
	// 刷新 Cookie 的域（EDU_COOKIE_DOMAIN），线上设为 agent.hrtiger.cn，api 子域也能收到；为空时只属于当前主机。
	CookieDomain string
	// 低于这个版本的客户端返回 426（EDU_MIN_CLIENT_VERSION，形如 1.2.0）；为空时不检查。
	MinClientVersion string
	// 部署在本机反向代理之后（EDU_TRUST_PROXY=loopback）：来自回环地址的请求用 X-Real-IP 作客户端地址。
	TrustProxy bool
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
	return c, nil
}

// SecureCookies is false only for local http development.
func (c Config) SecureCookies() bool { return c.Environment == "production" }
