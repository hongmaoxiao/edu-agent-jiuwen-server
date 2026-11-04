package gateway

import (
	"context"
	"net/http"
	"testing"

	"edu-agent-jiuwen-server/internal/account"
)

func (h *harness) demo(login, name string) {
	h.t.Helper()
	if _, err := account.New(h.db).CreateDemo(context.Background(), login, name, login+"-password"); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) switchTo(token, perspective string) reply {
	return h.do("POST", "/v1/session/perspective", token, map[string]string{"perspective": perspective}, nil)
}

func schoolNames(r reply) []string {
	var names []string
	list, _ := r.body["schools"].([]any)
	for _, item := range list {
		names = append(names, item.(map[string]any)["name"].(string))
	}
	return names
}

func TestDemoSwitchesPerspectiveWithoutSigningOut(t *testing.T) {
	h := newHarness(t)
	ops := h.mustLogin("ops", "operator-password", "desktop")
	real := h.school(ops, "真实学校")
	h.demo("demo", "客户演示")

	token := h.mustLogin("demo", "demo-password", "web")
	me := h.do("GET", "/v1/me", token, nil, nil)
	demo, _ := me.body["demo"].(map[string]any)
	if me.code != 200 || demo["perspective"] != "operator" || demo["display_name"] != "客户演示" || me.body["account"].(map[string]any)["is_operator"] != true {
		t.Fatalf("demo sign-in starts as the demo operator: %d %v", me.code, me.body)
	}
	if names := schoolNames(h.do("GET", "/v1/schools", token, nil, nil)); len(names) != 1 || names[0] != "演示学校（客户演示）" {
		t.Errorf("demo operator must see only its demo school, got %v", names)
	}
	if r := h.do("GET", "/v1/schools/"+real+"/members", token, nil, nil); r.code != 404 {
		t.Errorf("demo operator reached a real school: %d", r.code)
	}
	if r := h.do("POST", "/v1/schools/"+real+"/members", token, map[string]string{"login": "intruder", "display_name": "x", "role": "student"}, nil); r.code != 404 {
		t.Errorf("demo operator opened an account in a real school: %d", r.code)
	}

	teacher := h.switchTo(token, "teacher")
	if teacher.code != 200 || teacher.str("perspective") != "teacher" {
		t.Fatalf("switch to teacher: %d %v", teacher.code, teacher.body)
	}
	if r := h.do("GET", "/v1/me", token, nil, nil); r.code != 401 {
		t.Errorf("access token from before the switch still works: %d", r.code)
	}
	token = teacher.str("access_token")
	me = h.do("GET", "/v1/me", token, nil, nil)
	schools, _ := me.body["schools"].([]any)
	if me.body["demo"].(map[string]any)["perspective"] != "teacher" || len(schools) != 1 || schools[0].(map[string]any)["roles"].([]any)[0] != "teacher" {
		t.Errorf("teacher perspective: %v", me.body)
	}
	demoSchool := schools[0].(map[string]any)["id"].(string)
	if r := h.do("POST", "/v1/schools/"+demoSchool+"/members", token, map[string]string{"login": "someone", "display_name": "x", "role": "student"}, nil); r.code != 403 {
		t.Errorf("demo teacher opened an account: %d", r.code)
	}
	student := h.switchTo(token, "student")
	if r := h.do("GET", "/v1/me", student.str("access_token"), nil, nil); r.body["demo"].(map[string]any)["perspective"] != "student" {
		t.Errorf("student perspective: %v", r.body)
	}
	if r := h.switchTo(student.str("access_token"), "boss"); r.code != 400 {
		t.Errorf("unknown perspective: %d", r.code)
	}
	var acted int
	if err := h.db.QueryRow(context.Background(), "SELECT count(*) FROM audit_events e JOIN accounts a ON a.id=e.account_id WHERE a.login='demo' AND e.acting_account_id IS NOT NULL").Scan(&acted); err != nil || acted < 2 {
		t.Errorf("audit must record the demo account and the persona: %d %v", acted, err)
	}
	// The perspective survives a token refresh: it belongs to the session.
	web := h.login("demo", "demo-password", "web")
	h.switchTo(web.str("access_token"), "teacher")
	refreshed := h.do("POST", "/v1/auth/refresh", "", nil, map[string]string{"Cookie": cookieOf(web), "Origin": webOrigin})
	if r := h.do("GET", "/v1/me", refreshed.str("access_token"), nil, nil); r.body["demo"].(map[string]any)["perspective"] != "teacher" {
		t.Errorf("perspective lost on refresh: %v", r.body)
	}
}

func cookieOf(r reply) string {
	for _, c := range (&http.Response{Header: r.header}).Cookies() {
		if c.Name == refreshCookie {
			return c.Name + "=" + c.Value
		}
	}
	return ""
}

func TestOnlyDemoAccountsSwitchAndPersonasCannotSignIn(t *testing.T) {
	h := newHarness(t)
	h.demo("demo", "客户演示")
	ops := h.mustLogin("ops", "operator-password", "desktop")
	if r := h.switchTo(ops, "student"); r.code != 403 {
		t.Errorf("a real operator switched perspective: %d", r.code)
	}
	for _, persona := range []string{"demo.operator", "demo.teacher", "demo.student"} {
		if r := h.login(persona, "!", "web"); r.code != 401 {
			t.Errorf("persona %s signed in directly: %d", persona, r.code)
		}
	}
	token := h.mustLogin("demo", "demo-password", "web")
	if r := h.do("POST", "/v1/auth/password", token, map[string]string{"current_password": "demo-password", "new_password": "demo-password-2"}, nil); r.code != 204 {
		t.Fatalf("demo password change: %d %v", r.code, r.body)
	}
	if r := h.login("demo", "demo-password-2", "web"); r.code != 200 {
		t.Errorf("the demo account's own password must change: %d", r.code)
	}
}

func TestDemoDataStaysInsideItsDemoAndResets(t *testing.T) {
	h := newHarness(t)
	ops := h.mustLogin("ops", "operator-password", "desktop")
	real := h.school(ops, "真实学校")
	h.demo("demo", "客户演示")
	h.demo("other", "另一场演示")

	token := h.mustLogin("demo", "demo-password", "web")
	extra := h.school(token, "演示新校")
	initial := h.member(token, extra, "demo-admin", "school_admin")
	admin := h.signIn("demo-admin", initial)
	if r := h.do("GET", "/v1/schools/"+real+"/members", admin, nil, nil); r.code != 404 {
		t.Errorf("an account opened inside a demo reached a real school: %d", r.code)
	}
	if names := schoolNames(h.do("GET", "/v1/schools", h.mustLogin("other", "other-password", "web"), nil, nil)); len(names) != 1 || names[0] != "演示学校（另一场演示）" {
		t.Errorf("one demo sees another demo's schools: %v", names)
	}
	if names := schoolNames(h.do("GET", "/v1/schools", ops, nil, nil)); len(names) != 4 {
		t.Errorf("the real operator sees every school, demo ones included: %v", names)
	}

	if err := account.New(h.db).ResetDemo(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	if r := h.login("demo-admin", "demo-admin-new-password", "web"); r.code != 401 {
		t.Errorf("account opened during the demo survived the reset: %d", r.code)
	}
	after := h.mustLogin("demo", "demo-password", "web")
	if names := schoolNames(h.do("GET", "/v1/schools", after, nil, nil)); len(names) != 1 || names[0] != "演示学校（客户演示）" {
		t.Errorf("after reset the demo keeps only its own school: %v", names)
	}
	teacher := h.switchTo(after, "teacher")
	if r := h.do("GET", "/v1/me", teacher.str("access_token"), nil, nil); len(r.body["schools"].([]any)) != 1 {
		t.Errorf("teacher persona lost its school on reset: %v", r.body)
	}
	if names := schoolNames(h.do("GET", "/v1/schools", ops, nil, nil)); len(names) != 3 {
		t.Errorf("reset touched other schools: %v", names)
	}
}
