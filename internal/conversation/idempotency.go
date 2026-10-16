package conversation

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
)

// IdempotencyKey is a composer's key for one send (decision 37 (b)): a
// random UUID, its own type because kernel.ID values are UUIDv7 that
// PostgreSQL generates. It grants nothing; posting scopes it to the
// membership's organisation and author.
type IdempotencyKey [16]byte

// NewIdempotencyKey returns a random version-4 UUID. It is random only so
// that composers never collide by accident; it is not a secret.
func NewIdempotencyKey() IdempotencyKey {
	var k IdempotencyKey
	_, _ = rand.Read(k[:]) // crypto/rand.Read never fails (Go 1.24).
	k[6] = k[6]&0x0f | 0x40
	k[8] = k[8]&0x3f | 0x80
	return k
}

// String returns the canonical 36-character form composers render.
func (k IdempotencyKey) String() string {
	s := hex.EncodeToString(k[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

var (
	// ErrPostKeyReused means the author already sent another body or channel
	// under the key; Send returns that stored message with it and writes nothing.
	ErrPostKeyReused = errors.New("idempotency key reused for another message")
	// ErrPostKeyTaken is Writer.InsertMessage's signal that the author's key
	// is already stored; Send turns it into a repeat and never returns it.
	ErrPostKeyTaken = errors.New("idempotency key already stored")
)
