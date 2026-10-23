package identity_test

import (
	"testing"

	"github.com/tkakkie/ribbitto/internal/identity"
)

// Each code point the blank-looking list names is blank on its own, next to
// itself and between spaces, and a visible neighbour of it is not.
func TestBlankLookingCodePoints(t *testing.T) {
	for _, tc := range []struct {
		name  string
		r     rune
		blank bool
	}{
		{"space", ' ', true},
		{"ideographic space", '　', true},
		{"Hangul filler", 'ㅤ', true},
		{"half-width Hangul filler", 'ﾠ', true},
		{"Hangul choseong filler", 'ᅟ', true},
		{"Hangul jungseong filler", 'ᅠ', true},
		{"braille blank", '⠀', true},
		{"combining acute accent", '́', true},
		{"Latin letter", 'a', false},
		{"Hangul choseong kiyeok", 'ᄀ', false},
		{"Hangul choseong before the filler", 'ᅞ', false},
		{"Hangul jongseong kiyeok", 'ᆨ', false},
		{"half-width Hangul kiyeok", 'ﾡ', false},
		{"Hangul compatibility jamo after the filler", 'ㅥ', false},
		{"braille dot one", '⠁', false},
		{"ideographic character", '界', false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := string(tc.r)
			for _, name := range []string{s, s + s, " " + s + "　"} {
				if got := identity.IsBlankLookingName(name); got != tc.blank {
					t.Errorf("IsBlankLookingName(%q) = %t, want %t", name, got, tc.blank)
				}
			}
		})
	}
}
