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
	} {
		if got, err := tc.validate(tc.input); err != nil || got != tc.want {
			t.Errorf("normalizing %q: got %q, %v; want %q", tc.input, got, err, tc.want)
		}
	}
}
