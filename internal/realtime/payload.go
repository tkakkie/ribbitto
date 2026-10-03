package realtime

import (
	"encoding/hex"
	"errors"

	"github.com/tkakkie/ribbitto/internal/domain"
)

// FormatPayloadID returns id as canonical UUID text (8-4-4-4-12, lowercase),
// the form PostgreSQL's uuid type prints, so payloads keep one shape
// whichever writer stored them.
func FormatPayloadID(id domain.ID) string {
	var b [36]byte
	hex.Encode(b[0:8], id[0:4])
	hex.Encode(b[9:13], id[4:6])
	hex.Encode(b[14:18], id[6:8])
	hex.Encode(b[19:23], id[8:10])
	hex.Encode(b[24:36], id[10:16])
	b[8], b[13], b[18], b[23] = '-', '-', '-', '-'
	return string(b[:])
}

// ParsePayloadID parses a payload ID. Only canonical UUID text is accepted:
// PostgreSQL also reads other spellings, but stored payloads never use them,
// so another spelling means the payload is malformed.
func ParsePayloadID(value string) (domain.ID, error) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return domain.ID{}, errors.New("required ID is not a canonical UUID")
	}
	digits := value[0:8] + value[9:13] + value[14:18] + value[19:23] + value[24:36]
	var id domain.ID
	if _, err := hex.Decode(id[:], []byte(digits)); err != nil {
		return domain.ID{}, errors.New("required ID is not a canonical UUID")
	}
	return id, nil
}
