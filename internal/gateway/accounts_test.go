package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"edu-agent-jiuwen-server/internal/account"
	"edu-agent-jiuwen-server/internal/platform"
	"edu-agent-jiuwen-server/internal/testdb"

	"github.com/jackc/pgx/v5/pgxpool"
)

const webOrigin = "https://agent.hrtiger.cn"

type harness struct {
	t  *testing.T
	s  *Server
	db *pgxpool.Pool
}

func newHarness(t *testing.T) *harness {
	db := testdb.Open(t)
	h := &harness{t: t, db: db, s: New(platform.Config{Environment: "test", WebOrigins: []string{webOrigin}}, db)}
	if _, err := account.New(db).CreateOperator(context.Background(), "ops", "运营", "operator-password"); err != nil {
		t.Fatal(err)
	}
	return h
}

type reply struct {
	code   int
	body   map[string]any
	header http.Header
}

func (r reply) str(path ...string) string {
	var v any = r.body
	for _, key := range path {
		m, _ := v.(map[string]any)
		v = m[key]
	}
	s, _ := v.(string)
	return s
}

func (h *harness) do(method, path, token string, body any, header map[string]string) reply {
	h.t.Helper()
	var reader *strings.Reader
	if body == nil {
		reader = strings.NewReader("")
	} else {
		raw, _ := json.Marshal(body)
		reader = strings.NewReader(string(raw))
	}
	r := httptest.NewRequest(method, path, reader)
	if body == nil {
		r.ContentLength = 0
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.s.ServeHTTP(w, r)
	out := reply{code: w.Code, header: w.Header(), body: map[string]any{}}
	json.Unmarshal(w.Body.Bytes(), &out.body)
	return out
}

func (h *harness) login(login, password, app string) reply {
	return h.do("POST", "/v1/auth/login", "", map[string]string{"login": login, "password": password, "client_app": app}, nil)
}

func (h *harness) mustLogin(login, password, app string) string {
	h.t.Helper()
	r := h.login(login, password, app)
	if r.code != 200 {
		h.t.Fatalf("login %s: %d %v", login, r.code, r.body)
	}
	return r.str("access_token")
}

// member opens an account in a school and returns its initial password.
func (h *harness) member(token, school, login, role string) string {
	h.t.Helper()
	r := h.do("POST", "/v1/schools/"+school+"/members", token, map[string]string{"login": login, "display_name": login, "role": role}, nil)
	if r.code != 201 {
		h.t.Fatalf("create %s: %d %v", login, r.code, r.body)
	}
	return r.str("initial_password")
}

func (h *harness) school(token, name string) string {
	h.t.Helper()
	r := h.do("POST", "/v1/schools", token, map[string]string{"name": name}, nil)
	if r.code != 201 {
		h.t.Fatalf("create school: %d %v", r.code, r.body)
	}
	return r.str("id")
}

// signIn logs in with the initial password, changes it, and returns a usable access token.
func (h *harness) signIn(login, initial string) string {
	h.t.Helper()
	token := h.mustLogin(login, initial, "desktop")
	if r := h.do("POST", "/v1/auth/password", token, map[string]string{"current_password": initial, "new_password": login + "-new-password"}, nil); r.code != 204 {
		h.t.Fatalf("change password %s: %d %v", login, r.code, r.body)
	}
	return token
}

func TestFirstSignInMustChangePassword(t *testing.T) {
	h := newHarness(t)
	ops := h.mustLogin("ops", "operator-password", "desktop")
	school := h.school(ops, "示例学校")
	initial := h.member(ops, school, "admin-a", "school_admin")

	web := h.login("admin-a", initial, "web")
	if web.code != 200 || web.str("refresh_token") != "" || web.body["must_change_password"] != true {
		t.Fatalf("web login must hide the refresh token and require a password change: %d %v", web.code, web.body)
	}
	if cookie := web.header.Get("Set-Cookie"); !strings.Contains(cookie, "edu_refresh=") || !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "Path=/v1/auth") {
		t.Errorf("refresh cookie: %q", cookie)
	}
	token := web.str("access_token")
	if r := h.do("GET", "/v1/schools", token, nil, nil); r.code != 403 || r.str("error", "code") != "PASSWORD_CHANGE_REQUIRED" {
		t.Errorf("before the change: %d %v", r.code, r.body)
	}
	if r := h.do("GET", "/v1/me", token, nil, nil); r.code != 200 {
		t.Errorf("me must stay open before the change: %d", r.code)
	}
	if r := h.do("POST", "/v1/auth/password", token, map[string]string{"current_password": "wrong-password", "new_password": "a-better-password"}, nil); r.code != 400 || r.str("error", "code") != "WRONG_PASSWORD" {
		t.Errorf("wrong current password: %d %v", r.code, r.body)
	}
	if r := h.do("POST", "/v1/auth/password", token, map[string]string{"current_password": initial, "new_password": "short"}, nil); r.code != 400 {
		t.Errorf("short password accepted: %d", r.code)
	}
	if r := h.do("POST", "/v1/auth/password", token, map[string]string{"current_password": initial, "new_password": "a-better-password"}, nil); r.code != 204 {
		t.Fatalf("change: %d %v", r.code, r.body)
	}
	r := h.do("GET", "/v1/schools", token, nil, nil)
	schools, _ := r.body["schools"].([]any)
	if r.code != 200 || len(schools) != 1 {
		t.Fatalf("school admin sees own school only: %d %v", r.code, r.body)
	}
	me := h.do("GET", "/v1/me", token, nil, nil)
	if me.body["account"].(map[string]any)["must_change_password"] != false {
		t.Errorf("flag not cleared: %v", me.body)
	}
	if h.login("admin-a", initial, "desktop").code != 401 {
		t.Error("initial password still works")
	}
}

func TestWhoMayOpenAccounts(t *testing.T) {
	h := newHarness(t)
	ops := h.mustLogin("ops", "operator-password", "desktop")
	a, b := h.school(ops, "学校甲"), h.school(ops, "学校乙")
	admin := h.signIn("admin-a", h.member(ops, a, "admin-a", "school_admin"))

	h.member(admin, a, "teacher-a", "teacher")
	h.member(admin, a, "student-a", "student")
	if r := h.do("POST", "/v1/schools/"+a+"/members", admin, map[string]string{"login": "admin-a2", "display_name": "x", "role": "school_admin"}, nil); r.code != 403 {
		t.Errorf("school admin appointed another admin: %d", r.code)
	}
	if r := h.do("POST", "/v1/schools/"+b+"/members", admin, map[string]string{"login": "student-b", "display_name": "x", "role": "student"}, nil); r.code != 403 {
		t.Errorf("school admin opened an account in another school: %d", r.code)
	}
	if r := h.do("GET", "/v1/schools/"+b+"/members", admin, nil, nil); r.code != 403 {
		t.Errorf("school admin listed another school: %d", r.code)
	}
	if r := h.do("POST", "/v1/schools", admin, map[string]string{"name": "学校丙"}, nil); r.code != 403 {
		t.Errorf("school admin created a school: %d", r.code)
	}
	if r := h.do("POST", "/v1/schools/"+a+"/members", admin, map[string]string{"login": "teacher-a", "display_name": "x", "role": "student"}, nil); r.code != 409 {
		t.Errorf("duplicate login: %d", r.code)
	}
	if r := h.do("POST", "/v1/schools/"+a+"/members", admin, map[string]string{"login": "x", "display_name": "x", "role": "student"}, nil); r.code != 400 {
		t.Errorf("invalid login accepted: %d", r.code)
	}
	if r := h.do("POST", "/v1/schools/"+a+"/members", admin, map[string]string{"login": "someone", "display_name": "x", "role": "boss"}, nil); r.code != 400 {
		t.Errorf("unknown role accepted: %d", r.code)
	}
	if r := h.do("POST", "/v1/schools/sch_missing/members", ops, map[string]string{"login": "ghost", "display_name": "x", "role": "student"}, nil); r.code != 404 {
		t.Errorf("missing school: %d", r.code)
	}

	teacher := h.signIn("teacher-a2", h.member(ops, a, "teacher-a2", "teacher"))
	if r := h.do("POST", "/v1/schools/"+a+"/members", teacher, map[string]string{"login": "student-a2", "display_name": "x", "role": "student"}, nil); r.code != 403 {
		t.Errorf("teacher opened an account: %d", r.code)
	}
	if r := h.do("GET", "/v1/schools/"+a+"/members", teacher, nil, nil); r.code != 403 {
		t.Errorf("teacher listed members: %d", r.code)
	}
	members := h.do("GET", "/v1/schools/"+a+"/members", admin, nil, nil)
	if list, _ := members.body["members"].([]any); members.code != 200 || len(list) != 4 {
		t.Errorf("members of school A: %d %v", members.code, members.body)
	}
}

func TestRefreshRotationAndReuse(t *testing.T) {
	h := newHarness(t)
	first := h.login("ops", "operator-password", "desktop")
	oldAccess, oldRefresh := first.str("access_token"), first.str("refresh_token")
	if oldRefresh == "" {
		t.Fatal("desktop login must return a refresh token")
	}
	next := h.do("POST", "/v1/auth/refresh", "", map[string]string{"refresh_token": oldRefresh}, nil)
	if next.code != 200 || next.str("refresh_token") == "" || next.str("refresh_token") == oldRefresh {
		t.Fatalf("refresh: %d %v", next.code, next.body)
	}
	if r := h.do("GET", "/v1/me", oldAccess, nil, nil); r.code != 401 {
		t.Errorf("old access token still valid after refresh: %d", r.code)
	}
	if r := h.do("GET", "/v1/me", next.str("access_token"), nil, nil); r.code != 200 {
		t.Errorf("new access token: %d", r.code)
	}
	// Presenting a used refresh token means someone else holds a copy: the whole session ends.
	if r := h.do("POST", "/v1/auth/refresh", "", map[string]string{"refresh_token": oldRefresh}, nil); r.code != 401 {
		t.Errorf("reused refresh token accepted: %d", r.code)
	}
	if r := h.do("POST", "/v1/auth/refresh", "", map[string]string{"refresh_token": next.str("refresh_token")}, nil); r.code != 401 {
		t.Errorf("session survived refresh token reuse: %d", r.code)
	}
	if r := h.do("GET", "/v1/me", next.str("access_token"), nil, nil); r.code != 401 {
		t.Errorf("access token survived refresh token reuse: %d", r.code)
	}
}

func TestWebRefreshUsesCookieFromKnownOrigin(t *testing.T) {
	h := newHarness(t)
	web := h.login("ops", "operator-password", "web")
	cookie := strings.Split(web.header.Get("Set-Cookie"), ";")[0]
	if r := h.do("POST", "/v1/auth/refresh", "", nil, map[string]string{"Cookie": cookie}); r.code != 401 {
		t.Errorf("cookie refresh without Origin: %d", r.code)
	}
	if r := h.do("POST", "/v1/auth/refresh", "", nil, map[string]string{"Cookie": cookie, "Origin": "https://evil.example"}); r.code != 401 {
		t.Errorf("cookie refresh from another site: %d", r.code)
	}
	r := h.do("POST", "/v1/auth/refresh", "", nil, map[string]string{"Cookie": cookie, "Origin": webOrigin})
	if r.code != 200 || r.str("refresh_token") != "" || !strings.Contains(r.header.Get("Set-Cookie"), "edu_refresh=") {
		t.Fatalf("cookie refresh: %d %v", r.code, r.body)
	}
	logout := h.do("POST", "/v1/auth/logout", "", nil, map[string]string{"Cookie": strings.Split(r.header.Get("Set-Cookie"), ";")[0], "Origin": webOrigin})
	if logout.code != 204 || !strings.Contains(logout.header.Get("Set-Cookie"), "Max-Age=0") {
		t.Errorf("logout: %d %v", logout.code, logout.header)
	}
	if again := h.do("GET", "/v1/me", r.str("access_token"), nil, nil); again.code != 401 {
		t.Errorf("session survived logout: %d", again.code)
	}
}

func TestPrivilegedDesktopSessionsAreShort(t *testing.T) {
	h := newHarness(t)
	ops := h.mustLogin("ops", "operator-password", "desktop")
	school := h.school(ops, "示例学校")
	h.signIn("student-a", h.member(ops, school, "student-a", "student"))
	h.mustLogin("ops", "operator-password", "web")
	expiry := func(login, app string) time.Duration {
		var expires time.Time
		if err := h.db.QueryRow(context.Background(), "SELECT max(s.expires_at) FROM sessions s JOIN accounts a ON a.id=s.account_id WHERE a.login=$1 AND s.client_app=$2", login, app).Scan(&expires); err != nil {
			t.Fatal(err)
		}
		return time.Until(expires)
	}
	if d := expiry("ops", "desktop"); d > account.PrivilegedDesktopTTL {
		t.Errorf("operator desktop session lasts %v", d)
	}
	if d := expiry("ops", "web"); d < 29*24*time.Hour {
		t.Errorf("operator web session lasts only %v", d)
	}
	if d := expiry("student-a", "desktop"); d < 29*24*time.Hour {
		t.Errorf("student desktop session lasts only %v", d)
	}
}

func TestPasswordChangeEndsOtherSessions(t *testing.T) {
	h := newHarness(t)
	other := h.mustLogin("ops", "operator-password", "web")
	current := h.mustLogin("ops", "operator-password", "desktop")
	if r := h.do("POST", "/v1/auth/password", current, map[string]string{"current_password": "operator-password", "new_password": "operator-password-2"}, nil); r.code != 204 {
		t.Fatalf("change: %d %v", r.code, r.body)
	}
	if r := h.do("GET", "/v1/me", other, nil, nil); r.code != 401 {
		t.Errorf("other session survived: %d", r.code)
	}
	if r := h.do("GET", "/v1/me", current, nil, nil); r.code != 200 {
		t.Errorf("current session ended: %d", r.code)
	}
}

func TestLoginFailuresLookTheSameAndAreLimited(t *testing.T) {
	h := newHarness(t)
	unknown, wrong := h.login("nobody", "whatever-password", "web"), h.login("ops", "wrong-password", "web")
	if unknown.code != 401 || wrong.code != 401 || unknown.str("error", "code") != wrong.str("error", "code") {
		t.Errorf("unknown %d %v / wrong %d %v", unknown.code, unknown.body, wrong.code, wrong.body)
	}
	for i := 0; i < 9; i++ {
		h.login("ops", "wrong-password", "web")
	}
	if r := h.login("ops", "operator-password", "web"); r.code != 429 {
		t.Errorf("11th attempt within 10 minutes: %d", r.code)
	}
}
