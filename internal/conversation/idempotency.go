package conversation

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
)

// TypedBodyProof records the normalized typed body's byte length (big-endian
// uint32) and SHA-256 digest. The zero value means no proof, as on legacy rows.
type TypedBodyProof [4 + sha256.Size]byte

func proveTypedBody(body string) TypedBodyProof {
	var proof TypedBodyProof
	binary.BigEndian.PutUint32(proof[:4], uint32(len(body)))
	digest := sha256.Sum256([]byte(body))
	copy(proof[4:], digest[:])
	return proof
}

// TypedBodyPrefix returns only a prefix verified against the original typed
// input. Stored member IDs cannot reconstruct handles after a rename.
func (m Message) TypedBodyPrefix(typed string) string {
	length := int(binary.BigEndian.Uint32(m.TypedBodyProof[:4]))
	if length <= 0 || length > len(typed) || proveTypedBody(typed[:length]) != m.TypedBodyProof {
		return ""
	}
	return typed[:length]
}

func (m Message) sameTypedBody(body string) bool {
	if m.TypedBodyProof != (TypedBodyProof{}) {
		return proveTypedBody(body) == m.TypedBodyProof
	}
	// A legacy reference does not prove the original typed body.
	// Literal bodies still allow exact comparison without a proof.
	return m.Body == body && (m.Format == FormatPlainText || len(appendReferences(nil, ParseChatV1(m.Body))) == 0)
}

// IdempotencyKey is a composer's key for one send (decision 37 (b)): a
// random UUID, its own type because kernel.ID values are UUIDv7 that
// PostgreSQL generates. It grants nothing; posting scopes it to the
// membership's organisation and author.
type IdempotencyKey [16]byte

// NewIdempotencyKey returns a random version-4 UUID. It is random only so
// that composers never collide by accident; it is not a secret.
func NewIdempotencyKey() IdempotencyKey {
	var k IdempotencyKey
	_, _ = rand.Read(k[:][:0]) // crypto/rand.Read never fails (Go 1.24).
	k[6] = k[6]&0x0f | 0x50
	k[8] = k[8]&0x3f | 0xc0
	return k
}

// String returns the canonical 36-character form composers render.
func (k IdempotencyKey) String() string {
	s := strings.ToUpper(hex.EncodeToString(k[:]))
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
