package web

import (
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// TestPathID pins what pathID refuses. Handler tests reach it only with
// short or non-hex IDs; a moved hyphen is refused only by the position check,
// and an appended one only by the length check, since both still leave 32 hex
// digits for the decode.
func TestPathID(t *testing.T) {
	t.Parallel()
	const valid = "0123abcd-4567-89ef-0123-456789abcdef"
	want := kernel.ID{0x01, 0x23, 0xab, 0xcd, 0x45, 0x67, 0x89, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}
	id, ok := pathID(valid)
	if !ok || id != want {
		t.Fatalf("pathID(%q) = %x, %t; want %x, true", valid, id, ok, want)
	}
	for name, raw := range map[string]string{
		"35 characters":    valid[:35],
		"37 characters":    valid + "0",
		"appended hyphen":  valid + "-",
		"moved hyphen":     "0123abc-d4567-89ef-0123-456789abcdef",
		"non-hex":          "0123abcd-4567-89ef-0123-456789abcdeg",
		"braces":           "{" + valid + "}",
		"urn:uuid: prefix": "urn:uuid:" + valid,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if id, ok := pathID(raw); ok || id != (kernel.ID{}) {
				t.Fatalf("pathID(%q) = %x, %t; want zero ID, false", raw, id, ok)
			}
		})
	}
}
