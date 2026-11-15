package gateway

import (
	"net/http"
	"strconv"
	"time"

	"edu-agent-jiuwen-server/internal/account"
	"edu-agent-jiuwen-server/internal/teaching"
)

// Numbers and graded work: 管理 · 总览/用量/院校/模型/预算, 教学 · 作品评阅/成绩册/申诉, 成绩 · 我的作品/用量.
func (s *Server) registerTeaching() {
	s.route("GET /v1/overview", s.answer(func(r *http.Request, roles account.Roles) (any, error) {
		return s.teaching.Overview(r.Context(), roles, scopeOf(r), time.Now())
	}))
	s.route("GET /v1/usage", s.answer(func(r *http.Request, roles account.Roles) (any, error) {
		days, _ := strconv.Atoi(r.URL.Query().Get("days"))
		if days == 0 {
			days = 30
		}
		return s.teaching.Usage(r.Context(), roles, scopeOf(r), r.URL.Query().Get("group"), days, time.Now())
	}))
	s.route("GET /v1/schools/stats", s.answer(func(r *http.Request, roles account.Roles) (any, error) {
		schools, err := s.teaching.Schools(r.Context(), roles, time.Now())
		return map[string]any{"schools": schools}, err
	}))
	s.route("GET /v1/models", s.answer(func(r *http.Request, roles account.Roles) (any, error) {
		models, err := s.teaching.Models(r.Context(), roles, time.Now())
		return map[string]any{"models": models}, err
	}))
	s.route("GET /v1/budgets", s.answer(func(r *http.Request, roles account.Roles) (any, error) {
		budgets, err := s.teaching.Budgets(r.Context(), roles, time.Now())
		return map[string]any{"budgets": budgets}, err
	}))
	s.route("GET /v1/works", s.answer(func(r *http.Request, roles account.Roles) (any, error) {
		works, err := s.teaching.Works(r.Context(), roles)
		return map[string]any{"works": works}, err
	}))
	s.route("GET /v1/works/{work}", s.answer(func(r *http.Request, roles account.Roles) (any, error) {
		return s.teaching.Work(r.Context(), roles, r.PathValue("work"))
	}))
	s.route("POST /v1/works/{work}/appeal", answerBody(s, func(r *http.Request, roles account.Roles, body *struct {
		Item   string `json:"item"`
		Reason string `json:"reason"`
	}) (any, error) {
		return s.teaching.Appeal(r.Context(), roles, r.PathValue("work"), body.Item, body.Reason, time.Now())
	}))
	s.route("GET /v1/review", s.answer(func(r *http.Request, roles account.Roles) (any, error) {
		return s.teaching.Review(r.Context(), roles, r.URL.Query().Get("class_id"), r.URL.Query().Get("lab_id"))
	}))
	s.route("PUT /v1/review/{work}", answerBody(s, func(r *http.Request, roles account.Roles, body *struct {
		Scores  []int  `json:"scores"`
		Comment string `json:"comment"`
		Publish bool   `json:"publish"`
	}) (any, error) {
		return s.teaching.Grade(r.Context(), roles, r.PathValue("work"), body.Scores, body.Comment, body.Publish, time.Now())
	}))
	s.route("POST /v1/review/publish", answerBody(s, func(r *http.Request, roles account.Roles, body *struct {
		IDs []string `json:"ids"`
	}) (any, error) {
		n, err := s.teaching.Publish(r.Context(), roles, body.IDs, time.Now())
		return map[string]any{"published": n}, err
	}))
	s.route("GET /v1/gradebook", s.answer(func(r *http.Request, roles account.Roles) (any, error) {
		return s.teaching.Gradebook(r.Context(), roles, r.URL.Query().Get("class_id"))
	}))
	s.route("GET /v1/appeals", s.answer(func(r *http.Request, roles account.Roles) (any, error) {
		appeals, err := s.teaching.Appeals(r.Context(), roles)
		return map[string]any{"appeals": appeals}, err
	}))
	s.route("POST /v1/appeals/{appeal}/answer", answerBody(s, func(r *http.Request, roles account.Roles, body *struct {
		Reply string `json:"reply"`
	}) (any, error) {
		return nil, s.teaching.Answer(r.Context(), roles, r.PathValue("appeal"), body.Reply, time.Now())
	}))
}

func scopeOf(r *http.Request) teaching.Scope {
	q := r.URL.Query()
	return teaching.Scope{Kind: q.Get("scope"), SchoolID: q.Get("school_id"), ClassID: q.Get("class_id")}
}

// answer runs a request for a signed-in caller and writes the result (204 when there is none).
func (s *Server) answer(fn func(*http.Request, account.Roles) (any, error)) http.HandlerFunc {
	return answerBody(s, func(r *http.Request, roles account.Roles, _ *struct{}) (any, error) { return fn(r, roles) })
}

// answerBody is answer for requests with a JSON body of type T; a struct{} T reads no body.
func answerBody[T any](s *Server, fn func(*http.Request, account.Roles, *T) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roles, ok := s.roles(w, r)
		if !ok {
			return
		}
		var body T
		if _, none := any(body).(struct{}); !none && !decode(w, r, &body) {
			return
		}
		result, err := fn(r, roles, &body)
		if err != nil {
			fail(w, r, err)
			return
		}
		if result == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}
