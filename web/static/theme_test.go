package static_test

import (
	"os"
	"strings"
	"testing"
)

// TestDarkColourTokens prevents a new light token from leaking into dark pages.
func TestDarkColourTokens(t *testing.T) {
	source, err := os.ReadFile("css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	light, dark := map[string]bool{}, map[string]bool{}
	for _, rule := range cssRules(string(source)) {
		if rule.selector != ":root" {
			continue
		}
		tokens := light
		if strings.Contains(strings.Join(rule.context, " "), "@media (prefers-color-scheme:dark)") {
			tokens = dark
		}
		for _, declaration := range strings.Split(rule.declarations, ";") {
			name, _, _ := strings.Cut(declaration, ":")
			if strings.HasPrefix(name, "--rb-") {
				tokens[name] = true
			}
		}
	}
	if len(light) == 0 {
		t.Fatal("no light colour tokens found in the built stylesheet")
	}
	for name := range light {
		if !dark[name] {
			t.Errorf("dark scheme lacks %s", name)
		}
	}
	for name := range dark {
		if !light[name] {
			t.Errorf("dark scheme defines unknown colour token %s", name)
		}
	}
}
