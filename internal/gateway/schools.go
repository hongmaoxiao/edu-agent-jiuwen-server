package gateway

import (
	"net/http"
)

func (s *Server) registerSchools() {
	s.route("GET /v1/schools", s.listSchools)
	s.route("POST /v1/schools", s.createSchool)
	s.route("GET /v1/schools/{school}/members", s.listMembers)
	s.route("POST /v1/schools/{school}/members", s.createMember)
}

func (s *Server) listSchools(w http.ResponseWriter, r *http.Request) {
	roles, ok := s.roles(w, r)
	if !ok {
		return
	}
	schools, err := s.accounts.Schools(r.Context(), roles)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schools": schools})
}

func (s *Server) createSchool(w http.ResponseWriter, r *http.Request) {
	roles, ok := s.roles(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
		City string `json:"city"`
	}
	if !decode(w, r, &body) {
		return
	}
	school, err := s.accounts.CreateSchool(r.Context(), roles, body.Name, body.City)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, school)
}

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request) {
	roles, ok := s.roles(w, r)
	if !ok {
		return
	}
	members, err := s.accounts.Members(r.Context(), roles, r.PathValue("school"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": members})
}

func (s *Server) createMember(w http.ResponseWriter, r *http.Request) {
	roles, ok := s.roles(w, r)
	if !ok {
		return
	}
	var body struct {
		Login       string   `json:"login"`
		DisplayName string   `json:"display_name"`
		Roles       []string `json:"roles"`
	}
	if !decode(w, r, &body) {
		return
	}
	member, password, err := s.accounts.CreateMember(r.Context(), roles, r.PathValue("school"), body.Login, body.DisplayName, body.Roles)
	if err != nil {
		fail(w, r, err)
		return
	}
	// 初始密码只在这一次响应里出现，持有人首次登录必须改掉。
	writeJSON(w, http.StatusCreated, map[string]any{"member": member, "initial_password": password})
}
