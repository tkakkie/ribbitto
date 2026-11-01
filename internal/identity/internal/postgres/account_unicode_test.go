package postgres_test

import (
	"errors"
	"strconv"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"golang.org/x/text/unicode/norm"
)

// TestAccountChecksAcceptValidatedUnicode stores what ValidateEmail and
// ValidateDisplayName return for each code point after an "a", both as the
// code point and as its canonical decomposition (also with its first two
// marks swapped). The validators lower-case and compose with Go's Unicode
// tables, while account's CHECKs use PostgreSQL's lower and normalize, which
// follow the server's Unicode version and collation provider. A code point
// where they disagree turns a valid form into an unmapped 23514, a 500. The
// organisation, channel and topic name CHECKs pair the same Go NFC with the
// same normalize.
func TestAccountChecksAcceptValidatedUnicode(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	var emails, names []string
	for r := rune(0); r <= utf8.MaxRune; r++ {
		if !utf8.ValidRune(r) {
			continue
		}
		forms := []string{string(r)}
		if d := []rune(norm.NFD.String(string(r))); string(d) != string(r) {
			forms = append(forms, string(d))
			if len(d) >= 3 {
				d[1], d[2] = d[2], d[1]
				forms = append(forms, string(d))
			}
		}
		for _, form := range forms {
			if email, err := identity.ValidateEmail("a" + form + "@x"); err == nil {
				emails = append(emails, email)
			}
			if name, err := identity.ValidateDisplayName("a" + form); err == nil {
				names = append(names, name)
			}
		}
	}
	// A copy of account with its defaults and CHECKs but not its indexes
	// checks the hundreds of thousands of rows in seconds: comparing them
	// for the unique email index under the database's collation does not.
	_, err := pool.Exec(ctx, "CREATE TABLE account_probe (LIKE account INCLUDING DEFAULTS INCLUDING CONSTRAINTS)")
	requireNoError(t, err)
	for _, insert := range []struct {
		sql    string
		values []string
	}{
		{"INSERT INTO account_probe (email, display_name, password_hash) SELECT v, 'n', '$argon2id$test' FROM unnest($1::text[]) AS v", emails},
		{"INSERT INTO account_probe (email, display_name, password_hash) SELECT 'n@x', v, '$argon2id$test' FROM unnest($1::text[]) AS v", names},
	} {
		tag, err := pool.Exec(ctx, insert.sql, insert.values)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			t.Fatalf("a validated value fails %s: %s", pgErr.ConstraintName, strconv.QuoteToASCII(pgErr.Detail))
		}
		if err != nil || tag.RowsAffected() != int64(len(insert.values)) {
			t.Fatalf("inserted %d of %d values: %v", tag.RowsAffected(), len(insert.values), err)
		}
	}
}
