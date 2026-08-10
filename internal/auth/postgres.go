package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct{ pool *pgxpool.Pool }

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

func (r *PostgresRepository) Register(ctx context.Context, email, passwordHash, refreshHash string, expiresAt time.Time) (User, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var user User
	err = tx.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ($1, $2)
		RETURNING id::text, email, role, status, created_at, updated_at`, email, passwordHash).
		Scan(&user.ID, &user.Email, &user.Role, &user.Status, &user.CreatedAt, &user.UpdatedAt)
	if isConstraint(err, "users_email_unique") {
		return User{}, ErrEmailExists
	}
	if err != nil {
		return User{}, fmt.Errorf("insert user: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`, user.ID, refreshHash, expiresAt); err != nil {
		return User{}, fmt.Errorf("insert registration refresh token: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("commit registration: %w", err)
	}
	return user, nil
}

func (r *PostgresRepository) FindByEmail(ctx context.Context, email string) (StoredUser, error) {
	var user StoredUser
	err := r.pool.QueryRow(ctx, `SELECT id::text, email, password_hash, role, status, created_at, updated_at
		FROM users WHERE lower(email) = lower($1)`, email).
		Scan(&user.ID, &user.Email, &user.PasswordHash, &user.Role, &user.Status, &user.CreatedAt, &user.UpdatedAt)
	return user, err
}

func (r *PostgresRepository) FindByID(ctx context.Context, id string) (User, error) {
	var user User
	err := r.pool.QueryRow(ctx, `SELECT id::text, email, role, status, created_at, updated_at FROM users WHERE id = $1`, id).
		Scan(&user.ID, &user.Email, &user.Role, &user.Status, &user.CreatedAt, &user.UpdatedAt)
	return user, err
}

func (r *PostgresRepository) StoreRefreshToken(ctx context.Context, userID, tokenHash string, expiresAt time.Time) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`, userID, tokenHash, expiresAt)
	return err
}

func (r *PostgresRepository) RotateRefreshToken(ctx context.Context, oldHash, newHash string, expiresAt, now time.Time) (User, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var user User
	var tokenID string
	err = tx.QueryRow(ctx, `SELECT rt.id::text, u.id::text, u.email, u.role, u.status, u.created_at, u.updated_at
		FROM refresh_tokens rt JOIN users u ON u.id = rt.user_id
		WHERE rt.token_hash = $1 AND rt.revoked_at IS NULL AND rt.expires_at > $2 FOR UPDATE`, oldHash, now).
		Scan(&tokenID, &user.ID, &user.Email, &user.Role, &user.Status, &user.CreatedAt, &user.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrRefreshUnavailable
	}
	if err != nil {
		return User{}, err
	}
	if user.Status != StatusActive {
		_, _ = tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL`, user.ID, now)
		if err := tx.Commit(ctx); err != nil {
			return User{}, err
		}
		return user, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = $2 WHERE id = $1`, tokenID, now); err != nil {
		return User{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`, user.ID, newHash, expiresAt); err != nil {
		return User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, err
	}
	return user, nil
}

func (r *PostgresRepository) RevokeRefreshToken(ctx context.Context, tokenHash string, now time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = $2 WHERE token_hash = $1 AND revoked_at IS NULL`, tokenHash, now)
	return err
}

func isConstraint(err error, name string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == name
}
