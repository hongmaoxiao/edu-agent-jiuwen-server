package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"edu-agent-jiuwen-server/internal/account"
)

// 网页版的刷新令牌放在 HttpOnly Cookie 里，只发给 /v1/auth；桌面版放在系统钥匙串，经请求体传。
const refreshCookie = "edu_refresh"

func (s *Server) registerAuth() {
	s.route("POST /v1/auth/login", s.login)
	s.route("POST /v1/auth/refresh", s.refresh)
	s.route("POST /v1/auth/logout", s.logout)
	s.route("POST /v1/auth/password", s.changePassword)
	s.route("GET /v1/me", s.me)
	s.route("POST /v1/session/perspective", s.switchPerspective)
}

func (s *Server) setRefreshCookie(w http.ResponseWriter, tokens account.Tokens) {
	http.SetCookie(w, &http.Cookie{Name: refreshCookie, Value: tokens.RefreshToken, Path: "/v1/auth", Domain: s.config.CookieDomain,
		Expires: tokens.RefreshExpiresAt, HttpOnly: true, Secure: s.config.SecureCookies(), SameSite: http.SameSiteLaxMode})
}

func (s *Server) clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: refreshCookie, Value: "", Path: "/v1/auth", Domain: s.config.CookieDomain,
		MaxAge: -1, HttpOnly: true, Secure: s.config.SecureCookies(), SameSite: http.SameSiteLaxMode})
}

// cookieAllowed: a request that relies on the refresh cookie must come from a configured web origin,
// so another site cannot make the browser spend it.
func (s *Server) cookieAllowed(r *http.Request) bool { return s.webOrigin(r.Header.Get("Origin")) }

// respondTokens returns tokens to the client. Web clients get the refresh token only as a cookie.
func (s *Server) respondTokens(w http.ResponseWriter, tokens account.Tokens, web bool) {
	if web {
		s.setRefreshCookie(w, tokens)
		tokens.RefreshToken = ""
	}
	writeJSON(w, http.StatusOK, tokens)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Login     string `json:"login"`
		Password  string `json:"password"`
		ClientApp string `json:"client_app"`
	}
	if !decode(w, r, &body) {
		return
	}
	login := strings.ToLower(strings.TrimSpace(body.Login))
	if !s.limits.allow("login-ip:"+ipBlock(s.clientIP(r)), 30, 10*time.Minute) || !s.limits.allow("login:"+login, 10, 10*time.Minute) {
		logins.WithLabelValues("rate_limited").Inc()
		writeError(w, 429, "LOGIN_RATE_LIMITED", "登录尝试太多，请 10 分钟后再试")
		return
	}
	tokens, err := s.accounts.Login(r.Context(), body.Login, body.Password,
		account.Client{App: body.ClientApp, Version: r.Header.Get("X-Client-Version"), UserAgent: r.UserAgent()})
	if errors.Is(err, account.ErrInvalid) {
		logins.WithLabelValues("invalid").Inc()
		writeError(w, 401, "INVALID_CREDENTIALS", "账号或密码不正确")
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	logins.WithLabelValues("success").Inc()
	s.respondTokens(w, tokens, body.ClientApp == "web")
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	web := body.RefreshToken == ""
	if web {
		cookie, err := r.Cookie(refreshCookie)
		if err != nil || !s.cookieAllowed(r) {
			writeError(w, 401, "UNAUTHENTICATED", "登录已失效，请重新登录")
			return
		}
		body.RefreshToken = cookie.Value
	}
	tokens, err := s.accounts.Refresh(r.Context(), body.RefreshToken)
	if errors.Is(err, account.ErrInvalid) {
		if web {
			s.clearRefreshCookie(w)
		}
		writeError(w, 401, "UNAUTHENTICATED", "登录已失效，请重新登录")
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	s.respondTokens(w, tokens, web)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	raw, hasBearer := bearer(r)
	if p, err := s.authenticate(r, raw); hasBearer && err == nil {
		if err := s.accounts.Logout(r.Context(), p.SessionID); err != nil {
			fail(w, r, err)
			return
		}
	} else if cookie, err := r.Cookie(refreshCookie); err == nil && s.cookieAllowed(r) {
		if err := s.accounts.LogoutRefresh(r.Context(), cookie.Value); err != nil {
			fail(w, r, err)
			return
		}
	}
	s.clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func bearer(r *http.Request) (string, bool) {
	fields := strings.Fields(r.Header.Get("Authorization"))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return "", false
	}
	return fields[1], true
}

func (s *Server) authenticate(r *http.Request, raw string) (account.Principal, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	return s.accounts.Authenticate(ctx, raw)
}

// signedIn requires a valid access token; it answers the request itself when there is none.
func (s *Server) signedIn(w http.ResponseWriter, r *http.Request) (account.Principal, bool) {
	raw, ok := bearer(r)
	if !ok {
		writeError(w, 401, "UNAUTHENTICATED", "请先登录")
		return account.Principal{}, false
	}
	p, err := s.authenticate(r, raw)
	if errors.Is(err, account.ErrInvalid) {
		writeError(w, 401, "UNAUTHENTICATED", "登录已失效，请重新登录")
		return p, false
	}
	if err != nil {
		fail(w, r, err)
		return p, false
	}
	return p, true
}

// principal is signedIn plus the first sign-in rule: until the initial password is changed,
// only /v1/me, /v1/auth/password and logout are open.
func (s *Server) principal(w http.ResponseWriter, r *http.Request) (account.Principal, bool) {
	p, ok := s.signedIn(w, r)
	if ok && p.MustChangePassword {
		writeError(w, 403, "PASSWORD_CHANGE_REQUIRED", "首次登录请先修改密码")
		return p, false
	}
	return p, ok
}

// roles loads what the caller may do, after the sign-in checks.
func (s *Server) roles(w http.ResponseWriter, r *http.Request) (account.Roles, bool) {
	p, ok := s.principal(w, r)
	if !ok {
		return account.Roles{}, false
	}
	roles, err := s.accounts.Roles(r.Context(), p)
	if err != nil {
		fail(w, r, err)
		return roles, false
	}
	return roles, true
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	p, ok := s.signedIn(w, r)
	if !ok {
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !s.limits.allow("password:"+p.AccountID, 10, 10*time.Minute) {
		writeError(w, 429, "RATE_LIMITED", "尝试太多，请 10 分钟后再试")
		return
	}
	err := s.accounts.ChangePassword(r.Context(), p, body.CurrentPassword, body.NewPassword)
	if errors.Is(err, account.ErrInvalid) {
		writeError(w, 400, "WRONG_PASSWORD", "原密码不正确")
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p, ok := s.signedIn(w, r)
	if !ok {
		return
	}
	me, err := s.accounts.Me(r.Context(), p)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, me)
}

// switchPerspective is only for demo accounts: it moves the session to another persona and returns a new access token.
func (s *Server) switchPerspective(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		Perspective string `json:"perspective"`
	}
	if !decode(w, r, &body) {
		return
	}
	tokens, err := s.accounts.SwitchPerspective(r.Context(), p, body.Perspective)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"access_token": tokens.AccessToken, "expires_in": tokens.ExpiresIn, "perspective": body.Perspective})
}
