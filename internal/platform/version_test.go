package platform

import "testing"

func TestOlderThan(t *testing.T) {
	cases := []struct {
		version, minimum string
		older            bool
	}{
		{"1.2.0", "1.2.0", false},
		{"1.1.9", "1.2.0", true},
		{"v1.10.0", "1.9.0", false},
		{"0.9.0", "1.0.0", true},
		{"garbage", "1.0.0", false}, // unparseable versions are not judged
		{"", "1.0.0", false},
	}
	for _, c := range cases {
		if got := OlderThan(c.version, c.minimum); got != c.older {
			t.Errorf("OlderThan(%q, %q) = %v", c.version, c.minimum, got)
		}
	}
}

func TestWebOriginsMustBeHTTPSInProduction(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("EDU_ENV", "production")
	t.Setenv("EDU_WEB_ORIGINS", "http://agent.hrtiger.cn")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("http origin accepted in production")
	}
	t.Setenv("EDU_WEB_ORIGINS", "https://agent.hrtiger.cn, https://x.example/path")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("origin with a path accepted")
	}
	t.Setenv("EDU_WEB_ORIGINS", "https://agent.hrtiger.cn")
	c, err := LoadConfig()
	if err != nil || len(c.WebOrigins) != 1 {
		t.Fatalf("valid origin rejected: %v", err)
	}
}
