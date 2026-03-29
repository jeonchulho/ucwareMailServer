package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type User struct {
	Email        string
	PasswordHash string
	CreatedAt    time.Time
}

type AdminUser struct {
	Email          string
	PasswordHash   string
	Role           string
	TOTPSecret     string
	TOTPEnabled    bool
	OAuth2Provider string
	OAuth2Sub      string
	CreatedAt      time.Time
}

type TOTPChallenge struct {
	ID         string
	AdminEmail string
	ExpiresAt  time.Time
	Used       bool
}

type OAuth2State struct {
	State     string
	Provider  string
	ExpiresAt time.Time
}

type AuditLog struct {
	Action    string
	Actor     string
	Email     string
	Status    string
	Message   string
	RemoteIP  string
	UserAgent string
	CreatedAt time.Time
}

type RefreshToken struct {
	TokenHash  string
	AdminEmail string
	ExpiresAt  time.Time
	Revoked    bool
}

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	st := &SQLiteStore{db: db}
	if err := st.init(); err != nil {
		_ = db.Close()
		return nil, err
	}

	return st, nil
}

func (s *SQLiteStore) init() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			email TEXT PRIMARY KEY,
			password_hash TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)
	`)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS admin_users (
			email TEXT PRIMARY KEY,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)
	`)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS audit_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			action TEXT NOT NULL,
			actor_email TEXT NOT NULL,
			email TEXT NOT NULL,
			status TEXT NOT NULL,
			message TEXT NOT NULL,
			remote_ip TEXT NOT NULL,
			user_agent TEXT NOT NULL,
			created_at TEXT NOT NULL
		)
	`)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`ALTER TABLE audit_logs ADD COLUMN actor_email TEXT NOT NULL DEFAULT ''`)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return err
	}

	// admin_users 컬럼 추가 (마이그레이션 — 이미 있으면 무시)
	for _, col := range []string{
		`ALTER TABLE admin_users ADD COLUMN totp_secret TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE admin_users ADD COLUMN totp_enabled INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE admin_users ADD COLUMN oauth2_provider TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE admin_users ADD COLUMN oauth2_sub TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := s.db.Exec(col); err != nil {
			if !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
				return err
			}
		}
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS totp_challenges (
			id          TEXT PRIMARY KEY,
			admin_email TEXT NOT NULL,
			expires_at  TEXT NOT NULL,
			used        INTEGER NOT NULL DEFAULT 0
		)
	`)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS oauth2_states (
			state      TEXT PRIMARY KEY,
			provider   TEXT NOT NULL,
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL
		)
	`)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS refresh_tokens (
			token_hash  TEXT PRIMARY KEY,
			admin_email TEXT NOT NULL,
			expires_at  TEXT NOT NULL,
			revoked     INTEGER NOT NULL DEFAULT 0
		)
	`)
	if err != nil {
		return err
	}

	return nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func (s *SQLiteStore) UpsertUser(ctx context.Context, email, passwordHash string) (User, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users (email, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(email) DO UPDATE SET
			password_hash = excluded.password_hash,
			updated_at = excluded.updated_at
	`, email, passwordHash, now, now)
	if err != nil {
		return User{}, err
	}

	users, err := s.ListUsersByEmail(ctx, email)
	if err != nil {
		return User{}, err
	}
	if len(users) == 0 {
		return User{}, fmt.Errorf("user not found after upsert")
	}
	return users[0], nil
}

func (s *SQLiteStore) UpsertAdminUser(ctx context.Context, email, passwordHash, role string) (AdminUser, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO admin_users (email, password_hash, role, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(email) DO UPDATE SET
			password_hash = excluded.password_hash,
			role = excluded.role,
			updated_at = excluded.updated_at
	`, email, passwordHash, role, now, now)
	if err != nil {
		return AdminUser{}, err
	}

	return s.GetAdminUserByEmail(ctx, email)
}

func (s *SQLiteStore) GetAdminUserByEmail(ctx context.Context, email string) (AdminUser, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT email, password_hash, role, totp_secret, totp_enabled,
		       oauth2_provider, oauth2_sub, created_at
		FROM admin_users
		WHERE email = ?
	`, email)

	var out AdminUser
	var createdAt string
	var totpEnabled int
	if err := row.Scan(
		&out.Email, &out.PasswordHash, &out.Role,
		&out.TOTPSecret, &totpEnabled,
		&out.OAuth2Provider, &out.OAuth2Sub,
		&createdAt,
	); err != nil {
		return AdminUser{}, err
	}
	out.TOTPEnabled = totpEnabled != 0
	t, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return AdminUser{}, err
	}
	out.CreatedAt = t
	return out, nil
}

func (s *SQLiteStore) DeleteUser(ctx context.Context, email string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE email = ?`, email)
	return err
}

func (s *SQLiteStore) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT email, password_hash, created_at
		FROM users
		ORDER BY email ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		var createdAt string
		if err := rows.Scan(&u.Email, &u.PasswordHash, &createdAt); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, err
		}
		u.CreatedAt = t
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return users, nil
}

func (s *SQLiteStore) ListUsersByEmail(ctx context.Context, email string) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT email, password_hash, created_at
		FROM users
		WHERE email = ?
	`, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		var createdAt string
		if err := rows.Scan(&u.Email, &u.PasswordHash, &createdAt); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, err
		}
		u.CreatedAt = t
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return users, nil
}

func (s *SQLiteStore) InsertAuditLog(ctx context.Context, row AuditLog) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_logs (
			action, actor_email, email, status, message, remote_ip, user_agent, created_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, row.Action, row.Actor, row.Email, row.Status, row.Message, row.RemoteIP, row.UserAgent, now)
	return err
}

func (s *SQLiteStore) InsertRefreshToken(ctx context.Context, tokenHash, adminEmail string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO refresh_tokens (token_hash, admin_email, expires_at, revoked)
		VALUES (?, ?, ?, 0)
	`, tokenHash, adminEmail, expiresAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *SQLiteStore) GetRefreshToken(ctx context.Context, tokenHash string) (RefreshToken, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT token_hash, admin_email, expires_at, revoked
		FROM refresh_tokens
		WHERE token_hash = ?
	`, tokenHash)

	var rt RefreshToken
	var expiresAt string
	var revoked int
	if err := row.Scan(&rt.TokenHash, &rt.AdminEmail, &expiresAt, &revoked); err != nil {
		return RefreshToken{}, err
	}
	t, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return RefreshToken{}, err
	}
	rt.ExpiresAt = t
	rt.Revoked = revoked != 0
	return rt, nil
}

func (s *SQLiteStore) RevokeRefreshToken(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked = 1 WHERE token_hash = ?`, tokenHash)
	return err
}

func (s *SQLiteStore) PurgeExpiredRefreshTokens(ctx context.Context) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `DELETE FROM refresh_tokens WHERE expires_at < ?`, now)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return n, err
}

func (s *SQLiteStore) ListAdminUsers(ctx context.Context) ([]AdminUser, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT email, password_hash, role, totp_secret, totp_enabled,
		       oauth2_provider, oauth2_sub, created_at
		FROM admin_users
		ORDER BY email ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var admins []AdminUser
	for rows.Next() {
		var a AdminUser
		var createdAt string
		var totpEnabled int
		if err := rows.Scan(
			&a.Email, &a.PasswordHash, &a.Role,
			&a.TOTPSecret, &totpEnabled,
			&a.OAuth2Provider, &a.OAuth2Sub,
			&createdAt,
		); err != nil {
			return nil, err
		}
		a.TOTPEnabled = totpEnabled != 0
		t, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, err
		}
		a.CreatedAt = t
		admins = append(admins, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return admins, nil
}

func (s *SQLiteStore) DeleteAdminUser(ctx context.Context, email string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM admin_users WHERE email = ?`, email)
	return err
}

func (s *SQLiteStore) UpdateAdminUserRole(ctx context.Context, email, role string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`UPDATE admin_users SET role = ?, updated_at = ? WHERE email = ?`, role, now, email)
	return err
}

func (s *SQLiteStore) ChangeAdminPassword(ctx context.Context, email, passwordHash string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`UPDATE admin_users SET password_hash = ?, updated_at = ? WHERE email = ?`, passwordHash, now, email)
	return err
}

// ─── TOTP ────────────────────────────────────────────────────────────────────

func (s *SQLiteStore) SetTOTPSecret(ctx context.Context, email, secret string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`UPDATE admin_users SET totp_secret = ?, totp_enabled = 0, updated_at = ? WHERE email = ?`,
		secret, now, email)
	return err
}

func (s *SQLiteStore) EnableTOTP(ctx context.Context, email string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`UPDATE admin_users SET totp_enabled = 1, updated_at = ? WHERE email = ?`, now, email)
	return err
}

func (s *SQLiteStore) DisableTOTP(ctx context.Context, email string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`UPDATE admin_users SET totp_enabled = 0, totp_secret = '', updated_at = ? WHERE email = ?`, now, email)
	return err
}

func (s *SQLiteStore) InsertTOTPChallenge(ctx context.Context, id, adminEmail string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO totp_challenges (id, admin_email, expires_at, used) VALUES (?, ?, ?, 0)
	`, id, adminEmail, expiresAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *SQLiteStore) UseTOTPChallenge(ctx context.Context, id string) (TOTPChallenge, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, admin_email, expires_at, used FROM totp_challenges WHERE id = ?
	`, id)
	var c TOTPChallenge
	var expiresAt string
	var used int
	if err := row.Scan(&c.ID, &c.AdminEmail, &expiresAt, &used); err != nil {
		return TOTPChallenge{}, err
	}
	t, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return TOTPChallenge{}, err
	}
	c.ExpiresAt = t
	c.Used = used != 0
	if !c.Used {
		_, _ = s.db.ExecContext(ctx, `UPDATE totp_challenges SET used = 1 WHERE id = ?`, id)
	}
	return c, nil
}

func (s *SQLiteStore) PurgeExpiredTOTPChallenges(ctx context.Context) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `DELETE FROM totp_challenges WHERE expires_at < ?`, now)
	return err
}

// ─── OAuth2 State ────────────────────────────────────────────────────────────

func (s *SQLiteStore) InsertOAuth2State(ctx context.Context, state, provider string, expiresAt time.Time) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO oauth2_states (state, provider, created_at, expires_at) VALUES (?, ?, ?, ?)
	`, state, provider, now, expiresAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *SQLiteStore) PopOAuth2State(ctx context.Context, state string) (OAuth2State, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT state, provider, expires_at FROM oauth2_states WHERE state = ?
	`, state)
	var st OAuth2State
	var expiresAt string
	if err := row.Scan(&st.State, &st.Provider, &expiresAt); err != nil {
		return OAuth2State{}, err
	}
	t, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return OAuth2State{}, err
	}
	st.ExpiresAt = t
	_, _ = s.db.ExecContext(ctx, `DELETE FROM oauth2_states WHERE state = ?`, state)
	return st, nil
}

func (s *SQLiteStore) UpsertAdminUserByOAuth2(ctx context.Context, email, provider, sub, role string) (AdminUser, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO admin_users (email, password_hash, role, totp_secret, totp_enabled,
		                         oauth2_provider, oauth2_sub, created_at, updated_at)
		VALUES (?, '', ?, '', 0, ?, ?, ?, ?)
		ON CONFLICT(email) DO UPDATE SET
			oauth2_provider = excluded.oauth2_provider,
			oauth2_sub      = excluded.oauth2_sub,
			updated_at      = excluded.updated_at
	`, email, role, provider, sub, now, now)
	if err != nil {
		return AdminUser{}, err
	}
	return s.GetAdminUserByEmail(ctx, email)
}

func (s *SQLiteStore) ListAuditLogs(ctx context.Context, limit int) ([]AuditLog, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT action, actor_email, email, status, message, remote_ip, user_agent, created_at
		FROM audit_logs
		ORDER BY id DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AuditLog
	for rows.Next() {
		var item AuditLog
		var createdAt string
		if err := rows.Scan(
			&item.Action,
			&item.Actor,
			&item.Email,
			&item.Status,
			&item.Message,
			&item.RemoteIP,
			&item.UserAgent,
			&createdAt,
		); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, err
		}
		item.CreatedAt = t
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return out, nil
}
