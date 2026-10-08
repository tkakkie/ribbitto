package orgpg

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/internal/postgres"
)

// NewTxRunnerForTest lets external flow tests inject writers into the wiring's runner.
func NewTxRunnerForTest(pool *pgxpool.Pool) org.TxRunner { return newTxRunner(pool) }

// NewWrappedAuthorizerForTest gates real store reads without changing delivery.
func NewWrappedAuthorizerForTest(ctx context.Context, pool *pgxpool.Pool, wrap func(org.MembershipStore) org.MembershipStore) *org.Authorizer {
	return org.NewCachedAuthorizer(ctx, wrap(postgres.NewAuthzStore(pool)), 10)
}
