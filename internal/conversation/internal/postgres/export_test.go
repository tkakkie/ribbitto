package postgres

import "github.com/tkakkie/ribbitto/internal/conversation/internal/postgres/sqlcgen"

// NewWriterForTest binds writes to db so schema tests can independently
// exercise invalid statements without aborting a shared transaction.
func NewWriterForTest(db sqlcgen.DBTX) Writer {
	return Writer{queries: sqlcgen.New(db), topics: NewTopicStore(db)}
}

// NewReadStoreForTest binds schema-test reads to the same fixture database.
func NewReadStoreForTest(db sqlcgen.DBTX) ReadStore {
	return ReadStore{channels: NewChannelStore(db), topics: NewTopicStore(db), queries: sqlcgen.New(db)}
}
