//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/ent/apikey"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyCreateWithLimit_ConcurrentDatabaseCreates(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	owner := mustCreateAPIKeyRepoUser(t, ctx, client, fmt.Sprintf("key-limit-%d@test.com", time.Now().UnixNano()))
	t.Cleanup(func() {
		_, err := client.APIKey.Delete().Where(apikey.UserIDEQ(owner.ID)).Exec(ctx)
		require.NoError(t, err)
		require.NoError(t, client.User.DeleteOneID(owner.ID).Exec(ctx))
	})
	const attempts = 8
	start := make(chan struct{})
	results := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		go func(index int) {
			<-start
			// Separate repository instances model multiple server processes.
			repo := newAPIKeyRepositoryWithSQL(client, integrationDB)
			results <- repo.CreateWithLimit(ctx, &service.APIKey{
				UserID: owner.ID, Key: fmt.Sprintf("sk-limit-%d-%d", owner.ID, index), Name: "limit", Status: service.StatusActive,
			}, 1)
		}(i)
	}
	close(start)
	succeeded := 0
	for i := 0; i < attempts; i++ {
		if err := <-results; err == nil {
			succeeded++
		} else {
			require.ErrorIs(t, err, service.ErrAPIKeyCountExceeded)
		}
	}
	require.Equal(t, 1, succeeded)
	count, err := client.APIKey.Query().Where(apikey.UserIDEQ(owner.ID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestResetPasswordOnce_ConcurrentDatabaseWrites(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	owner := mustCreateAPIKeyRepoUser(t, ctx, client, fmt.Sprintf("reset-once-%d@test.com", time.Now().UnixNano()))
	input := service.PasswordResetUpdate{UserID: owner.ID, TokenHash: fmt.Sprintf("reset-token-%d", owner.ID), PasswordHash: "new-hash", ExpiresAt: time.Now().Add(time.Hour)}
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, "DELETE FROM idempotency_records WHERE scope = $1 AND idempotency_key_hash = $2", passwordResetScope, input.TokenHash)
		require.NoError(t, err)
		require.NoError(t, client.User.DeleteOneID(owner.ID).Exec(ctx))
	})
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			results <- newUserRepositoryWithSQL(client, integrationDB).ResetPasswordOnce(ctx, input)
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first == nil {
		require.ErrorIs(t, second, service.ErrInvalidResetToken)
	} else {
		require.ErrorIs(t, first, service.ErrInvalidResetToken)
		require.NoError(t, second)
	}
	stored, err := client.User.Query().Where(user.IDEQ(owner.ID)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, input.PasswordHash, stored.PasswordHash)
}
