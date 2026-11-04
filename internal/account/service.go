// Package account owns accounts, passwords, sessions and roles. Identity always comes from a session, never a request body.
package account

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"edu-agent-jiuwen-server/internal/platform"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInvalid   = errors.New("invalid or expired credentials")
	ErrRejected  = errors.New("rejected")
	ErrForbidden = errors.New("forbidden")
	ErrConflict  = errors.New("conflict")
	ErrNotFound  = errors.New("not found")
)

const (
	AccessTTL = 15 * time.Minute
	// 会话从登录起算 30 天，刷新不顺延；有运营或学校管理员角色的账号在桌面版只给 12 小时（docs/plan/02-backend.md §5）。
	SessionTTL           = 30 * 24 * time.Hour
	PrivilegedDesktopTTL = 12 * time.Hour
)

var loginPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,63}$`)

type Service struct{ db *pgxpool.Pool }

func New(db *pgxpool.Pool) *Service { return &Service{db: db} }

func Hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func token(prefix string) string { return prefix + "_" + rand.Text() + rand.Text() }

func NormalizeLogin(value string) (string, error) {
	login := strings.ToLower(strings.TrimSpace(value))
	if !loginPattern.MatchString(login) {
		return "", fmt.Errorf("%w: 登录名只能用小写字母、数字和 . _ -，长度 3 到 64", ErrRejected)
	}
	return login, nil
}

type Client struct{ App, Version, UserAgent string }

type Tokens struct {
	AccessToken        string    `json:"access_token"`
	ExpiresIn          int       `json:"expires_in"`
	RefreshToken       string    `json:"refresh_token,omitempty"`
	MustChangePassword bool      `json:"must_change_password"`
	RefreshExpiresAt   time.Time `json:"-"`
}

// Login checks the password and opens a session. Unknown accounts take as long as wrong passwords.
func (s *Service) Login(ctx context.Context, login, password string, client Client) (Tokens, error) {
	if client.App != "web" && client.App != "desktop" {
		return Tokens{}, fmt.Errorf("%w: client_app 只能是 web 或 desktop", ErrRejected)
	}
	normalized, err := NormalizeLogin(login)
	if err != nil {
		checkPassword(dummyHash, password)
		return Tokens{}, ErrInvalid
	}
	var id, stored, status string
	var mustChange bool
	err = s.db.QueryRow(ctx, "SELECT id,password_hash,status,must_change_password FROM accounts WHERE login=$1", normalized).Scan(&id, &stored, &status, &mustChange)
	if errors.Is(err, pgx.ErrNoRows) {
		checkPassword(dummyHash, password)
		return Tokens{}, ErrInvalid
	}
	if err != nil {
		return Tokens{}, err
	}
	if !checkPassword(stored, password) || status != "active" {
		return Tokens{}, ErrInvalid
	}
	ttl := SessionTTL
	if client.App == "desktop" {
		privileged, err := s.privileged(ctx, id)
		if err != nil {
			return Tokens{}, err
		}
		if privileged {
			ttl = PrivilegedDesktopTTL
		}
	}
	now := time.Now()
	tokens := Tokens{AccessToken: token("eat"), RefreshToken: token("ert"), ExpiresIn: int(AccessTTL.Seconds()), MustChangePassword: mustChange, RefreshExpiresAt: now.Add(ttl)}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Tokens{}, err
	}
	defer tx.Rollback(ctx)
	sessionID := platform.ID("ses")
	if _, err := tx.Exec(ctx, "INSERT INTO sessions(id,account_id,client_app,client_version,user_agent,access_hash,access_expires_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)",
		sessionID, id, client.App, limit(client.Version, 32), limit(client.UserAgent, 256), Hash(tokens.AccessToken), now.Add(AccessTTL), tokens.RefreshExpiresAt); err != nil {
		return Tokens{}, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO refresh_tokens(token_hash,session_id,generation,expires_at) VALUES($1,$2,0,$3)", Hash(tokens.RefreshToken), sessionID, tokens.RefreshExpiresAt); err != nil {
		return Tokens{}, err
	}
	return tokens, tx.Commit(ctx)
}

func limit(value string, n int) string {
	if len(value) > n {
		return value[:n]
	}
	return value
}

func (s *Service) privileged(ctx context.Context, accountID string) (bool, error) {
	var privileged bool
	err := s.db.QueryRow(ctx, "SELECT is_operator OR EXISTS (SELECT 1 FROM school_members WHERE account_id=$1 AND role='school_admin') FROM accounts WHERE id=$1", accountID).Scan(&privileged)
	return privileged, err
}

// Refresh rotates the refresh token; the session keeps the expiry it got at sign-in. A refresh token that was already used revokes the whole session,
// because only a copy held by someone else can still present it.
func (s *Service) Refresh(ctx context.Context, raw string) (Tokens, error) {
	if len(raw) < 32 || len(raw) > 128 {
		return Tokens{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Tokens{}, err
	}
	defer tx.Rollback(ctx)
	var sessionID, status string
	var generation, current int
	var tokenExpires, sessionExpires time.Time
	var usedAt, revokedAt *time.Time
	var mustChange bool
	err = tx.QueryRow(ctx, `SELECT r.session_id,r.generation,r.expires_at,r.used_at,s.generation,s.expires_at,s.revoked_at,a.status,a.must_change_password
		FROM refresh_tokens r JOIN sessions s ON s.id=r.session_id JOIN accounts a ON a.id=s.account_id
		WHERE r.token_hash=$1 FOR UPDATE OF r,s`, Hash(raw)).
		Scan(&sessionID, &generation, &tokenExpires, &usedAt, &current, &sessionExpires, &revokedAt, &status, &mustChange)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tokens{}, ErrInvalid
	}
	if err != nil {
		return Tokens{}, err
	}
	now := time.Now()
	if usedAt != nil || generation != current {
		if _, err := tx.Exec(ctx, "UPDATE sessions SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1", sessionID); err != nil {
			return Tokens{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return Tokens{}, err
		}
		return Tokens{}, ErrInvalid
	}
	if revokedAt != nil || status != "active" || !now.Before(tokenExpires) || !now.Before(sessionExpires) {
		return Tokens{}, ErrInvalid
	}
	tokens := Tokens{AccessToken: token("eat"), RefreshToken: token("ert"), ExpiresIn: int(AccessTTL.Seconds()), MustChangePassword: mustChange, RefreshExpiresAt: sessionExpires}
	if _, err := tx.Exec(ctx, "UPDATE sessions SET access_hash=$1,access_expires_at=$2,generation=generation+1 WHERE id=$3", Hash(tokens.AccessToken), now.Add(AccessTTL), sessionID); err != nil {
		return Tokens{}, err
	}
	if _, err := tx.Exec(ctx, "UPDATE refresh_tokens SET used_at=$1 WHERE token_hash=$2", now, Hash(raw)); err != nil {
		return Tokens{}, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO refresh_tokens(token_hash,session_id,generation,expires_at) VALUES($1,$2,$3,$4)", Hash(tokens.RefreshToken), sessionID, generation+1, sessionExpires); err != nil {
		return Tokens{}, err
	}
	return tokens, tx.Commit(ctx)
}

type Principal struct {
	AccountID, SessionID, ClientApp string
	MustChangePassword              bool
}

func (s *Service) Authenticate(ctx context.Context, raw string) (Principal, error) {
	var p Principal
	if len(raw) < 32 || len(raw) > 128 {
		return p, ErrInvalid
	}
	err := s.db.QueryRow(ctx, `SELECT s.account_id,s.id,s.client_app,a.must_change_password FROM sessions s JOIN accounts a ON a.id=s.account_id
		WHERE s.access_hash=$1 AND s.revoked_at IS NULL AND s.access_expires_at>now() AND s.expires_at>now() AND a.status='active'`, Hash(raw)).
		Scan(&p.AccountID, &p.SessionID, &p.ClientApp, &p.MustChangePassword)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrInvalid
	}
	return p, err
}

func (s *Service) Logout(ctx context.Context, sessionID string) error {
	_, err := s.db.Exec(ctx, "UPDATE sessions SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1", sessionID)
	return err
}

// LogoutRefresh ends the session a refresh token belongs to; the web client only holds the refresh cookie.
func (s *Service) LogoutRefresh(ctx context.Context, raw string) error {
	_, err := s.db.Exec(ctx, "UPDATE sessions SET revoked_at=COALESCE(revoked_at,now()) WHERE id=(SELECT session_id FROM refresh_tokens WHERE token_hash=$1)", Hash(raw))
	return err
}

// ChangePassword keeps the current session and ends every other one.
func (s *Service) ChangePassword(ctx context.Context, p Principal, current, next string) error {
	if err := ValidPassword(next); err != nil {
		return err
	}
	if current == next {
		return fmt.Errorf("%w: 新密码不能和原密码相同", ErrRejected)
	}
	var stored string
	if err := s.db.QueryRow(ctx, "SELECT password_hash FROM accounts WHERE id=$1", p.AccountID).Scan(&stored); err != nil {
		return err
	}
	if !checkPassword(stored, current) {
		return ErrInvalid
	}
	hash, err := HashPassword(next)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "UPDATE accounts SET password_hash=$1,must_change_password=false,password_changed_at=now(),updated_at=now() WHERE id=$2", hash, p.AccountID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE sessions SET revoked_at=now() WHERE account_id=$1 AND id<>$2 AND revoked_at IS NULL", p.AccountID, p.SessionID); err != nil {
		return err
	}
	if err := audit(ctx, tx, p.AccountID, "account.password_changed", p.AccountID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func audit(ctx context.Context, tx pgx.Tx, accountID, action, target string) error {
	_, err := tx.Exec(ctx, "INSERT INTO audit_events(id,account_id,action,target) VALUES($1,$2,$3,$4)", platform.ID("aud"), accountID, action, target)
	return err
}
