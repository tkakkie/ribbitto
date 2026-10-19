package main

import (
	"path"
	"regexp"
	"strings"
)

const fuzzKind = "fuzz"
const browserJavaScriptKind = "browser JavaScript"

type rule struct {
	pattern string
	kind    string
}

// Derived from origin/main's 976 tracked files on 2026-10-19.
// Patterns match whole, case-sensitive path components: * never crosses /,
// and ** is one whole component matching zero or more directories. Every
// other directory rule has exactly the written depth, the narrowest in the baseline.
// The two fuzz roots are explicit; Fuzz* names one directory and * one file.
// Vendor and testdata are fenced before the general rules so no new kind or
// filename can enter through (for example) Go-anywhere. There are no exclusions.
func rules() []rule {
	return []rule{
		{"**/*.go", "Go"},
		{"db/migrations/*.sql", "SQL migrations"},
		{"db/queries/*/*.sql", "SQL queries"},
		{"internal/web/view/*.templ", "templ"},
		{"internal/lintfixture/*.templ", "templ lint fixture"},
		{"web/styles/*.css", "CSS source"},
		{"web/static/css/*.css", "CSS output"},
		// The pattern follows web/static/javascript_test.go; the kind also checks its regexp.
		{"web/static/*-v[0-9]*.js", browserJavaScriptKind},
		{"web/static/brand/*.svg", "brand SVG"},
		{"web/static/channel-images-v1/*.svg", "channel SVG"},
		{"docs/**/*.md", "documentation"},
		{".github/ISSUE_TEMPLATE/*.md", "issue templates"},
		{".github/instructions/*.md", "review instructions"},
		{".github/prompts/*.md", "review prompts"},
		{".github/pull_request_template.md", "pull request template"},
		{"AGENTS.md", "agent instructions"},
		{"CLAUDE.md", "agent instructions"},
		{"DECISIONS.md", "decision index"},
		{"README.md", "repository documentation"},
		{".github/workflows/*.yml", "workflows"},
		{".github/ISSUE_TEMPLATE/config.yml", "issue configuration"},
		{".github/dependabot.yml", "dependency configuration"},
		{".golangci.yml", "lint configuration"},
		{".tbls.yml", "schema configuration"},
		{"compose.yml", "development services"},
		{"sqlc.yaml", "query generation configuration"},
		{"internal/web/i18n/locales/*.toml", "translations"},
		{"cmd/seed/conversations.json", "seed data"},
		{"go.mod", "Go module"}, {"go.sum", "Go checksums"},
		{"tools/go.mod", "Go module"}, {"tools/go.sum", "Go checksums"},
		{"tools/tbls/go.mod", "Go module"}, {"tools/tbls/go.sum", "Go checksums"},
		{"LICENSE", "licence"}, {"Makefile", "build commands"},
		{".gitignore", "git configuration"}, {".gitattributes", "git configuration"},
		{".env.example", "example configuration"},
		{"docs/api/*.txt", "API summaries"},
		{"docs/size-exceptions.txt", "document exceptions"},
		{exceptionsFile, "language exceptions"},
		{"tools/scopecheck/allowlist.txt", "query check configuration"},
	}
}

func testdataRules() []rule {
	return []rule{
		{"internal/web/testdata/browser-shard-totals.txt", "browser test configuration"},
		{"internal/conversation/testdata/fuzz/Fuzz*/*", fuzzKind},
		{"internal/identity/testdata/fuzz/Fuzz*/*", fuzzKind},
	}
}

func vendorRules() []rule {
	return []rule{
		{"web/static/vendor/README.md", "vendor documentation"},
		{"web/static/vendor/fluent-icons-1.1.344.svg", "vendor SVG"},
		{"web/static/vendor/htmx-2.0.7.min.js", "vendor JavaScript"},
		{"web/static/vendor/htmx-ext-sse-2.2.4.min.js", "vendor JavaScript"},
		{"web/static/vendor/idiomorph-ext-0.7.4.min.js", "vendor JavaScript"},
		{"web/static/vendor/fluent-icons-LICENSE", "vendor licence"},
		{"web/static/vendor/htmx-ext-sse-LICENSE", "vendor licence"},
		{"web/static/vendor/htmx.org-LICENSE", "vendor licence"},
		{"web/static/vendor/idiomorph-LICENSE", "vendor licence"},
	}
}

func allowedKind(name string) string {
	if !validName(name) {
		return ""
	}
	table := rules()
	parts := strings.Split(name, "/")
	// No vendor directory anywhere inherits general rules, including a nested
	// vendor directory below an otherwise allowed documentation or asset path.
	for _, part := range parts[:len(parts)-1] {
		if part == "vendor" {
			table = vendorRules()
			break
		}
		if part == "testdata" {
			table = testdataRules()
			break
		}
	}
	for _, r := range table {
		if match(strings.Split(r.pattern, "/"), parts) {
			if r.kind == fuzzKind && path.Ext(name) != "" {
				continue
			}
			if r.kind == browserJavaScriptKind && !regexp.MustCompile(`^.+-v[0-9]+\.js$`).MatchString(path.Base(name)) {
				continue
			}
			return r.kind
		}
	}
	return ""
}

func match(pattern, name []string) bool {
	if len(pattern) == 0 {
		return len(name) == 0
	}
	if pattern[0] == "**" {
		for i := 0; i < len(name); i++ {
			if match(pattern[1:], name[i:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	ok, err := path.Match(pattern[0], name[0])
	return err == nil && ok && match(pattern[1:], name[1:])
}
