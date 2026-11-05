// Package workspace gives every signed-in account its own WorkSwarm container on agent.hrtiger.cn
// (docs/plan/02-backend.md §10). The server never touches Docker: it asks the broker over a local socket.
// nginx forwards each request on agent.hrtiger.cn to the container of whoever holds the forwarding cookie;
// that cookie is tied to the login session, so ending the session ends the forwarding.
package workspace

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"edu-agent-jiuwen-server/internal/account"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Broker starts and stops containers. Ensure returns once the container answers on its port.
type Broker interface {
	Ensure(ctx context.Context, accountID string, port int) error
	Stop(ctx context.Context, accountID string, port int) error
}

type Config struct {
	FirstPort, LastPort int           // host ports handed out to accounts, one each, for good
	Limit               int           // containers starting or running at once; beyond that people wait
	Idle                time.Duration // stop a container nobody has used for this long
	StartTimeout        time.Duration // a start still pending after this is treated as failed
	Starts              int           // containers starting at the same time
}

type Service struct {
	db     *pgxpool.Pool
	broker Broker
	config Config
	starts chan struct{}
}

func New(db *pgxpool.Pool, broker Broker, config Config) *Service {
	return &Service{db: db, broker: broker, config: config, starts: make(chan struct{}, max(config.Starts, 1))}
}

// States reported to the browser.
const (
	Starting = "starting"
	Ready    = "ready"
	Busy     = "busy" // the limit is reached; ask again later
)

var ErrUnavailable = errors.New("workspace unavailable")

// Open makes sure the account's container is running or starting, and ties a forwarding cookie to the session.
// It returns a new cookie value when the presented one does not belong to this session.
func (s *Service) Open(ctx context.Context, p account.Principal, cookie string) (string, string, error) {
	issued := ""
	var current bool
	if cookie != "" {
		if err := s.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM sessions WHERE id=$1 AND workspace_hash=$2)", p.SessionID, account.Hash(cookie)).Scan(&current); err != nil {
			return "", "", err
		}
	}
	if !current {
		issued = "ews_" + rand.Text() + rand.Text()
		if _, err := s.db.Exec(ctx, "UPDATE sessions SET workspace_hash=$1 WHERE id=$2", account.Hash(issued), p.SessionID); err != nil {
			return "", "", err
		}
	}
	// 演示账号切视角时用的还是登录账号本人的容器。
	state, err := s.claim(ctx, p.LoginAccountID)
	return state, issued, err
}

// claim moves the account's workspace towards running and reports where it stands.
func (s *Service) claim(ctx context.Context, accountID string) (string, error) {
	if err := s.assignPort(ctx, accountID); err != nil {
		return "", err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	// 串行判断名额：两个人同时进来不会一起超过上限。
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(4020002)"); err != nil {
		return "", err
	}
	var port int
	var state string
	var stale bool
	if err := tx.QueryRow(ctx, "SELECT port,state,updated_at<now()-make_interval(secs=>$2) FROM workspaces WHERE account_id=$1",
		accountID, s.config.StartTimeout.Seconds()).Scan(&port, &state, &stale); err != nil {
		return "", err
	}
	switch {
	case state == Ready:
		_, err := tx.Exec(ctx, "UPDATE workspaces SET last_seen_at=now() WHERE account_id=$1", accountID)
		if err != nil {
			return "", err
		}
		return Ready, tx.Commit(ctx)
	case state == Starting && !stale:
		return Starting, nil
	}
	var active int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM workspaces WHERE state IN ('starting','ready') AND account_id<>$1", accountID).Scan(&active); err != nil {
		return "", err
	}
	if active >= s.config.Limit {
		return Busy, nil
	}
	if _, err := tx.Exec(ctx, "UPDATE workspaces SET state='starting',started_at=now(),last_seen_at=now(),updated_at=now() WHERE account_id=$1", accountID); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	go s.start(accountID, port)
	return Starting, nil
}

// assignPort gives the account a host port the first time it opens a workspace; the port stays its own.
func (s *Service) assignPort(ctx context.Context, accountID string) error {
	for range 5 {
		tag, err := s.db.Exec(ctx, `INSERT INTO workspaces(account_id,port)
			SELECT $1,p FROM generate_series($2::int,$3::int) p WHERE NOT EXISTS (SELECT 1 FROM workspaces w WHERE w.port=p) LIMIT 1
			ON CONFLICT (account_id) DO NOTHING`, accountID, s.config.FirstPort, s.config.LastPort)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			continue // 另一个账号同时拿到了同一个端口，换一个
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		var exists bool
		if err := s.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM workspaces WHERE account_id=$1)", accountID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		return fmt.Errorf("%w: no free workspace port", ErrUnavailable)
	}
	return fmt.Errorf("%w: could not assign a workspace port", ErrUnavailable)
}

func (s *Service) start(accountID string, port int) {
	s.starts <- struct{}{}
	defer func() { <-s.starts }()
	ctx, cancel := context.WithTimeout(context.Background(), s.config.StartTimeout)
	defer cancel()
	state := Ready
	if err := s.broker.Ensure(ctx, accountID, port); err != nil {
		slog.Error("workspace start failed", "service", "edu-agent-jiuwen-server", "account", accountID, "error", err.Error())
		state = "failed"
	}
	// 只有还在启动中才改：启动期间账号被停用、容器被停掉的，不能又变回可用。
	if _, err := s.db.Exec(context.Background(), "UPDATE workspaces SET state=$2,last_seen_at=now(),updated_at=now() WHERE account_id=$1 AND state='starting'", accountID, state); err != nil {
		slog.Error("workspace state not saved", "service", "edu-agent-jiuwen-server", "account", accountID, "error", err.Error())
	}
}

// Upstream answers nginx's auth_request: the container address for a forwarding cookie whose session is still
// valid and whose workspace is running. Use counts as activity for the idle stop.
func (s *Service) Upstream(ctx context.Context, cookie string) (string, error) {
	if len(cookie) < 32 || len(cookie) > 128 {
		return "", account.ErrInvalid
	}
	var port int
	err := s.db.QueryRow(ctx, `SELECT w.port FROM sessions s JOIN accounts a ON a.id=s.account_id JOIN workspaces w ON w.account_id=s.account_id
		WHERE s.workspace_hash=$1 AND s.revoked_at IS NULL AND s.expires_at>now() AND a.status='active' AND w.state='ready'`, account.Hash(cookie)).Scan(&port)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", account.ErrInvalid
	}
	if err != nil {
		return "", err
	}
	if _, err := s.db.Exec(ctx, "UPDATE workspaces SET last_seen_at=now() WHERE port=$1 AND last_seen_at<now()-interval '1 minute'", port); err != nil {
		return "", err
	}
	return fmt.Sprintf("127.0.0.1:%d", port), nil
}

// StopAccount stops an account's container right away, so open WebSocket connections end too.
// Disabling an account and resetting its password call this; nginx checks only at connection time.
func (s *Service) StopAccount(ctx context.Context, accountID string) error {
	var port int
	err := s.db.QueryRow(ctx, "UPDATE workspaces SET state='stopped',updated_at=now() WHERE account_id=$1 AND state<>'stopped' RETURNING port", accountID).Scan(&port)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.broker.Stop(ctx, accountID, port)
}

// Reap stops containers idle longer than Config.Idle; their data stays for the next sign-in.
func (s *Service) Reap(ctx context.Context) error {
	rows, err := s.db.Query(ctx, "SELECT account_id FROM workspaces WHERE state IN ('ready','failed') AND last_seen_at<now()-make_interval(secs=>$1)", s.config.Idle.Seconds())
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.StopAccount(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// Run stops idle containers every minute until ctx ends. Starts cut short by a restart are retried on next open.
func (s *Service) Run(ctx context.Context) {
	if _, err := s.db.Exec(ctx, "UPDATE workspaces SET state='failed',updated_at=now() WHERE state='starting'"); err != nil {
		slog.Error("workspace recovery failed", "service", "edu-agent-jiuwen-server", "error", err.Error())
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Reap(ctx); err != nil {
				slog.Error("idle workspace stop failed", "service", "edu-agent-jiuwen-server", "error", err.Error())
			}
		}
	}
}
