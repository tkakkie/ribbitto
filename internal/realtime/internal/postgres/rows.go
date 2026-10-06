package postgres

import (
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/kernel"
)

// uuid is this store's own copy; modules share no store code (decision 26).
func uuid(id kernel.ID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }
