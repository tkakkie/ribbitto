package realtime

import (
	"encoding/hex"

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
