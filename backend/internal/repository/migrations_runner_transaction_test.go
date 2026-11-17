package repository

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

const legacyWrappedMigrationName = "194_usage_log_billing_status.sql"

func TestTransactionalMigrationSQL(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
	}{
		{"plain", "SELECT 1;", "SELECT 1;"},
		{"wrapped", "BEGIN;\nSELECT 1;\nCOMMIT;", "SELECT 1;"},
		{"lowercase", "begin; SELECT 1; commit;", "SELECT 1;"},
		{"procedure", "DO $$ BEGIN PERFORM 1; END; $$;", "DO $$ BEGIN PERFORM 1; END; $$;"},
		{"wrapped procedure", "BEGIN; DO $$ BEGIN PERFORM 1; END; $$; COMMIT;", "DO $$ BEGIN PERFORM 1; END; $$;"},
		{"literal", "BEGIN; SELECT 'COMMIT;'; COMMIT;", "SELECT 'COMMIT;';"},
		{"unpaired begin", "BEGIN; SELECT 1;", "BEGIN; SELECT 1;"},
		{"unpaired commit", "SELECT 1; COMMIT;", "SELECT 1; COMMIT;"},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, transactionalMigrationSQL(tc.input))
		})
	}
}

func TestApplyMigrationsFS_LegacyTransactionWrapper(t *testing.T) {
	raw, err := migrations.FS.ReadFile(legacyWrappedMigrationName)
	require.NoError(t, err)
	content := strings.TrimSpace(string(raw))
	body := transactionalMigrationSQL(content)
	require.NotEqual(t, content, body)
	require.Contains(t, body, "DO $$\nBEGIN\n")

	for _, failure := range []string{"", "ddl", "record"} {
		t.Run("failure="+failure, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			prepareMigrationsBootstrapExpectations(mock)
			mock.ExpectQuery("SELECT checksum FROM schema_migrations WHERE filename = \\$1").
				WithArgs(legacyWrappedMigrationName).WillReturnError(sql.ErrNoRows)
			expectLegacyMigrationTransaction(mock, content, failure)
			mock.ExpectExec("SELECT pg_advisory_unlock\\(\\$1\\)").
				WithArgs(migrationsAdvisoryLockID).WillReturnResult(sqlmock.NewResult(0, 1))
			err = applyMigrationsFS(context.Background(), db, fstest.MapFS{
				legacyWrappedMigrationName: &fstest.MapFile{Data: raw},
			})
			if failure == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, failure+" failed")
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func expectLegacyMigrationTransaction(mock sqlmock.Sqlmock, content, failure string) {
	mock.ExpectBegin()
	ddl := mock.ExpectExec("^" + regexp.QuoteMeta(transactionalMigrationSQL(content)) + "$")
	if failure == "ddl" {
		ddl.WillReturnError(errors.New("ddl failed"))
	} else {
		ddl.WillReturnResult(sqlmock.NewResult(0, 0))
		record := mock.ExpectExec("INSERT INTO schema_migrations").
			WithArgs(legacyWrappedMigrationName, migrationChecksum(content))
		if failure == "record" {
			record.WillReturnError(errors.New("record failed"))
		} else {
			record.WillReturnResult(sqlmock.NewResult(0, 1))
		}
	}
	if failure == "" {
		mock.ExpectCommit()
	} else {
		mock.ExpectRollback()
	}
}
