package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"edu-agent-jiuwen-server/internal/platform"
)

func call(s *Server, method, path, body string, header map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestCORS(t *testing.T) {
	s := New(platform.Config{WebOrigins: []string{"https://agent.hrtiger.cn"}, AllowLoopbackOrigins: true}, nil)
	preflight := func(origin string) *httptest.ResponseRecorder {
		return call(s, "OPTIONS", "/v1/me", "", map[string]string{"Origin": origin, "Access-Control-Request-Method": "GET"})
	}
	web := preflight("https://agent.hrtiger.cn")
	if web.Code != 204 || web.Header().Get("Access-Control-Allow-Origin") != "https://agent.hrtiger.cn" || web.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("web origin preflight: %d %v", web.Code, web.Header())
	}
	desktop := preflight("http://127.0.0.1:51234")
	if desktop.Header().Get("Access-Control-Allow-Origin") != "http://127.0.0.1:51234" || desktop.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Errorf("desktop loopback must be allowed without cookies: %v", desktop.Header())
	}
	if other := preflight("https://evil.example"); other.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("unknown origin allowed: %v", other.Header())
	}
	closed := New(platform.Config{}, nil)
	if got := call(closed, "OPTIONS", "/v1/me", "", map[string]string{"Origin": "http://localhost:3000", "Access-Control-Request-Method": "GET"}); got.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("loopback allowed although disabled")
	}
}

func TestOldClientIsAskedToUpgrade(t *testing.T) {
	s := New(platform.Config{MinClientVersion: "1.2.0"}, nil)
	old := call(s, "GET", "/livez", "", map[string]string{"X-Client-Version": "1.1.0"})
	if old.Code != 426 || !strings.Contains(old.Body.String(), "CLIENT_TOO_OLD") {
		t.Errorf("old client: %d %s", old.Code, old.Body)
	}
	if current := call(s, "GET", "/livez", "", map[string]string{"X-Client-Version": "1.2.0"}); current.Code != 200 {
		t.Errorf("current client: %d", current.Code)
	}
}

func TestRequestsWithoutSession(t *testing.T) {
	s := New(platform.Config{}, nil)
	if w := call(s, "GET", "/readyz", "", nil); w.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz without database: %d", w.Code)
	}
	if w := call(s, "GET", "/v1/me", "", nil); w.Code != 401 || !strings.Contains(w.Body.String(), `"code":"UNAUTHENTICATED"`) {
		t.Errorf("me without token: %d %s", w.Code, w.Body)
	}
	if w := call(s, "POST", "/v1/auth/login", `{"login":"a","password":"b","client_app":"web","extra":1}`, nil); w.Code != 400 {
		t.Errorf("unknown field accepted: %d", w.Code)
	}
	if w := call(s, "GET", "/nowhere", "", nil); w.Code != 404 {
		t.Errorf("unmatched route: %d", w.Code)
	}
}
