package gateway

import (
	"context"
	"testing"

	"edu-agent-jiuwen-server/internal/account"
)

// teacher opens a teacher account outside any school and returns its id and initial password.
func (h *harness) teacher(ops, login string, canOpen bool) (string, string) {
	h.t.Helper()
	r := h.do("POST", "/v1/accounts", ops, map[string]any{"login": login, "display_name": login, "can_open_classes": canOpen}, nil)
	if r.code != 201 {
		h.t.Fatalf("create teacher %s: %d %v", login, r.code, r.body)
	}
	return r.str("account", "id"), r.str("initial_password")
}

func (h *harness) class(token, name string) string {
	h.t.Helper()
	r := h.do("POST", "/v1/classes", token, map[string]string{"name": name}, nil)
	if r.code != 201 {
		h.t.Fatalf("create class %s: %d %v", name, r.code, r.body)
	}
	return r.str("id")
}

// students opens student accounts in a class and returns login → {id, initial password}.
func (h *harness) students(token, class string, logins ...string) map[string][2]string {
	h.t.Helper()
	var list []map[string]string
	for _, login := range logins {
		list = append(list, map[string]string{"login": login, "display_name": login})
	}
	r := h.do("POST", "/v1/classes/"+class+"/students", token, map[string]any{"students": list}, nil)
	if r.code != 201 {
		h.t.Fatalf("create students: %d %v", r.code, r.body)
	}
	out := map[string][2]string{}
	for _, item := range r.body["students"].([]any) {
		created := item.(map[string]any)
		a := created["member"].(map[string]any)["account"].(map[string]any)
		out[a["login"].(string)] = [2]string{a["id"].(string), created["initial_password"].(string)}
	}
	return out
}

func classRoles(r reply) map[string]string {
	out := map[string]string{}
	list, _ := r.body["classes"].([]any)
	for _, item := range list {
		c := item.(map[string]any)
		role, _ := c["role"].(string)
		out[c["name"].(string)] = role
	}
	return out
}

func TestOperatorOpensTeachersAndTeachersOpenStudents(t *testing.T) {
	h := newHarness(t)
	ops := h.mustLogin("ops", "operator-password", "desktop")
	_, initial := h.teacher(ops, "teacher-a", true)
	_, plainInitial := h.teacher(ops, "teacher-b", false)
	teacher := h.signIn("teacher-a", initial)
	plain := h.signIn("teacher-b", plainInitial)

	me := h.do("GET", "/v1/me", teacher, nil, nil)
	if me.body["account"].(map[string]any)["can_open_classes"] != true {
		t.Fatalf("teacher must be allowed to open classes: %v", me.body)
	}
	for _, token := range []string{teacher, plain} {
		if r := h.do("POST", "/v1/accounts", token, map[string]any{"login": "sneaky", "display_name": "x"}, nil); r.code != 403 {
			t.Errorf("only operators open accounts outside classes: %d", r.code)
		}
		if r := h.do("GET", "/v1/accounts", token, nil, nil); r.code != 403 {
			t.Errorf("only operators list accounts: %d", r.code)
		}
	}
	if r := h.do("POST", "/v1/classes", plain, map[string]string{"name": "无权开班"}, nil); r.code != 403 {
		t.Errorf("a teacher without the class permission opened a class: %d", r.code)
	}

	class := h.class(teacher, "AI 实训一班")
	created := h.students(teacher, class, "stu-1", "stu-2")
	student := h.signIn("stu-1", created["stu-1"][1])
	if roles := classRoles(h.do("GET", "/v1/me", student, nil, nil)); roles["AI 实训一班"] != "student" {
		t.Errorf("student must see the class in /v1/me: %v", roles)
	}
	if roles := classRoles(h.do("GET", "/v1/me", teacher, nil, nil)); roles["AI 实训一班"] != "teacher" {
		t.Errorf("teacher must see the class in /v1/me: %v", roles)
	}
	if r := h.do("GET", "/v1/classes/"+class+"/members", teacher, nil, nil); r.code != 200 || len(r.body["members"].([]any)) != 3 {
		t.Errorf("members: %d %v", r.code, r.body)
	}
	for _, token := range []string{student, plain} {
		if r := h.do("GET", "/v1/classes/"+class+"/members", token, nil, nil); r.code != 403 {
			t.Errorf("only the class's teachers and operators see its members: %d", r.code)
		}
		if r := h.do("POST", "/v1/classes/"+class+"/students", token, map[string]any{"students": []map[string]string{{"login": "x-1", "display_name": "x"}}}, nil); r.code != 403 {
			t.Errorf("only the class's teachers and operators open students: %d", r.code)
		}
	}

	// 运营看到全部社会班；运营开班必须指定任教老师。
	if roles := classRoles(h.do("GET", "/v1/classes", ops, nil, nil)); len(roles) != 1 {
		t.Errorf("operator must see every social class: %v", roles)
	}
	if r := h.do("POST", "/v1/classes", ops, map[string]string{"name": "没有老师"}, nil); r.code != 400 {
		t.Errorf("operator class without a teacher: %d", r.code)
	}
}

func TestStudentBatchIsAllOrNothing(t *testing.T) {
	h := newHarness(t)
	ops := h.mustLogin("ops", "operator-password", "desktop")
	_, initial := h.teacher(ops, "teacher-a", true)
	teacher := h.signIn("teacher-a", initial)
	class := h.class(teacher, "一班")
	h.students(teacher, class, "taken")

	r := h.do("POST", "/v1/classes/"+class+"/students", teacher, map[string]any{"students": []map[string]string{
		{"login": "fresh", "display_name": "新"}, {"login": "taken", "display_name": "重复"}}}, nil)
	if r.code != 409 || r.str("error", "message") != "登录名 taken 已被使用" {
		t.Fatalf("a taken login must reject the batch and name it: %d %v", r.code, r.body)
	}
	if h.login("fresh", "whatever-password", "web").code != 401 {
		t.Error("the rest of a rejected batch must not be created")
	}
	if r := h.do("POST", "/v1/classes/"+class+"/students", teacher, map[string]any{"students": []map[string]string{}}, nil); r.code != 400 {
		t.Errorf("empty batch: %d", r.code)
	}
}

func TestPasswordResetsAndRemovalStayInsideTheClass(t *testing.T) {
	h := newHarness(t)
	ops := h.mustLogin("ops", "operator-password", "desktop")
	_, initialA := h.teacher(ops, "teacher-a", true)
	_, initialB := h.teacher(ops, "teacher-b", true)
	a := h.signIn("teacher-a", initialA)
	b := h.signIn("teacher-b", initialB)
	classA := h.class(a, "A 班")
	classB := h.class(b, "B 班")
	mine := h.students(a, classA, "stu-a")["stu-a"]
	other := h.students(b, classB, "stu-b")["stu-b"]
	student := h.signIn("stu-a", mine[1])

	reset := h.do("POST", "/v1/accounts/"+mine[0]+"/password", a, nil, nil)
	if reset.code != 200 || reset.str("initial_password") == "" {
		t.Fatalf("teacher resets own student: %d %v", reset.code, reset.body)
	}
	if r := h.do("GET", "/v1/me", student, nil, nil); r.code != 401 {
		t.Errorf("a reset must end the student's sessions: %d", r.code)
	}
	if r := h.login("stu-a", reset.str("initial_password"), "web"); r.code != 200 || r.body["must_change_password"] != true {
		t.Errorf("the new initial password must work and require a change: %d %v", r.code, r.body)
	}
	if r := h.do("POST", "/v1/accounts/"+other[0]+"/password", a, nil, nil); r.code != 403 {
		t.Errorf("teacher reset a student of another class: %d", r.code)
	}
	var opsID string
	h.db.QueryRow(context.Background(), "SELECT id FROM accounts WHERE login='ops'").Scan(&opsID)
	if r := h.do("POST", "/v1/accounts/"+opsID+"/password", a, nil, nil); r.code != 403 {
		t.Errorf("teacher reset an operator: %d", r.code)
	}
	if _, err := account.New(h.db).CreateOperator(context.Background(), "ops-2", "运营二", "operator-password"); err != nil {
		t.Fatal(err)
	}
	var ops2 string
	h.db.QueryRow(context.Background(), "SELECT id FROM accounts WHERE login='ops-2'").Scan(&ops2)
	if r := h.do("POST", "/v1/accounts/"+ops2+"/password", ops, nil, nil); r.code != 403 {
		t.Errorf("operator reset another operator: %d", r.code)
	}

	if r := h.do("DELETE", "/v1/classes/"+classB+"/students/"+other[0], a, nil, nil); r.code != 403 {
		t.Errorf("teacher removed a student from another class: %d", r.code)
	}
	if r := h.do("DELETE", "/v1/classes/"+classA+"/students/"+mine[0], a, nil, nil); r.code != 204 {
		t.Fatalf("remove student: %d %v", r.code, r.body)
	}
	if r := h.do("POST", "/v1/accounts/"+mine[0]+"/password", a, nil, nil); r.code != 403 {
		t.Errorf("a removed student is no longer the teacher's to reset: %d", r.code)
	}
}

func TestOperatorDisablesAccountsAndGrantsClasses(t *testing.T) {
	h := newHarness(t)
	ops := h.mustLogin("ops", "operator-password", "desktop")
	id, initial := h.teacher(ops, "teacher-a", false)
	teacher := h.signIn("teacher-a", initial)

	if r := h.do("PATCH", "/v1/accounts/"+id, ops, map[string]any{"can_open_classes": true}, nil); r.code != 200 || r.body["can_open_classes"] != true {
		t.Fatalf("grant classes: %d %v", r.code, r.body)
	}
	h.class(teacher, "授权后开的班")
	if r := h.do("PATCH", "/v1/accounts/"+id, teacher, map[string]any{"status": "disabled"}, nil); r.code != 403 {
		t.Errorf("only operators disable accounts: %d", r.code)
	}
	if r := h.do("PATCH", "/v1/accounts/"+id, ops, map[string]any{"status": "disabled"}, nil); r.code != 200 {
		t.Fatalf("disable: %d %v", r.code, r.body)
	}
	if r := h.do("GET", "/v1/me", teacher, nil, nil); r.code != 401 {
		t.Errorf("disabling must end the account's sessions: %d", r.code)
	}
	if r := h.login("teacher-a", "teacher-a-new-password", "web"); r.code != 401 {
		t.Errorf("a disabled account signed in: %d", r.code)
	}
}

func TestDemoSocialClassStaysInsideItsDemoAndResets(t *testing.T) {
	h := newHarness(t)
	ops := h.mustLogin("ops", "operator-password", "desktop")
	h.teacher(ops, "real-teacher", true)
	h.demo("demo", "客户演示")

	token := h.mustLogin("demo", "demo-password", "web")
	if r := h.do("GET", "/v1/accounts", token, nil, nil); r.code != 200 || len(r.body["accounts"].([]any)) != 0 {
		t.Errorf("demo operator must see no real accounts: %d %v", r.code, r.body)
	}
	teacher := h.switchTo(token, "teacher")
	token = teacher.str("access_token")
	me := h.do("GET", "/v1/me", token, nil, nil)
	if roles := classRoles(me); roles["演示社会班"] != "teacher" || me.body["account"].(map[string]any)["can_open_classes"] != true {
		t.Fatalf("demo teacher teaches the demo social class: %v", me.body)
	}
	demoClass := me.body["classes"].([]any)[0].(map[string]any)
	if demoClass["students"] != float64(4) {
		t.Errorf("demo class holds the demo student and three social students: %v", demoClass)
	}
	extra := h.class(token, "演示时开的班")
	h.students(token, extra, "demo-guest")
	h.students(token, demoClass["id"].(string), "demo-guest-2")

	if err := account.New(h.db).ResetDemo(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	token = h.switchTo(h.mustLogin("demo", "demo-password", "web"), "teacher").str("access_token")
	after := h.do("GET", "/v1/me", token, nil, nil)
	if roles := classRoles(after); len(roles) != 1 || roles["演示社会班"] != "teacher" {
		t.Errorf("reset keeps only the demo social class: %v", roles)
	}
	if after.body["classes"].([]any)[0].(map[string]any)["students"] != float64(4) {
		t.Errorf("reset restores the demo class's students: %v", after.body["classes"])
	}
	if h.login("demo-guest", "x-password-123", "web").code != 401 {
		t.Error("students opened during the demo must go at reset")
	}
	var n int
	h.db.QueryRow(context.Background(), "SELECT count(*) FROM accounts WHERE login IN ('demo-guest','demo-guest-2')").Scan(&n)
	if n != 0 {
		t.Errorf("%d demo-opened student accounts survived the reset", n)
	}
}

func TestSetPasswordReplacesAndEndsSessions(t *testing.T) {
	h := newHarness(t)
	h.demo("demo", "客户演示")
	session := h.mustLogin("demo", "demo-password", "web")
	svc := account.New(h.db)
	if err := svc.SetPassword(context.Background(), "demo", "short"); err == nil {
		t.Error("set-password must apply the password rules")
	}
	if err := svc.SetPassword(context.Background(), "demo", "the-old-site-password"); err != nil {
		t.Fatal(err)
	}
	if r := h.do("GET", "/v1/me", session, nil, nil); r.code != 401 {
		t.Errorf("set-password must end existing sessions: %d", r.code)
	}
	if r := h.login("demo", "the-old-site-password", "web"); r.code != 200 || r.body["must_change_password"] != false {
		t.Errorf("new password signs in without a forced change: %d %v", r.code, r.body)
	}
	if err := svc.SetPassword(context.Background(), "demo.teacher", "persona-password"); err == nil {
		t.Error("personas must stay unable to sign in")
	}
}
