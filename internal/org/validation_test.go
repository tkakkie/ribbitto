package org_test

import (
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/org"
)

func TestValidation(t *testing.T) {
	for _, rule := range []struct {
		name           string
		validate       func(string) (string, error)
		valid, invalid []string
	}{
		{"organization", org.ValidateOrganizationName, []string{"a", strings.Repeat("界", 100)}, []string{"", strings.Repeat("界", 101), "a\x00", "a\t", "\xff"}},
		{"slug", org.ValidateSlug, []string{"a", "0", "a-b", "a" + strings.Repeat("-", 61) + "0"}, []string{"", "-", "-a", "a-", "A", "a_b", "a.b", "a\n", "界", strings.Repeat("a", 64)}},
		{"handle", org.ValidateHandle, []string{"ab", "a0", "tomoya", "a_b.c-d", "a" + strings.Repeat("-", 30) + "z", "member-1", "everyones", "al"}, []string{"", "a", "0a", "_a", "a_", "a.", "a-", "a b", "a@b", "a\x00b", "a\nb", "\xff", "tomoyá", "Kelvin", "ｔｏｍｏｙａ", "a" + strings.Repeat("b", 32), "everyone", "here", "channel", "all", "ALL", "\ttomoya", "tomoya\n", "\u00a0\u200btomoya"}},
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
		{org.ValidateOrganizationName, "　 Example Team 　", "Example Team"},
		{org.ValidateOrganizationName, "  e\u0301  ", "é"},
		{org.ValidateHandle, "  Tomoya  ", "tomoya"},
		{org.ValidateHandle, "Kelvin", "kelvin"},
		// Trimming comes before the ASCII check, so Unicode white space around a handle is removed, not rejected.
		{org.ValidateHandle, "\u3000Tomoya\u00a0", "tomoya"},
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
			if tc.text == " " {
				return // ASCII spaces are allowed inside names.
			}
			if _, err := org.ValidateOrganizationName("a" + tc.text + "b"); err == nil {
				t.Error("organization name accepted non-printable text")
			}
		})
	}
}
