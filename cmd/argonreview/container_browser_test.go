package main

import (
	"regexp"
	"strings"
	"testing"
)

func browserRunCases() (accepted, refused []string) {
	long := "TestBrowser/" + strings.Repeat("a", 64) + "/" + strings.Repeat("b", 64) + "/" + strings.Repeat("c", 58)
	return []string{
		"TestBrowser/drafts-markers/sidebar-sse", "TestBrowser/quote/scripts=true/width=1280", "TestTopicPostBrowser",
		"TestBrowser/a/b/c/d/e/f", long, "TestBrowser/" + strings.Repeat("a", 64), "Test_Browser", "TestBrowser/x",
		"Test" + strings.Repeat("A", 53) + "Browser", "Test0Browser/0_=-",
	}, []string{
		"", "TestBrowser", "TestBrowser/", "/TestBrowser", "TestBrowser//x", "TestBrowser/a/b/c/d/e/f/g", long + "x",
		"TestBrowser/" + strings.Repeat("a", 65), "Test" + strings.Repeat("A", 54) + "Browser",
		"..", "./TestBrowser", "-test.v", "-run=x", "TestBrowser -v", "TestBrowser/.*", "TestBrowser/|",
		"TestBrowser/^x$", "TestBrowser/(a)", "TestBrowser/[a]", "TestBrowser/a{2}", "TestBrowser/a+", "TestBrowser/a?",
		`TestBrowser/\x`, "TestBrowser/#01", "TestBrowser/-x", "TestBrowser/=x", "TestBrowser/_x", "Testbrowser",
		"testBrowser", "TestFoo", "TestBrowsers", "TestоBrowser", "TestBrowser／x", "TestBrowser/a\x00", "TestBrowser/a\n", "TestBrowser/a\t",
		"TestfooBrowser", "Test-Browser", "Test=Browser", "Test", "TestBrowser/a.b", "TestBrowser/a,b", "TestBrowser/a:b",
		"TestBrowser/a'b", `TestBrowser/a"b`, "TestBrowser/a%b",
	}
}

func TestBrowserRunValidation(t *testing.T) {
	accepted, refused := browserRunCases()
	for _, rows := range []struct {
		values []string
		valid  bool
	}{{accepted, true}, {refused, false}} {
		for _, value := range rows.values {
			t.Run(value, func(t *testing.T) {
				if err := validateBrowserRun(value); (err == nil) != rows.valid {
					t.Fatalf("validateBrowserRun(%q) = %v, valid = %v", value, err, rows.valid)
				}
			})
		}
	}
}

func TestBrowserRunPattern(t *testing.T) {
	for _, tc := range [][2]string{
		{"TestBrowser/drafts-markers/sidebar-sse", "^TestBrowser$/^drafts-markers$/^sidebar-sse$"},
		{"TestBrowser/quote/scripts=true/width=1280", "^TestBrowser$/^quote$/^scripts=true$/^width=1280$"},
		{"TestTopicPostBrowser", "^TestTopicPostBrowser$"}, {"-run=x", ""},
	} {
		got, err := browserRunPattern(tc[0])
		if got != tc[1] || (err == nil) != (tc[1] != "") {
			t.Fatalf("pattern(%q) = %q, %v; want %q", tc[0], got, err, tc[1])
		}
	}
	accepted, _ := browserRunCases()
	for _, run := range accepted {
		pattern, err := browserRunPattern(run)
		if err != nil {
			t.Fatal(err)
		}
		for i, segment := range strings.Split(run, "/") {
			if got := strings.Split(pattern, "/")[i]; got != "^"+regexp.QuoteMeta(segment)+"$" {
				t.Fatalf("segment pattern = %q", got)
			}
		}
	}
}

func TestBrowserPackage(t *testing.T) {
	for _, pattern := range []string{"./internal/web", "./cmd/ribbitto", ".", "./...", "./internal/web/", "./internal/web/...", "internal/web", "./internal/web/view", "./cmd/ribbitto/sub", ""} {
		want := ""
		if pattern == "./internal/web" || pattern == "./cmd/ribbitto" {
			want = "github.com/tkakkie/ribbitto/" + strings.TrimPrefix(pattern, "./")
		}
		t.Run(pattern, func(t *testing.T) {
			if got, err := browserPackageImportPath(pattern); got != want || (err == nil) != (want != "") {
				t.Fatalf("package(%q) = %q, %v; want %q", pattern, got, err, want)
			}
		})
	}
}

func FuzzBrowserRunValidation(f *testing.F) {
	accepted, refused := browserRunCases()
	for _, run := range append(accepted, refused...) {
		f.Add(run)
	}
	topRule := regexp.MustCompile(`^Test[A-Z0-9_][A-Za-z0-9_]*$`)
	subtestRule := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_=-]{0,63}$`)
	patternRule := regexp.MustCompile(`^[A-Za-z0-9_=/^$-]+$`)
	f.Fuzz(func(t *testing.T, run string) {
		valid := validateBrowserRun(run) == nil
		// This independent regexp reference also checks byte limits: its alphabet is ASCII.
		parts := strings.Split(run, "/")
		reference := len(run) >= 1 && len(run) <= 200 && len(parts) <= 7 && run != "TestBrowser"
		for i, part := range parts {
			rule := subtestRule
			if i == 0 {
				rule = topRule
				reference = reference && len(part) <= 64 && strings.HasSuffix(part, "Browser")
			}
			reference = reference && rule.MatchString(part)
		}
		pattern, err := browserRunPattern(run)
		if valid != reference || valid != (err == nil) || !valid && pattern != "" {
			t.Fatalf("run %q: accepted=%v, reference=%v, pattern=%q, error=%v", run, valid, reference, pattern, err)
		}
		if valid && (strings.HasPrefix(pattern, "-") || !patternRule.MatchString(pattern)) {
			t.Fatalf("unsafe pattern %q", pattern)
		}
	})
}
