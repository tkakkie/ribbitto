package static_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestReducedMotion proves on the built stylesheet that reduced motion stops
// every motion (docs/ui-code.md#motion): animations and transitions by one
// universal rule nothing outranks, movement on state changes by existing only
// for no-preference.
func TestReducedMotion(t *testing.T) {
	source, err := os.ReadFile("css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	const reduce, safe = "@media (prefers-reduced-motion:reduce)", "@media (prefers-reduced-motion:no-preference)"
	property := regexp.MustCompile(`^(animation|transition|scale|rotate|translate|transform)(-[a-z-]+)?$`)
	state := regexp.MustCompile(`:(hover|active|focus)`)
	// Minified, ::before and ::after are written :before and :after.
	pseudo := regexp.MustCompile(`::[a-z-]+`)
	reset, built := false, map[string]bool{}
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
		for _, declaration := range strings.Split(rule.declarations, ";") {
			name, value, _ := strings.Cut(declaration, ":")
			match := property.FindStringSubmatch(name)
			switch {
			case match == nil:
			case match[1] == "animation" || match[1] == "transition":
				built[rule.selector] = true
				// The reset reaches elements, ::before and ::after only, not a
				// sheet's ::backdrop or other pseudo-elements.
				if pseudo.MatchString(rule.selector) && !strings.Contains(context, safe) {
					t.Errorf("%s: %s on a pseudo-element the reset misses, outside %s", rule.selector, name, safe)
				}
				// Layered !important would outrank the unlayered reset.
				if strings.Contains(value, "!important") {
					t.Errorf("%s: %s is !important, so reduced motion cannot reset it", rule.selector, name)
				}
			case match[1] == "scale" || match[1] == "rotate" ||
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
	// The motion classes are built, so the checks above cover them.
	for _, class := range []string{".animate-typing-dot-1", ".animate-typing-dot-3", ".transition-feedback", `.motion-safe\:active\:scale-96:active`} {
		if !built[class] {
			t.Errorf("motion class %s missing from the stylesheet", class)
		}
	}
}

// TestNoViewTransitions proves the built stylesheet opts into no
// cross-document view transition: its setup delayed every switch (#921), and
// the arrival motion runs without it (docs/ui-code.md#motion).
func TestNoViewTransitions(t *testing.T) {
	source, err := os.ReadFile("css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), "view-transition") {
		t.Error("built stylesheet declares a view transition (@view-transition, ::view-transition or a view-transition property)")
	}
}

// TestHoverOnlyWithHover proves on the built stylesheet that every hover
// style applies only under @media (hover:hover): a tap leaves the tapped
// element :hover, and a touch screen must not keep it highlighted
// (docs/ui-code.md#motion). Tailwind's hover: utilities do this; a
// hand-written rule must too.
func TestHoverOnlyWithHover(t *testing.T) {
	source, err := os.ReadFile("css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	hovers := 0
	for _, rule := range cssRules(string(source)) {
		if strings.Contains(rule.selector, ":hover") {
			hovers++
			if !strings.Contains(strings.Join(rule.context, " "), "@media (hover:hover)") {
				t.Errorf("%s: a hover style outside @media (hover:hover)", rule.selector)
			}
		}
	}
	if hovers == 0 {
		t.Error("no hover style in the built stylesheet, so this test proves nothing")
	}
}

// TestArrivalMotion proves on the built stylesheet that the arrival motion,
// the pill, the panel and below desk its sheet, shows its content from the
// first frame (docs/ui-code.md#motion): its keyframes only move, never fade
// or hide, and no motion waits for a delay.
func TestArrivalMotion(t *testing.T) {
	source, err := os.ReadFile("css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	moves := regexp.MustCompile(`^(transform|translate|scale)$`)
	times := regexp.MustCompile(`var\(--duration-[a-z]+\)|(^|\s)[\d.]+m?s\b`)
	keyframes := map[string]bool{}
	for _, rule := range cssRules(string(source)) {
		context := strings.Join(rule.context, " ")
		// The typing dots, the only loop, are decorative and pulse by opacity.
		frames := strings.Contains(context, "@keyframes rb-")
		if frames {
			keyframes[context] = true
		}
		for _, declaration := range strings.Split(rule.declarations, ";") {
			name, value, _ := strings.Cut(declaration, ":")
			switch {
			case frames && !moves.MatchString(name):
				t.Errorf("%s %s: %s; arrival keyframes only move, so content shows from the first frame", context, rule.selector, declaration)
			case name == "animation-delay" || name == "transition-delay",
				name == "animation" && strings.HasPrefix(value, "rb-") && len(times.FindAllString(value, -1)) != 1:
				t.Errorf("%s: %s delays the motion", rule.selector, declaration)
			}
		}
	}
	// The phone's sheet rises by the panel's keyframes redefined under max-desk:.
	for _, want := range []string{"@keyframes rb-pill-in", "@keyframes rb-panel-in", "@media not all and (min-width:53.25rem) @keyframes rb-panel-in"} {
		if !keyframes[want] {
			t.Errorf("built stylesheet lacks %s", want)
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

// TestMotionTokens proves every documented motion token reaches the built
// stylesheet, used by a utility yet or not (docs/ui-code.md#motion).
func TestMotionTokens(t *testing.T) {
	source, err := os.ReadFile("css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"--duration-faster:", "--duration-fast:", "--duration-normal:", "--duration-gentle:", "--ease-standard:", "--ease-decelerate:", "--ease-accelerate:"} {
		if !strings.Contains(string(source), token) {
			t.Errorf("built stylesheet lacks %s", strings.TrimSuffix(token, ":"))
		}
	}
}
