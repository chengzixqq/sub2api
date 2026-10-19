package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type adminAccessTokenRepository struct {
	db *sql.DB
}

func NewAdminAccessTokenRepository(db *sql.DB) service.AdminAccessTokenRepository {
	return &adminAccessTokenRepository{db: db}
}

const adminAccessTokenColumns = `id, user_id, name, token_hash, prefix, last4, scope, expires_at,
 last_used_at, COALESCE(last_used_ip, ''), revoked_at, created_at`

func scanAdminAccessToken(row rowScanner) (*service.AdminAccessToken, error) {
	var t service.AdminAccessToken
	var lastUsed, revoked sql.NullTime
	if err := row.Scan(&t.ID, &t.UserID, &t.Name, &t.TokenHash, &t.Prefix, &t.Last4, &t.Scope, &t.ExpiresAt,
		&lastUsed, &t.LastUsedIP, &revoked, &t.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrAdminAccessTokenNotFound
		}
		return nil, err
	}
	if lastUsed.Valid {
		at := lastUsed.Time
		t.LastUsedAt = &at
	}
	if revoked.Valid {
		at := revoked.Time
		t.RevokedAt = &at
	}
	return &t, nil
}

func (r *adminAccessTokenRepository) Create(ctx context.Context, t *service.AdminAccessToken) error {
	return r.db.QueryRowContext(ctx, `
INSERT INTO admin_access_tokens (user_id, name, token_hash, prefix, last4, scope, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, created_at`,
		t.UserID, t.Name, t.TokenHash, t.Prefix, t.Last4, t.Scope, t.ExpiresAt).Scan(&t.ID, &t.CreatedAt)
}

func (r *adminAccessTokenRepository) GetByHash(ctx context.Context, tokenHash string) (*service.AdminAccessToken, error) {
	return scanAdminAccessToken(r.db.QueryRowContext(ctx,
		`SELECT `+adminAccessTokenColumns+` FROM admin_access_tokens WHERE token_hash = $1`, tokenHash))
}

func (r *adminAccessTokenRepository) ListByUser(ctx context.Context, userID int64, now time.Time) ([]*service.AdminAccessToken, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+adminAccessTokenColumns+`
 FROM admin_access_tokens
 WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > $2
 ORDER BY created_at DESC, id DESC`, userID, now)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*service.AdminAccessToken{}
	for rows.Next() {
		t, err := scanAdminAccessToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *adminAccessTokenRepository) CountActiveByUser(ctx context.Context, userID int64, now time.Time) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_access_tokens
 WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > $2`, userID, now).Scan(&n)
	return n, err
}

func (r *adminAccessTokenRepository) Revoke(ctx context.Context, userID, id int64, now time.Time) (*service.AdminAccessToken, error) {
	return scanAdminAccessToken(r.db.QueryRowContext(ctx, `UPDATE admin_access_tokens SET revoked_at = $3
 WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL
 RETURNING `+adminAccessTokenColumns, id, userID, now))
}

func (r *adminAccessTokenRepository) TouchLastUsed(ctx context.Context, id int64, at time.Time, ip string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE admin_access_tokens SET last_used_at = $2, last_used_ip = NULLIF($3, '')
 WHERE id = $1`, id, at, truncateAccessTokenIP(ip, 64))
	return err
}

func truncateAccessTokenIP(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
