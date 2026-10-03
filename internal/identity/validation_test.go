package identity_test

import (
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/identity"
)

func TestValidation(t *testing.T) {
	for _, rule := range []struct {
		name           string
		validate       func(string) (string, error)
		valid, invalid []string
	}{
		{"email", identity.ValidateEmail, []string{"a@b", strings.Repeat("界", 84) + "@b"}, []string{"", "a", "@b", "a@", "a@@b", "a^@@b", "a\x00@b", "a\n@b", "a\u0085@b", "a\xff@b", strings.Repeat("a", 253) + "@b"}},
		{"password", identity.ValidatePassword, []string{strings.Repeat("界", 15), strings.Repeat("a", 128), strings.Repeat(" ", 15), strings.Repeat("\x00", 15)}, []string{"", strings.Repeat("界", 14), strings.Repeat("界", 129), strings.Repeat("\xff", 15)}},
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
		{identity.ValidateEmail, "  USER@EXAMPLE.COM  ", "user@example.com"},
		{identity.ValidateEmail, "  " + strings.Repeat("A", 252) + "@B  ", strings.Repeat("a", 252) + "@b"},
		{identity.ValidateEmail, "  E\u0301@EXAMPLE.COM  ", "é@example.com"},
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
			if _, err := identity.ValidateEmail("a" + tc.text + "b@example.com"); err == nil {
				t.Error("email accepted non-printable text or space")
			}
		})
	}
}
