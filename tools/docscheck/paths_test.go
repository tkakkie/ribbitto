package main

import (
	"strings"
	"testing"
)

func TestPaths(t *testing.T) {
	for _, tt := range []struct {
		name, span, want string
	}{
		{"existing file", "internal/web/handler.go", ""},
		{"existing directory", "internal/web", ""},
		{"missing path", "internal/gone", "internal/gone"},
		{"command path", "cmd/gone", "cmd/gone"},
		{"database path", "db/gone", "db/gone"},
		{"document path", "docs/gone.txt", "docs/gone.txt"},
		{"tool path", "tools/gone", "tools/gone"},
		{"script path", "scripts/gone", "scripts/gone"},
		{"GitHub path", ".github/gone", ".github/gone"},
		{"placeholder", "internal/<module>/store", ""},
		{"Unicode ellipsis", "internal/…/store", ""},
		{"ASCII ellipsis", "cmd/...", ""},
		{"glob", "db/queries/**", ""},
		{"question glob", "web/static/?.js", ""},
		{"bracket glob", "web/static/[ab].js", ""},
		{"line suffix", "internal/web/handler.go:42", ""},
		{"missing with line suffix", "internal/gone.go:42", "internal/gone.go"},
		{"trailing slash", "internal/web/", ""},
		{"slash and line suffix", "internal/web/:42", ""},
		{"missing slash and line suffix", "internal/gone/:42", "internal/gone"},
		{"line suffix and slash", "internal/web:42/", ""},
		{"missing with trailing slash", "internal/gone/", "internal/gone"},
		{"brace set", "internal/web/i18n/locales/{en,ja}.toml", ""},
		{"missing brace member", "internal/web/i18n/locales/{en,fr}.toml", "internal/web/i18n/locales/fr.toml"},
		{"multiple brace sets", "internal/{web,gone}/i18n/locales/{en,ja}.toml", "internal/gone/i18n/locales/en.toml\ninternal/gone/i18n/locales/ja.toml"},
		{"brace set with line suffix", "internal/web/i18n/locales/{en,ja}.toml:42", ""},
		{"package selector", "internal/web.NewHandler", ""},
		{"identity selector", "internal/identity.Sessions", ""},
		{"method selector", "internal/identity.Sessions.Create", ""},
		{"invalid method identifier", "internal/identity.Sessions.42", "internal/identity.Sessions.42"},
		{"unexported selector", "internal/web.newHandler", ""},
		{"unexported method selector", "internal/identity.Sessions.create", ""},
		{"unexported type selector", "internal/identity.sessions", ""},
		{"unexported type and method selector", "internal/identity.sessions.create", ""},
		{"underscore identifier", "internal/web._handler", ""},
		{"non-digit number in identifier", "internal/web.handler²", "internal/web.handler²"},
		{"missing Go file", "internal/web.go", "internal/web.go"},
		{"missing SQL file", "internal/web.sql", "internal/web.sql"},
		{"known extension after selector", "internal/identity.sessions.go", "internal/identity.sessions.go"},
		{"moved Markdown file", "docs/architecture.md", "docs/architecture.md"},
		{"selector without Go files", "docs/architecture.NewHandler", "docs/architecture.NewHandler"},
		{"selector with only Go directory", "internal/empty.Missing", "internal/empty.Missing"},
		{"missing selector package", "internal/gone.NewHandler", "internal/gone.NewHandler"},
		{"selector prefix is a file", "internal/web/handler.go.Name", "internal/web/handler.go.Name"},
		{"invalid identifier", "internal/web.42", "internal/web.42"},
		{"file extension", "web/static/js/app.js", ""},
		{"missing file with extension", "web/static/js/gone.js", "web/static/js/gone.js"},
		{"existing dotted file wins", "internal/web.NewHandler.go", ""},
		{"unknown top level", "example.com/pkg", ""},
		{"ordinary code", "org.Authorizer", ""},
		{"command", "cmd/seed -streams 60000", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := repo(t, map[string]string{
				"docs/a.md":               "# Paths\n\n`" + tt.span + "`\n",
				"internal/web/handler.go": "", "internal/web.NewHandler.go": "",
				"internal/web/i18n/locales/en.toml": "", "internal/web/i18n/locales/ja.toml": "",
				"web/static/js/app.js":                    "",
				"internal/identity/doc.go":                "",
				"docs/architecture/README.md":             "",
				"internal/empty/something.go/placeholder": "",
			})
			problems, err := checkPaths(root)
			if err != nil {
				t.Fatal(err)
			}
			for i := range problems {
				if !strings.HasPrefix(problems[i], "docs/a.md:3: ") || !strings.HasSuffix(problems[i], " does not exist") {
					t.Fatalf("unexpected diagnostic: %q", problems[i])
				}
				problems[i] = strings.TrimPrefix(problems[i], "docs/a.md:3: ")
				problems[i] = strings.TrimSuffix(problems[i], " does not exist")
			}
			if strings.Join(problems, "\n") != tt.want {
				t.Fatalf("problems %q, want %q", problems, tt.want)
			}
		})
	}
}

func TestPathScope(t *testing.T) {
	for _, tt := range []struct {
		file    string
		checked bool
	}{
		{"AGENTS.md", true}, {"README.md", true}, {"DECISIONS.md", true},
		{"docs/a.md", true}, {"docs/schema/a.md", true}, {"docs/dependencies.md", true},
		{".github/a.md", false}, {"scripts/a.md", false}, {"notes.md", false},
		{"docs/decisions/01-old.md", false},
	} {
		t.Run(tt.file, func(t *testing.T) {
			root := repo(t, map[string]string{tt.file: "`internal/gone`\n"})
			problems, err := checkPaths(root)
			if err != nil {
				t.Fatal(err)
			}
			if tt.checked && (len(problems) != 1 || problems[0] != tt.file+":1: internal/gone does not exist") || !tt.checked && len(problems) != 0 {
				t.Fatalf("problems %q, checked %v", problems, tt.checked)
			}
		})
	}
}

func TestPathMarkdown(t *testing.T) {
	root := repo(t, map[string]string{"docs/a.md": "internal/prose\n\n```\n`internal/fenced`\n```\n\n    `internal/indented`\n\n[`internal/linked`](https://example.com)\n\n``internal/double``\n"})
	problems, err := checkPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 2 || problems[0] != "docs/a.md:11: internal/double does not exist" || problems[1] != "docs/a.md:9: internal/linked does not exist" {
		t.Fatalf("problems %q", problems)
	}
}

func TestDecisionLinksStillChecked(t *testing.T) {
	root := repo(t, map[string]string{"docs/decisions/01-old.md": "`internal/old` [missing](gone.md) [anchor](target.md#gone)", "docs/decisions/target.md": "# Target"})
	problems, err := checkLinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 2 || !strings.Contains(problems[0], "gone.md does not exist") || !strings.Contains(problems[1], "has no heading #gone") {
		t.Fatalf("problems %q", problems)
	}
}
