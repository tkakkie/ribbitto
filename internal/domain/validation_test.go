package domain_test

import (
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/domain"
)

func TestValidation(t *testing.T) {
	for _, rule := range []struct {
		name           string
		validate       func(string) (string, error)
		valid, invalid []string
	}{
		{"email", domain.ValidateEmail, []string{"a@b", strings.Repeat("界", 84) + "@b"}, []string{"", "a", "@b", "a@", "a@@b", "a^@@b", "a\x00@b", "a\n@b", "a\u0085@b", "a\xff@b", strings.Repeat("a", 253) + "@b"}},
		{"display", domain.ValidateDisplayName, []string{"a", strings.Repeat("界", 50)}, []string{"", "  ", strings.Repeat("界", 51), "a\x00", "a\n", "a\u007f", "\xff"}},
		{"password", domain.ValidatePassword, []string{strings.Repeat("界", 15), strings.Repeat("a", 128), strings.Repeat(" ", 15), strings.Repeat("\x00", 15)}, []string{"", strings.Repeat("界", 14), strings.Repeat("界", 129), strings.Repeat("\xff", 15)}},
		{"organization", domain.ValidateOrganizationName, []string{"a", strings.Repeat("界", 100)}, []string{"", strings.Repeat("界", 101), "a\x00", "a\t", "\xff"}},
		{"slug", domain.ValidateSlug, []string{"a", "0", "a-b", "a" + strings.Repeat("-", 61) + "0"}, []string{"", "-", "-a", "a-", "A", "a_b", "a.b", "a\n", "界", strings.Repeat("a", 64)}},
		{"handle", domain.ValidateHandle, []string{"ab", "a0", "tomoya", "a_b.c-d", "a" + strings.Repeat("-", 30) + "z", "member-1", "everyones", "al"}, []string{"", "a", "0a", "_a", "a_", "a.", "a-", "a b", "a@b", "a\x00b", "a\nb", "\xff", "tomoyá", "Kelvin", "ｔｏｍｏｙａ", "a" + strings.Repeat("b", 32), "everyone", "here", "channel", "all", "ALL", "\ttomoya", "tomoya\n", "\u00a0\u200btomoya"}},
	} {
		t.Run(rule.name, func(t *testing.T) {
			for _, value := range rule.valid {
				if got, err := rule.validate(value); err != nil || got != value {
					t.Errorf("accepted %q: got %q, %v", value, got, err)
				}
			}
			for _, value := range rule.invalid {
				if _, err := rule.validate(value); err == nil {
					t.Errorf("accepted invalid %q", value)
				}
			}
		})
	}
	for _, tc := range []struct {
		validate    func(string) (string, error)
		input, want string
	}{
		{domain.ValidateEmail, "  USER@EXAMPLE.COM  ", "user@example.com"},
		{domain.ValidateEmail, "  " + strings.Repeat("A", 252) + "@B  ", strings.Repeat("a", 252) + "@b"},
		{domain.ValidateDisplayName, "　 Alice 　", "Alice"},
		{domain.ValidateOrganizationName, "　 Example Team 　", "Example Team"},
		{domain.ValidateEmail, "  E\u0301@EXAMPLE.COM  ", "é@example.com"},
		{domain.ValidateDisplayName, "  e\u0301  ", "é"},
		{domain.ValidateOrganizationName, "  e\u0301  ", "é"},
		{domain.ValidateDisplayName, "Alice Smith", "Alice Smith"},
		{domain.ValidateDisplayName, "山田\u3000太郎", "山田\u3000太郎"},
		{domain.ValidateHandle, "  Tomoya  ", "tomoya"},
		{domain.ValidateHandle, "Kelvin", "kelvin"},
		// Trimming comes before the ASCII check, so Unicode white space around a handle is removed, not rejected.
		{domain.ValidateHandle, "\u3000Tomoya\u00a0", "tomoya"},
	} {
		if got, err := tc.validate(tc.input); err != nil || got != tc.want {
			t.Errorf("normalizing %q: got %q, %v; want %q", tc.input, got, err, tc.want)
		}
	}
}

func TestValidationNonPrintableText(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"zero-width space", "\u200b"},
		{"RTL override", "\u202e"},
		{"BOM", "\ufeff"},
		{"soft hyphen", "\u00ad"},
		{"line separator", "\u2028"},
		{"paragraph separator", "\u2029"},
		{"nonbreaking space", "\u00a0"},
		{"ASCII space", " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := domain.ValidateEmail("a" + tc.text + "b@example.com"); err == nil {
				t.Error("email accepted non-printable text or space")
			}
			if tc.text == " " {
				return // ASCII spaces are allowed inside names.
			}
			if _, err := domain.ValidateDisplayName("a" + tc.text + "b"); err == nil {
				t.Error("display name accepted non-printable text")
			}
			if _, err := domain.ValidateOrganizationName("a" + tc.text + "b"); err == nil {
				t.Error("organization name accepted non-printable text")
			}
		})
	}
}

// The accepted and rejected names mirror the examples in docs/names.md.
func TestBlankLookingNames(t *testing.T) {
	for _, tc := range []struct {
		name  string
		blank bool
	}{
		{"", true},
		{"ㅤ", true},   // Hangul filler
		{"ﾠ", true},   // half-width Hangul filler
		{"ᅟᅠ", true},  // Hangul choseong and jungseong fillers
		{"⠀⠀", true},  // braille blank
		{"́", true},   // combining acute accent with no base
		{"⃝", true},   // enclosing mark with no base
		{"ㅤ ㅤ", true}, // fillers around an ASCII space
		{"⠀　́", true}, // braille blank, ideographic space, mark
		{"Alice", false},
		{"山田　太郎", false},
		{"김민준", false},
		{"가", false}, // Hangul written with conjoining jamo
		{"é", false},
		{"é", false}, // a base with its mark
		{"ㅤa", false}, // one visible character is enough
		{"😀", false},
		{"مريم", false},
	} {
		if got := domain.IsBlankLookingName(tc.name); got != tc.blank {
			t.Errorf("IsBlankLookingName(%q) = %t, want %t", tc.name, got, tc.blank)
		}
		// Every name that is valid otherwise is rejected exactly when blank-looking.
		if tc.name == "" {
			continue
		}
		if _, err := domain.ValidateDisplayName(tc.name); (err != nil) != tc.blank {
			t.Errorf("ValidateDisplayName(%q): %v, want rejected = %t", tc.name, err, tc.blank)
		}
	}
}
