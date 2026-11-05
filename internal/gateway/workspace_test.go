package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"edu-agent-jiuwen-server/internal/workspace"
)

type fakeBroker struct {
	mu      sync.Mutex
	ensured map[string]int
	stopped []string
}

func (b *fakeBroker) Ensure(_ context.Context, accountID string, port int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensured[accountID]++
	return nil
}

func (b *fakeBroker) Stop(_ context.Context, accountID string, _ int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopped = append(b.stopped, accountID)
	return nil
}

func (b *fakeBroker) stops(accountID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, id := range b.stopped {
		if id == accountID {
			n++
		}
	}
	return n
}

func (h *harness) withWorkspaces(limit int) *fakeBroker {
	broker := &fakeBroker{ensured: map[string]int{}}
	h.s.WithWorkspaces(workspace.New(h.db, broker, workspace.Config{
		FirstPort: 57000, LastPort: 57009, Limit: limit, Idle: time.Hour, StartTimeout: time.Minute, Starts: 2,
	}))
	return broker
}

// open calls POST /v1/workspace with an optional forwarding cookie; it returns the reply and the cookie to keep.
func (h *harness) open(token, cookie string) (reply, string) {
	h.t.Helper()
	header := map[string]string{}
	if cookie != "" {
		header["Cookie"] = workspaceCookie + "=" + cookie
	}
	r := h.do("POST", "/v1/workspace", token, nil, header)
	for _, c := range (&http.Response{Header: r.header}).Cookies() {
		if c.Name == workspaceCookie {
			cookie = c.Value
		}
	}
	return r, cookie
}

// ready opens the workspace and waits until the fake broker's start has been recorded.
func (h *harness) ready(token string) string {
	h.t.Helper()
	r, cookie := h.open(token, "")
	if r.code != 200 || r.str("state") != "starting" || r.body["cookie_issued"] != true {
		h.t.Fatalf("first open: %d %v", r.code, r.body)
	}
	for range 100 {
		if r, _ = h.open(token, cookie); r.str("state") == "ready" {
			if r.body["cookie_issued"] != false {
				h.t.Errorf("a valid cookie must be kept: %v", r.body)
			}
			return cookie
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("workspace never became ready: %v", r.body)
	return ""
}

func (h *harness) upstream(cookie string) (int, string) {
	r := httptest.NewRequest("GET", "/internal/workspace/auth", nil)
	if cookie != "" {
		r.Header.Set("Cookie", workspaceCookie+"="+cookie)
	}
	w := httptest.NewRecorder()
	h.s.Internal().ServeHTTP(w, r)
	return w.Code, w.Header().Get("X-Workspace-Upstream")
}

func (h *harness) accountID(login string) string {
	var id string
	h.db.QueryRow(context.Background(), "SELECT id FROM accounts WHERE login=$1", login).Scan(&id)
	return id
}

func TestWorkspaceForwardingFollowsTheSession(t *testing.T) {
	h := newHarness(t)
	h.withWorkspaces(10)
	ops := h.mustLogin("ops", "operator-password", "desktop")
	_, initial := h.teacher(ops, "teacher-a", true)
	teacher := h.signIn("teacher-a", initial)

	first, cookie := h.open(teacher, "")
	if set := first.header.Get("Set-Cookie"); !strings.Contains(set, "HttpOnly") || !strings.Contains(set, "Path=/") || strings.Contains(strings.ToLower(set), "domain=") {
		t.Errorf("forwarding cookie must be host-only, HttpOnly, whole site: %q", set)
	}
	if code, _ := h.upstream(cookie); code != 401 {
		t.Errorf("no forwarding before the container is ready: %d", code)
	}
	cookie = h.ready(teacher)
	code, upstream := h.upstream(cookie)
	if code != 204 || !strings.HasPrefix(upstream, "127.0.0.1:570") {
		t.Fatalf("ready workspace must forward to its loopback port: %d %q", code, upstream)
	}
	if code, _ := h.upstream(""); code != 401 {
		t.Errorf("no cookie: %d", code)
	}
	if code, _ := h.upstream(strings.Repeat("x", 40)); code != 401 {
		t.Errorf("unknown cookie: %d", code)
	}
	if r := h.do("GET", "/internal/workspace/auth", "", nil, map[string]string{"Cookie": workspaceCookie + "=" + cookie}); r.code != 404 {
		t.Errorf("the public API must not answer the internal check: %d", r.code)
	}

	if r := h.do("POST", "/v1/auth/logout", teacher, nil, nil); r.code != 204 {
		t.Fatalf("logout: %d", r.code)
	}
	if code, _ := h.upstream(cookie); code != 401 {
		t.Errorf("logout must end forwarding: %d", code)
	}
}

func TestStudentsGetTheirOwnContainers(t *testing.T) {
	h := newHarness(t)
	h.withWorkspaces(10)
	ops := h.mustLogin("ops", "operator-password", "desktop")
	_, initial := h.teacher(ops, "teacher-a", true)
	teacher := h.signIn("teacher-a", initial)
	created := h.students(teacher, h.class(teacher, "一班"), "stu-1", "stu-2")
	one := h.signIn("stu-1", created["stu-1"][1])
	two := h.signIn("stu-2", created["stu-2"][1])
	_, a := h.upstream(h.ready(one))
	_, b := h.upstream(h.ready(two))
	if a == "" || a == b {
		t.Errorf("two students must reach different containers: %q %q", a, b)
	}
	// 同一个学生在另一台电脑登录，进的还是自己的容器。
	again := h.mustLogin("stu-1", "stu-1-new-password", "web")
	r, cookie := h.open(again, "")
	if _, c := h.upstream(cookie); r.str("state") != "ready" || r.body["cookie_issued"] != true || c != a {
		t.Errorf("same student, second session: %v %q want %q", r.body, c, a)
	}
}

func TestDisablingOrResettingStopsTheContainer(t *testing.T) {
	h := newHarness(t)
	broker := h.withWorkspaces(10)
	ops := h.mustLogin("ops", "operator-password", "desktop")
	id, initial := h.teacher(ops, "teacher-a", true)
	teacher := h.signIn("teacher-a", initial)
	student := h.students(teacher, h.class(teacher, "一班"), "stu-1")["stu-1"]
	studentToken := h.signIn("stu-1", student[1])

	studentCookie := h.ready(studentToken)
	if r := h.do("POST", "/v1/accounts/"+student[0]+"/password", teacher, nil, nil); r.code != 200 {
		t.Fatalf("reset: %d", r.code)
	}
	if code, _ := h.upstream(studentCookie); code != 401 || broker.stops(student[0]) != 1 {
		t.Errorf("a reset must end forwarding and stop the container: %d, %d stops", code, broker.stops(student[0]))
	}

	teacherCookie := h.ready(teacher)
	if r := h.do("PATCH", "/v1/accounts/"+id, ops, map[string]any{"status": "disabled"}, nil); r.code != 200 {
		t.Fatalf("disable: %d", r.code)
	}
	if code, _ := h.upstream(teacherCookie); code != 401 || broker.stops(id) != 1 {
		t.Errorf("disabling must end forwarding and stop the container: %d, %d stops", code, broker.stops(id))
	}
}

func TestWorkspaceLimitAndIdleStop(t *testing.T) {
	h := newHarness(t)
	broker := h.withWorkspaces(1)
	ops := h.mustLogin("ops", "operator-password", "desktop")
	_, initialA := h.teacher(ops, "teacher-a", true)
	_, initialB := h.teacher(ops, "teacher-b", true)
	a := h.signIn("teacher-a", initialA)
	b := h.signIn("teacher-b", initialB)

	cookie := h.ready(a)
	if r, _ := h.open(b, ""); r.str("state") != "busy" {
		t.Errorf("over the limit the second person must wait: %v", r.body)
	}
	h.db.Exec(context.Background(), "UPDATE workspaces SET last_seen_at=now()-interval '2 hours'")
	if err := workspace.New(h.db, broker, workspace.Config{Idle: time.Hour, Limit: 1}).Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if code, _ := h.upstream(cookie); code != 401 || broker.stops(h.accountID("teacher-a")) != 1 {
		t.Errorf("an idle container must stop: %d", code)
	}
	if r, _ := h.open(b, ""); r.str("state") != "starting" {
		t.Errorf("a freed place must go to the next person: %v", r.body)
	}
}

func TestDemoPerspectivesShareOneWorkspace(t *testing.T) {
	h := newHarness(t)
	broker := h.withWorkspaces(10)
	h.demo("demo", "客户演示")
	token := h.mustLogin("demo", "demo-password", "web")
	cookie := h.ready(token)
	_, before := h.upstream(cookie)
	token = h.switchTo(token, "student").str("access_token")
	r, kept := h.open(token, cookie)
	if r.str("state") != "ready" || r.body["cookie_issued"] != false || kept != cookie {
		t.Errorf("switching perspective keeps the workspace and its cookie: %v", r.body)
	}
	if _, after := h.upstream(cookie); after != before || broker.ensured[h.accountID("demo")] != 1 {
		t.Errorf("one container per demo account: %q → %q, %d starts", before, after, broker.ensured[h.accountID("demo")])
	}
}

func TestWorkspaceNeedsAChangedPassword(t *testing.T) {
	h := newHarness(t)
	h.withWorkspaces(10)
	ops := h.mustLogin("ops", "operator-password", "desktop")
	_, initial := h.teacher(ops, "teacher-a", true)
	token := h.mustLogin("teacher-a", initial, "web")
	if r, _ := h.open(token, ""); r.code != 403 || r.str("error", "code") != "PASSWORD_CHANGE_REQUIRED" {
		t.Errorf("first sign-in must change the password before the workspace: %d %v", r.code, r.body)
	}
	if r := h.do("POST", "/v1/workspace", ops, nil, nil); r.code != 200 {
		t.Errorf("signed-in operator: %d", r.code)
	}
	h.s.WithWorkspaces(nil)
	if r := h.do("POST", "/v1/workspace", ops, nil, nil); r.code != 503 {
		t.Errorf("without a broker the endpoint must say so: %d", r.code)
	}
}
