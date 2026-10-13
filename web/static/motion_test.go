package static_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestReducedMotion proves on the built stylesheet that reduced motion stops
// every motion (docs/ui-code.md#motion): animations and transitions by one
// universal rule nothing outranks, view transitions and movement on state
// changes by existing only for no-preference.
func TestReducedMotion(t *testing.T) {
	source, err := os.ReadFile("css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	const reduce, safe = "@media (prefers-reduced-motion:reduce)", "@media (prefers-reduced-motion:no-preference)"
	property := regexp.MustCompile(`^(animation|transition|scale|rotate|translate|transform|view-transition)(-[a-z-]+)?$`)
	state := regexp.MustCompile(`:(hover|active|focus)`)
	reset, navigation, built := false, false, map[string]bool{}
	for _, rule := range cssRules(string(source)) {
		context := strings.Join(rule.context, " ")
		if strings.Contains(context, "@keyframes") || strings.HasPrefix(rule.selector, "@property") {
			continue
		}
		if strings.Contains(context, reduce) {
			reset = reset || strings.HasPrefix(rule.selector, "*") &&
				strings.Contains(rule.declarations, "animation:none!important") && strings.Contains(rule.declarations, "transition:none!important")
			continue
		}
		if (rule.selector == "@view-transition" || strings.Contains(rule.selector, "::view-transition")) && !strings.Contains(context, safe) {
			t.Errorf("%s: view transition outside %s", rule.selector, safe)
		}
		navigation = navigation || rule.selector == "@view-transition" && rule.declarations == "navigation:auto"
		for _, declaration := range strings.Split(rule.declarations, ";") {
			name, value, _ := strings.Cut(declaration, ":")
			match := property.FindStringSubmatch(name)
			switch {
			case match == nil:
			case match[1] == "animation" || match[1] == "transition":
				built[rule.selector] = true
				// Layered !important would outrank the unlayered reset.
				if strings.Contains(value, "!important") {
					t.Errorf("%s: %s is !important, so reduced motion cannot reset it", rule.selector, name)
				}
			case match[1] == "view-transition" || match[1] == "scale" || match[1] == "rotate" ||
				state.MatchString(rule.selector):
				built[rule.selector] = true
				if !strings.Contains(context, safe) {
					t.Errorf("%s: %s outside %s", rule.selector, name, safe)
				}
			}
		}
	}
	if !reset {
		t.Errorf("no %s rule resets animation and transition on every element with !important", reduce)
	}
	if !navigation {
		t.Errorf("no @view-transition { navigation: auto }")
	}
	// The motion classes are built, so the checks above cover them.
	for _, class := range []string{".animate-message-in", ".animate-typing-dot-3", ".transition", `.motion-safe\:active\:scale-96:active`} {
		if !built[class] {
			t.Errorf("motion class %s missing from the stylesheet", class)
		}
	}
}

type cssRule struct {
	context      []string // enclosing at-rule preludes, outermost first
	selector     string   // or the prelude of an at-rule holding declarations
	declarations string
}

// cssRules lists the rules of Tailwind's minified output by splitting on
// braces; its strings and URLs hold none.
func cssRules(source string) []cssRule {
	source = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(source, "")
	var rules []cssRule
	var stack []string
	start := 0
	for i, r := range source {
		switch {
		case r == '{':
			prelude := source[start:i]
			stack = append(stack, strings.TrimSpace(prelude[strings.LastIndex(prelude, ";")+1:]))
			start = i + 1
		case r == '}' && len(stack) > 0:
			if body := strings.TrimSpace(source[start:i]); body != "" {
				rules = append(rules, cssRule{append([]string(nil), stack[:len(stack)-1]...), stack[len(stack)-1], body})
			}
			stack, start = stack[:len(stack)-1], i+1
		}
	}
	return rules
}
