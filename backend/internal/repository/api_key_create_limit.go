package repository

import (
	"context"
	"errors"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/apikey"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.APIKeyLimitedCreator = (*apiKeyRepository)(nil)

func (r *apiKeyRepository) CreateWithLimit(ctx context.Context, key *service.APIKey, maxActive int) error {
	if maxActive <= 0 {
		return r.Create(ctx, key)
	}
	var ownedTx *dbent.Tx
	client := clientFromContext(ctx, r.client)
	if dbent.TxFromContext(ctx) == nil {
		tx, err := r.client.Tx(ctx)
		switch {
		case errors.Is(err, dbent.ErrTxStarted):
			// A transaction-bound client is owned by the caller.
		case err != nil:
			return err
		default:
			ownedTx = tx
			defer func() { _ = tx.Rollback() }()
			ctx = dbent.NewTxContext(ctx, tx)
			client = tx.Client()
		}
	}
	// Lock the owner, not the existing keys: an empty key set must also serialize.
	if _, err := client.User.Query().Where(user.IDEQ(key.UserID), user.DeletedAtIsNil()).
		Select(user.FieldID).ForUpdate().Only(ctx); err != nil {
		return translatePersistenceError(err, service.ErrUserNotFound, nil)
	}
	count, err := client.APIKey.Query().Where(apikey.UserIDEQ(key.UserID), apikey.DeletedAtIsNil()).Count(ctx)
	if err != nil {
		return err
	}
	if count >= maxActive {
		return service.ErrAPIKeyCountExceeded
	}
	created := *key
	if err := r.Create(ctx, &created); err != nil {
		return err
	}
	if ownedTx != nil {
		if err := ownedTx.Commit(); err != nil {
			return err
		}
	}
	*key = created
	return nil
}
