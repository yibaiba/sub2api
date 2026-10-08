package repository

import (
	"context"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const passwordResetScope = "auth.password_reset"

var _ service.PasswordResetRepository = (*userRepository)(nil)

func (r *userRepository) ResetPasswordOnce(ctx context.Context, input service.PasswordResetUpdate) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	// The existing idempotency table provides a durable single-use marker, even
	// if Redis cleanup fails or the process exits immediately after commit.
	result, err := client.ExecContext(ctx, `
		INSERT INTO idempotency_records (scope, idempotency_key_hash, request_fingerprint, status, expires_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
		ON CONFLICT (scope, idempotency_key_hash) DO NOTHING`,
		passwordResetScope, input.TokenHash, strconv.FormatInt(input.UserID, 10), service.IdempotencyStatusSucceeded, input.ExpiresAt)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return service.ErrInvalidResetToken
	}
	result, err = client.ExecContext(ctx, `
		UPDATE users SET password_hash = $1, updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL AND status = $3`, input.PasswordHash, input.UserID, service.StatusActive)
	if err != nil {
		return err
	}
	count, err = result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return service.ErrInvalidResetToken
	}
	return tx.Commit()
}
