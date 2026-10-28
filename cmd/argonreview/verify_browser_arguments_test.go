package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Each row changes one scope predicate; the same corpus exercises guard/server agreement.
func verifyBrowserArgumentsCases() []struct {
	name, raw string
	allow     bool
} {
	cases := []struct {
		name, raw string
		allow     bool
	}{}
	add := func(name, raw string, allow bool) {
		cases = append(cases, struct {
			name, raw string
			allow     bool
		}{name, raw, allow})
	}
	encode := func(pkg, run string) string {
		b, _ := json.Marshal(map[string]string{"check": "test-browser", "package": pkg, "run": run})
		return string(b)
	}
	for _, pkg := range []string{"./internal/web", "./cmd/ribbitto"} {
		add("allow-package-"+pkg, encode(pkg, "TestTopicPostBrowser"), true)
	}
	for _, run := range []string{"TestBrowser/drafts-markers/sidebar-sse", "TestBrowser/quote/scripts=true/width=1280", "TestTopicPostBrowser", "TestBrowser/a/b/c/d/e/f", "TestBrowser/" + strings.Repeat("a", 62) + "/" + strings.Repeat("b", 62) + "/" + strings.Repeat("c", 62)} {
		add("allow-run-"+run, encode("./internal/web", run), true)
	}
	prefix := `{"check":"test-browser","package":"./internal/web"`
	add("run-required", prefix+`}`, false)
	for _, value := range []string{`null`, `1`, `true`, `[]`} {
		add("run-type-"+value, prefix+`,"run":`+value+`}`, false)
	}
	add("duplicate-run", prefix+`,"run":"TestBrowser/a","run":"TestBrowser/a"}`, false)
	add("escaped-duplicate-run", prefix+`,"run":"TestBrowser/a","r\u0075n":"TestBrowser/a"}`, false)
	add("wrong-case", prefix+`,"Run":"TestBrowser/a"}`, false)
	add("wrong-key", prefix+`,"other":"run"}`, false)
	add("extra-key", prefix+`,"run":"TestBrowser/a","extra":"x"}`, false)
	add("run-overlong", encode("./internal/web", "TestBrowser/"+strings.Repeat("a", 63)+"/"+strings.Repeat("b", 62)+"/"+strings.Repeat("c", 62)), false)
	for _, pkg := range []string{"./...", ".", "./internal/org", "./internal/web/...", "./cmd/ribbitto/x"} {
		add("deny-package-"+pkg, encode(pkg, "TestBrowser/a"), false)
	}
	for _, run := range []string{"TestBrowser", "", "-test.v", "a|b", "TestFoo", "../x"} {
		add("deny-run-"+run, encode("./internal/web", run), false)
	}
	for _, check := range verifyCheckNames() {
		// generated's valid package isolates the run-only predicate.
		for _, run := range []string{"TestBrowser/a", ""} {
			b, _ := json.Marshal(map[string]string{"check": check, "package": "./...", "run": run})
			add("ordinary-run-"+check+"-"+run, string(b), false)
		}
	}
	return cases
}

func TestVerifyArgumentsBrowser(t *testing.T) {
	for _, row := range verifyBrowserArgumentsCases() {
		t.Run(row.name, func(t *testing.T) {
			a, err := parseVerifyArguments([]byte(row.raw))
			if (err == nil) != row.allow {
				t.Fatalf("browser argument scope %s: accepted=%v want=%v: %v", row.name, err == nil, row.allow, err)
			}
			if row.allow {
				var want verifyArguments
				var fields map[string]string
				_ = json.Unmarshal([]byte(row.raw), &fields)
				want = verifyArguments{check: fields["check"], pattern: fields["package"], run: fields["run"]}
				if a != want {
					t.Fatalf("browser argument values %s: %+v want %+v", row.name, a, want)
				}
			}
		})
	}
}

func TestVerifyContainerBrowserValidation(t *testing.T) {
	for _, row := range verifyBrowserArgumentsCases() {
		var fields map[string]string
		// Object shape belongs to the parser; only value predicates reach the runner.
		if json.Unmarshal([]byte(row.raw), &fields) != nil {
			continue
		}
		valueCase := row.allow || strings.HasPrefix(row.name, "deny-") || row.name == "run-required" || row.name == "run-overlong" || strings.HasPrefix(row.name, "ordinary-run-") && fields["run"] != ""
		if !valueCase {
			continue
		}
		t.Run(row.name, func(t *testing.T) {
			err := validateContainerCheck("tree", fields["check"], fields["package"], fields["run"])
			if (err == nil) != row.allow {
				t.Fatalf("container validation scope %s: %v", row.name, err)
			}
		})
	}
}
