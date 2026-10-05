package postgres

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres/sqlcgen"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

// NewWriterForTest binds writes to db so schema tests can independently
// exercise invalid statements without aborting a shared transaction.
func NewWriterForTest(db sqlcgen.DBTX) Writer {
	return newWriter(db)
}

// NewReadStoreForTest binds schema-test reads to the same fixture database.
func NewReadStoreForTest(db sqlcgen.DBTX) ReadStore {
	return newReadStore(db)
}

// CreateDefaultChannelForTest allows schema tests to exercise the default flag
// with arbitrary names, independently of setup's transaction.
func (s *ChannelStore) CreateDefaultChannelForTest(ctx context.Context, organizationID kernel.ID, name string) (conversation.Channel, error) {
	return s.createChannel(ctx, organizationID, name, true)
}
