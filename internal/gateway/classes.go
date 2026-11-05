package gateway

import (
	"net/http"

	"edu-agent-jiuwen-server/internal/account"
)

// Accounts outside schools and social classes: operators open teachers, teachers open their students.
func (s *Server) registerClasses() {
	s.route("GET /v1/accounts", s.listAccounts)
	s.route("POST /v1/accounts", s.createAccount)
	s.route("PATCH /v1/accounts/{account}", s.updateAccount)
	s.route("POST /v1/accounts/{account}/password", s.resetPassword)
	s.route("GET /v1/classes", s.listClasses)
	s.route("POST /v1/classes", s.createClass)
	s.route("GET /v1/classes/{class}/members", s.listClassMembers)
	s.route("POST /v1/classes/{class}/students", s.createStudents)
	s.route("DELETE /v1/classes/{class}/students/{account}", s.removeStudent)
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	roles, ok := s.roles(w, r)
	if !ok {
		return
	}
	accounts, err := s.accounts.Accounts(r.Context(), roles)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": accounts})
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	roles, ok := s.roles(w, r)
	if !ok {
		return
	}
	var body struct {
		Login            string `json:"login"`
		DisplayName      string `json:"display_name"`
		CanOpenClasses   bool   `json:"can_open_classes"`
		CanCreateCourses bool   `json:"can_create_courses"`
	}
	if !decode(w, r, &body) {
		return
	}
	created, password, err := s.accounts.CreateAccount(r.Context(), roles, body.Login, body.DisplayName, body.CanOpenClasses, body.CanCreateCourses)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"account": created, "initial_password": password})
}

func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request) {
	roles, ok := s.roles(w, r)
	if !ok {
		return
	}
	var change account.AccountChange
	if !decode(w, r, &change) {
		return
	}
	updated, err := s.accounts.UpdateAccount(r.Context(), roles, r.PathValue("account"), change)
	if err != nil {
		fail(w, r, err)
		return
	}
	if updated.Status == "disabled" {
		s.stopWorkspace(r, updated.ID)
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request) {
	roles, ok := s.roles(w, r)
	if !ok {
		return
	}
	password, err := s.accounts.ResetPassword(r.Context(), roles, r.PathValue("account"))
	if err != nil {
		fail(w, r, err)
		return
	}
	s.stopWorkspace(r, r.PathValue("account"))
	writeJSON(w, http.StatusOK, map[string]string{"initial_password": password})
}

func (s *Server) listClasses(w http.ResponseWriter, r *http.Request) {
	roles, ok := s.roles(w, r)
	if !ok {
		return
	}
	classes, err := s.accounts.Classes(r.Context(), roles)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"classes": classes})
}

func (s *Server) createClass(w http.ResponseWriter, r *http.Request) {
	roles, ok := s.roles(w, r)
	if !ok {
		return
	}
	var body struct {
		Name      string `json:"name"`
		TeacherID string `json:"teacher_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	class, err := s.accounts.CreateClass(r.Context(), roles, body.Name, body.TeacherID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, class)
}

func (s *Server) listClassMembers(w http.ResponseWriter, r *http.Request) {
	roles, ok := s.roles(w, r)
	if !ok {
		return
	}
	members, err := s.accounts.ClassMembers(r.Context(), roles, r.PathValue("class"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": members})
}

func (s *Server) createStudents(w http.ResponseWriter, r *http.Request) {
	roles, ok := s.roles(w, r)
	if !ok {
		return
	}
	var body struct {
		Students []account.NewStudent `json:"students"`
	}
	if !decode(w, r, &body) {
		return
	}
	created, err := s.accounts.CreateStudents(r.Context(), roles, r.PathValue("class"), body.Students)
	if err != nil {
		fail(w, r, err)
		return
	}
	// 初始密码只在这一次响应里出现，学员首次登录必须改掉。
	writeJSON(w, http.StatusCreated, map[string]any{"students": created})
}

func (s *Server) removeStudent(w http.ResponseWriter, r *http.Request) {
	roles, ok := s.roles(w, r)
	if !ok {
		return
	}
	if err := s.accounts.RemoveStudent(r.Context(), roles, r.PathValue("class"), r.PathValue("account")); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
