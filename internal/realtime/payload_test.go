package realtime

import (
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestPayloadID(t *testing.T) {
	const canonical = "01234567-89ab-cdef-0123-456789abcdef"
	id := kernel.ID{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}
	for _, tt := range []struct {
		name    string
		text    string
		want    kernel.ID
		format  string
		wantErr bool
	}{
		{name: "canonical", text: canonical, want: id, format: canonical},
		{name: "upper-case hex", text: "01234567-89AB-CDEF-0123-456789ABCDEF", want: id, format: canonical},
		{name: "zero", text: "00000000-0000-0000-0000-000000000000", format: "00000000-0000-0000-0000-000000000000"},
		{name: "empty", text: "", wantErr: true},
		{name: "no dashes", text: "0123456789abcdef0123456789abcdef", wantErr: true},
		{name: "braces", text: "{" + canonical + "}", wantErr: true},
		{name: "wrong separators", text: "01234567x89abxcdefx0123x456789abcdef", wantErr: true},
		{name: "misplaced dash", text: "0123456-789ab-cdef-0123-456789abcdef", wantErr: true},
		{name: "invalid hex", text: "g1234567-89ab-cdef-0123-456789abcdef", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePayloadID(tt.text)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParsePayloadID(%q) error = %v; want error %v", tt.text, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got != tt.want {
				t.Fatalf("ParsePayloadID(%q) = %v; want %v", tt.text, got, tt.want)
			}
			formatted := FormatPayloadID(got)
			if formatted != tt.format {
				t.Fatalf("FormatPayloadID(%v) = %q; want %q", got, formatted, tt.format)
			}
			if roundTrip, err := ParsePayloadID(formatted); err != nil || roundTrip != tt.want {
				t.Fatalf("round trip = %v, %v; want %v", roundTrip, err, tt.want)
			}
		})
	}
}
