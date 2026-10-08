package service

import (
	"context"
	"time"
)

// PasswordResetUpdate contains only hashed credentials for the atomic DB write.
type PasswordResetUpdate struct {
	UserID       int64
	PasswordHash string
	TokenHash    string
	ExpiresAt    time.Time
}

// PasswordResetRepository commits the password and single-use token marker together.
// Failed writes roll back both, leaving the verified token usable for retry.
type PasswordResetRepository interface {
	ResetPasswordOnce(ctx context.Context, input PasswordResetUpdate) error
}
