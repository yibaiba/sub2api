package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newAPIKeyLimitRepoMock(t *testing.T) (*apiKeyRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	return newAPIKeyRepositoryWithSQL(client, db), mock
}

func expectAPIKeyOwnerLock(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM "users" .* FOR UPDATE`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
}

func TestAPIKeyCreateWithLimit_CountsAfterOwnerLock(t *testing.T) {
	repo, mock := newAPIKeyLimitRepoMock(t)
	expectAPIKeyOwnerLock(mock)
	mock.ExpectQuery(`SELECT COUNT\(.* FROM "api_keys"`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectRollback()
	err := repo.CreateWithLimit(context.Background(), &service.APIKey{UserID: 7}, 1)
	require.ErrorIs(t, err, service.ErrAPIKeyCountExceeded)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAPIKeyCreateWithLimit_CommitsInsertInLockTransaction(t *testing.T) {
	repo, mock := newAPIKeyLimitRepoMock(t)
	expectAPIKeyOwnerLock(mock)
	mock.ExpectQuery(`SELECT COUNT\(.* FROM "api_keys"`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`INSERT INTO "api_keys" .*RETURNING "id"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(9))
	mock.ExpectCommit()
	key := &service.APIKey{UserID: 7, Key: "sk-test", Name: "test", Status: service.StatusActive}
	require.NoError(t, repo.CreateWithLimit(context.Background(), key, 1))
	require.Equal(t, int64(9), key.ID)
	require.WithinDuration(t, time.Now(), key.CreatedAt, time.Second)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAPIKeyCreateWithLimit_FailedCommitDoesNotPublishKey(t *testing.T) {
	repo, mock := newAPIKeyLimitRepoMock(t)
	expectAPIKeyOwnerLock(mock)
	mock.ExpectQuery(`SELECT COUNT\(.* FROM "api_keys"`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`INSERT INTO "api_keys" .*RETURNING "id"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(9))
	mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
	key := &service.APIKey{UserID: 7, Key: "sk-test", Name: "test", Status: service.StatusActive}
	require.Error(t, repo.CreateWithLimit(context.Background(), key, 1))
	require.Zero(t, key.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}
