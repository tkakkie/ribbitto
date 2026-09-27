package domain

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidateEmail returns the trimmed, lower-case address used for storage and lookup.
func ValidateEmail(value string) (string, error) {
	if !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
		return "", fmt.Errorf("email must contain valid text without control characters")
	}
	value = strings.ToLower(strings.TrimSpace(value))
	local, host, found := strings.Cut(value, "@")
	if !found || local == "" || host == "" || strings.Contains(host, "@") || len(value) > 254 {
		return "", fmt.Errorf("email must have one @ with nonempty parts and at most 254 bytes")
	}
	return value, nil
}

// ValidateDisplayName returns a trimmed name of 1–50 Unicode code points.
func ValidateDisplayName(value string) (string, error) {
	if strings.ContainsFunc(value, unicode.IsControl) {
		return "", fmt.Errorf("display name must not contain control characters")
	}
	return validateText(strings.TrimSpace(value), 1, 50, "display name")
}

// ValidatePassword preserves the input and imposes only a 15–128 code point length.
func ValidatePassword(value string) (string, error) {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 15 || utf8.RuneCountInString(value) > 128 {
		return "", fmt.Errorf("password must contain 15–128 Unicode code points")
	}
	return value, nil
}

// ValidateOrganizationName accepts 1–100 Unicode code points without controls.
func ValidateOrganizationName(value string) (string, error) {
	return validateText(value, 1, 100, "organization name")
}

// ValidateSlug accepts 1–63 ASCII letters, digits or hyphens, with alphanumeric ends.
func ValidateSlug(value string) (string, error) {
	if len(value) < 1 || len(value) > 63 || value[0] == '-' || value[len(value)-1] == '-' {
		return "", fmt.Errorf("slug must contain 1–63 characters with alphanumeric ends")
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return "", fmt.Errorf("slug must contain only a-z, 0-9 and hyphens")
		}
	}
	return value, nil
}

func validateText(value string, minLength, maxLength int, field string) (string, error) {
	length := utf8.RuneCountInString(value)
	if !utf8.ValidString(value) || length < minLength || length > maxLength || strings.ContainsFunc(value, unicode.IsControl) {
		return "", fmt.Errorf("%s must contain %d–%d Unicode code points without controls", field, minLength, maxLength)
	}
	return value, nil
}
