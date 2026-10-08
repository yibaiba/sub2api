//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type passwordResetCacheStub struct {
	EmailCache
	mu           sync.Mutex
	stored       *PasswordResetTokenData
	consumeErr   error
	consumeCalls int
}

func (s *passwordResetCacheStub) GetPasswordResetToken(context.Context, string) (*PasswordResetTokenData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stored == nil {
		return nil, nil
	}
	copy := *s.stored
	return &copy, nil
}

func (s *passwordResetCacheStub) ConsumePasswordResetToken(_ context.Context, _ string, hash string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consumeCalls++
	if s.consumeErr != nil {
		return false, s.consumeErr
	}
	if s.stored == nil || s.stored.Token != hash {
		return false, nil
	}
	s.stored = nil
	return true, nil
}

type passwordResetUserRepoStub struct {
	UserRepository
	mu          sync.Mutex
	user        User
	used        map[string]bool
	writeErr    error
	getErr      error
	writes      int
	readBarrier chan struct{}
	readReady   chan struct{}
}

func (s *passwordResetUserRepoStub) GetByEmail(context.Context, string) (*User, error) {
	if s.readBarrier != nil {
		s.readReady <- struct{}{}
		<-s.readBarrier
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := s.user
	return &copy, s.getErr
}

func (s *passwordResetUserRepoStub) ResetPasswordOnce(_ context.Context, input PasswordResetUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	if s.used[input.TokenHash] {
		return ErrInvalidResetToken
	}
	s.used[input.TokenHash] = true
	s.user.PasswordHash = input.PasswordHash
	s.writes++
	return nil
}

func newPasswordResetTestService() (*AuthService, *passwordResetUserRepoStub, *passwordResetCacheStub) {
	repo := &passwordResetUserRepoStub{user: User{ID: 7, Email: "reset@test.com", Status: StatusActive, PasswordHash: "original"}, used: map[string]bool{}}
	cache := &passwordResetCacheStub{stored: &PasswordResetTokenData{Token: hashPasswordResetToken("test-token")}}
	svc := &AuthService{userRepo: repo, emailService: NewEmailService(nil, cache), settingService: &SettingService{settingRepo: &settingRepoStub{values: map[string]string{
		SettingKeyEmailVerifyEnabled: "true", SettingKeyPasswordResetEnabled: "true",
	}}}}
	return svc, repo, cache
}

func TestAuthResetPassword_FailedWritePreservesTokenForRetry(t *testing.T) {
	svc, repo, cache := newPasswordResetTestService()
	repo.writeErr = errors.New("database temporarily unavailable")
	require.ErrorIs(t, svc.ResetPassword(context.Background(), repo.user.Email, "test-token", "new-password"), ErrServiceUnavailable)
	require.Equal(t, "original", repo.user.PasswordHash)
	require.NotNil(t, cache.stored)
	require.Zero(t, cache.consumeCalls)

	repo.writeErr = nil
	require.NoError(t, svc.ResetPassword(context.Background(), repo.user.Email, "test-token", "new-password"))
	require.True(t, svc.CheckPassword("new-password", repo.user.PasswordHash))
	require.Nil(t, cache.stored)
	require.Equal(t, 1, repo.writes)
}

func TestAuthResetPassword_PreWriteFailuresPreserveToken(t *testing.T) {
	for _, failure := range []string{"user_read", "password_hash"} {
		t.Run(failure, func(t *testing.T) {
			svc, repo, cache := newPasswordResetTestService()
			password := "new-password"
			if failure == "user_read" {
				repo.getErr = errors.New("database unavailable")
			} else {
				password = strings.Repeat("a", 73) // bcrypt rejects passwords over 72 bytes.
			}
			require.Error(t, svc.ResetPassword(context.Background(), repo.user.Email, "test-token", password))
			require.NotNil(t, cache.stored)
			require.Zero(t, cache.consumeCalls)
			require.Zero(t, repo.writes)
		})
	}
}

func TestAuthResetPassword_ConcurrentUseChangesPasswordOnlyOnce(t *testing.T) {
	svc, repo, _ := newPasswordResetTestService()
	repo.readBarrier = make(chan struct{})
	repo.readReady = make(chan struct{}, 2)
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			results <- svc.ResetPassword(context.Background(), "reset@test.com", "test-token", "new-password")
		}()
	}
	<-repo.readReady
	<-repo.readReady
	close(repo.readBarrier)
	first, second := <-results, <-results
	if first == nil {
		require.ErrorIs(t, second, ErrInvalidResetToken)
	} else {
		require.ErrorIs(t, first, ErrInvalidResetToken)
		require.NoError(t, second)
	}
	require.Equal(t, 1, repo.writes)
}

func TestAuthResetPassword_RedisCleanupFailureCannotReuseToken(t *testing.T) {
	svc, repo, cache := newPasswordResetTestService()
	cache.consumeErr = errors.New("redis unavailable")
	require.NoError(t, svc.ResetPassword(context.Background(), repo.user.Email, "test-token", "new-password"))
	require.NotNil(t, cache.stored)
	require.ErrorIs(t, svc.ResetPassword(context.Background(), repo.user.Email, "test-token", "different-password"), ErrInvalidResetToken)
	require.True(t, svc.CheckPassword("new-password", repo.user.PasswordHash))
	require.Equal(t, 1, repo.writes)
}
