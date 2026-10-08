package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func passwordResetTestInput() service.PasswordResetUpdate {
	return service.PasswordResetUpdate{UserID: 42, PasswordHash: "bcrypt-hash", TokenHash: "token-hash", ExpiresAt: time.Now().Add(time.Hour)}
}

func expectResetTokenMarker(mock sqlmock.Sqlmock, input service.PasswordResetUpdate) *sqlmock.ExpectedExec {
	return mock.ExpectExec(`INSERT INTO idempotency_records .*ON CONFLICT .* DO NOTHING`).
		WithArgs(passwordResetScope, input.TokenHash, "42", service.IdempotencyStatusSucceeded, input.ExpiresAt)
}

func TestResetPasswordOnce_RollsBackTokenOnWriteFailureAndAllowsRetry(t *testing.T) {
	repo, mock := newRedeemAdjustmentRepoMock(t)
	input := passwordResetTestInput()
	mock.ExpectBegin()
	expectResetTokenMarker(mock, input).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE users SET password_hash = .*WHERE id = .*deleted_at IS NULL AND status =`).
		WithArgs(input.PasswordHash, input.UserID, service.StatusActive).WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()
	require.Error(t, repo.ResetPasswordOnce(context.Background(), input))

	mock.ExpectBegin()
	expectResetTokenMarker(mock, input).WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectExec(`UPDATE users SET password_hash = .*WHERE id = .*deleted_at IS NULL AND status =`).
		WithArgs(input.PasswordHash, input.UserID, service.StatusActive).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.ResetPasswordOnce(context.Background(), input))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResetPasswordOnce_RejectsUsedTokenWithoutChangingPassword(t *testing.T) {
	repo, mock := newRedeemAdjustmentRepoMock(t)
	input := passwordResetTestInput()
	mock.ExpectBegin()
	expectResetTokenMarker(mock, input).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	require.ErrorIs(t, repo.ResetPasswordOnce(context.Background(), input), service.ErrInvalidResetToken)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResetPasswordOnce_RollsBackMarkerForMissingOrDisabledUser(t *testing.T) {
	repo, mock := newRedeemAdjustmentRepoMock(t)
	input := passwordResetTestInput()
	mock.ExpectBegin()
	expectResetTokenMarker(mock, input).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE users SET password_hash =`).
		WithArgs(input.PasswordHash, input.UserID, service.StatusActive).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	require.ErrorIs(t, repo.ResetPasswordOnce(context.Background(), input), service.ErrInvalidResetToken)
	require.NoError(t, mock.ExpectationsWereMet())
}
