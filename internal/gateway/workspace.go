package gateway

import (
	"log/slog"
	"net/http"
	"time"

	"edu-agent-jiuwen-server/internal/workspace"
)

// The forwarding cookie belongs to agent.hrtiger.cn: the page calls POST /v1/workspace through its own origin
// (nginx there proxies /v1/workspace here), so the cookie is host-only on the page's host.
const workspaceCookie = "edu_workspace"

// WithWorkspaces turns on per-account containers; without it POST /v1/workspace answers 503.
func (s *Server) WithWorkspaces(w *workspace.Service) { s.workspaces = w }

func (s *Server) registerWorkspace() {
	s.route("POST /v1/workspace", s.openWorkspace)
}

func (s *Server) openWorkspace(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	if s.workspaces == nil {
		writeError(w, 503, "WORKSPACE_DISABLED", "个人实训环境还没有开通")
		return
	}
	presented := ""
	if cookie, err := r.Cookie(workspaceCookie); err == nil {
		presented = cookie.Value
	}
	state, issued, err := s.workspaces.Open(r.Context(), p, presented)
	if err != nil {
		fail(w, r, err)
		return
	}
	if issued != "" {
		var expires time.Time
		if err := s.db.QueryRow(r.Context(), "SELECT expires_at FROM sessions WHERE id=$1", p.SessionID).Scan(&expires); err != nil {
			fail(w, r, err)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: workspaceCookie, Value: issued, Path: "/", Expires: expires,
			HttpOnly: true, Secure: s.config.SecureCookies(), SameSite: http.SameSiteLaxMode})
	}
	// cookie_issued: the page was served before this browser could reach its container, so reload once ready.
	writeJSON(w, http.StatusOK, map[string]any{"state": state, "cookie_issued": issued != ""})
}

// stopWorkspace ends an account's container after its sessions were revoked, so open connections drop too.
func (s *Server) stopWorkspace(r *http.Request, accountID string) {
	if s.workspaces == nil {
		return
	}
	if err := s.workspaces.StopAccount(r.Context(), accountID); err != nil {
		slog.Error("workspace stop failed", "service", "edu-agent-jiuwen-server", "account", accountID, "error", err.Error())
	}
}

// Internal serves nginx's auth_request on a loopback-only listener, apart from the public API port,
// so nothing reachable from outside can ask for a container address.
func (s *Server) Internal() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/workspace/auth", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		cookie, err := r.Cookie(workspaceCookie)
		if err != nil || s.workspaces == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		upstream, err := s.workspaces.Upstream(r.Context(), cookie.Value)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-Workspace-Upstream", upstream)
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}
