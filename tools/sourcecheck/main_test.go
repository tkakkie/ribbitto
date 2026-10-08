package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSources(t *testing.T) {
	labels, err := os.ReadFile("../../docs/domain/vocabulary.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ name, path, source, rule string }{
		{"table source", "sample.go", "package sample; var SploshID int", "docs/domain/vocabulary.md"},
		{"camel", "sample.go", "package sample; var PondID int", "docs/domain/vocabulary.md"},
		{"lower camel", "sample.go", "package sample; var pondFor int", "docs/domain/vocabulary.md"},
		{"acronym and digits", "sample.go", "package sample; var IDPond2 int", "docs/domain/vocabulary.md"},
		{"plural camel", "sample.go", "package sample; func ListRibbits() {}", "docs/domain/vocabulary.md"},
		{"plural es", "sample.go", "package sample; var marshes int", "docs/domain/vocabulary.md"},
		{"plural sql", "sample.sql", "CREATE TABLE ribbits (id int);", "docs/domain/vocabulary.md"},
		{"plural route", "sample.go", `package sample; func f() { mux.HandleFunc("GET /ponds/{id}", h) }`, "docs/domain/vocabulary.md"},
		{"hyphen route", "sample.go", `package sample; func f() { mux.HandleFunc("GET /pond-settings", h) }`, "docs/domain/vocabulary.md"},
		{"underscore route", "sample.go", `package sample; func f() { mux.HandleFunc("GET /x/pond_list", h) }`, "docs/domain/vocabulary.md"},
		{"plural suffix", "sample.go", `package sample; var routes = []orgRoute{{"GET", "/ripples/{id}", h}}`, "docs/domain/vocabulary.md"},
		{"handle route", "sample.go", `package sample; func f() { mux.Handle("GET /pond/", h) }`, "docs/domain/vocabulary.md"},
		{"package", "sample.go", "package marsh", "docs/domain/vocabulary.md"},
		{"filename", "pond_test.go", "package sample", "docs/domain/vocabulary.md"},
		{"directory", "pond/sample.go", "package sample", "docs/domain/vocabulary.md"},
		{"sql", "sample.sql", "CREATE TABLE sample (pond_id int);", "docs/domain/vocabulary.md"},
		{"quoted sql", "sample.sql", `SELECT "ribbit" FROM sample;`, "docs/domain/vocabulary.md"},
		{"route", "sample.go", `package sample; func f() { mux.HandleFunc("GET /pond/{id}", handler) }`, "docs/domain/vocabulary.md"},
		{"route suffix", "sample.go", `package sample; var routes = []orgRoute{{method, "/marsh/", handler}}`, "docs/domain/vocabulary.md"},
		{"session route", "sample.go", `package sample; func f() { mux.HandleFuncWithoutSession("POST /chorus", handler) }`, "docs/domain/vocabulary.md"},
		{"hex class", "sample.templ", `<p class="hover:text-[#fff]">`, "docs/ui.md"},
		{"hex background class", "sample.templ", `<p class="bg-[#fff]">`, "docs/ui.md"},
		{"rgb class", "sample.templ", `<p class={ "bg-[rgb(1_2_3)]" }>`, "docs/ui.md"},
		{"named class", "sample.templ", `<p class="bg-[rebeccapurple]">`, "docs/ui.md"},
		{"named background class", "sample.templ", `<p class="bg-[red]">`, "docs/ui.md"},
		{"image and colour class", "sample.templ", `<p class="bg-[url(https://example.com/red.svg),red]">`, "docs/ui.md"},
		{"inline style", "sample.templ", `<p style='color: red'>`, "docs/ui.md"},
		{"dynamic style", "sample.templ", `<p style={ "color: oklch(1 0 0)" }>`, "docs/ui.md"},
		{"css literal", "web/styles/app.css", `.x { color: #fff; }`, "docs/ui.md"},
		{"css without semicolon", "web/styles/app.css", `.x { color: red }`, "docs/ui.md"},
		{"css shadow", "web/styles/app.css", `.x { box-shadow: 0 0 2px black; }`, "docs/ui.md"},
		{"local variable", "web/styles/app.css", `.x { --local: blue; }`, "docs/ui.md"},
		{"apply hex", "web/styles/app.css", `.x { @apply bg-[#fff]; }`, "docs/ui.md"},
		{"apply named", "web/styles/app.css", `.x { @apply text-[red]; }`, "docs/ui.md"},
		{"nonroot token", "web/styles/app.css", `.x { --rb-bg: red; }`, "docs/ui.md"},
		{"root local", "web/styles/app.css", `:root { --local: red; }`, "docs/ui.md"},
		{"theme declaration", "web/styles/app.css", `@theme { color: red; }`, "docs/ui.md"},
		{"arbitrary property", "sample.templ", `<p class="[color:red]">`, "docs/ui.md"},
		{"ordinary class", "sample.templ", `<p class="red white-space">`, ""},
		{"style property", "sample.templ", `<p style="white-space: nowrap">`, ""},
		{"class property", "sample.templ", `<p class="[white-space:pre]">`, ""},
		{"css function", "web/styles/app.css", `.x { rotate: calc(tan(1deg) * 1turn); }`, ""},
		{"css spaced function", "web/styles/app.css", `.x { rotate: tan (1deg); }`, ""},
		{"css quoted", "web/styles/app.css", `.x { font-family: "Navy Sans"; }`, ""},
		{"apply quoted", "web/styles/app.css", `.x { content: "@apply bg-[#fff]"; }`, ""},
		{"style quoted", "sample.templ", `<p style='font-family: "Navy Sans"'>`, ""},
		{"data attribute", "sample.templ", `<p data-style="red" data-class="bg-[#fff]">`, ""},
		{"colour reference", "sample.templ", `<p class="text-[var(--white)]" style="color: var(--red)">`, ""},
		{"agent worktree", ".claude/worktrees/pond/sample.go", "package pond", ""},
		{"neutral go", "sample.go", `package ribbitto; var pondering, marshalled, Ponding int; var label = "pond" // ribbit`, ""},
		{"neutral sql", "sample.sql", "-- pond\n/* marsh */ SELECT pondering, 'ribbit', $$lilypad$$, $tag$chorus$tag$;", ""},
		{"neutral route", "sample.go", `package sample; func f() { mux.Handle("GET /pondering/{pond}", handler) }`, ""},
		{"labels", "internal/web/i18n/locales/pond.toml", "pond = 'ribbit'", ""},
		{"docs", "docs/pond.go", "package pond", ""},
		{"vendor", "web/static/vendor/pond.go", "package pond", ""},
		{"generated css", "web/static/css/app.css", `.x { color: red; }`, ""},
		{"image class", "sample.templ", `<p class="bg-[url('/images/red.svg')]">`, ""},
		{"unquoted image class", "sample.templ", `<p class="bg-[url(https://example.com/red.svg)]">`, ""},
		{"token classes", "sample.templ", `<p class="bg-brand text-[var(--color-fg)] bg-[transparent] text-[currentColor] text-[14px]" style="color: var(--rb-fg); background: transparent; border-color: currentColor">`, ""},
		{"token css", "web/styles/app.css", `@source "./*.css"; /* tokens */ :root { --rb-bg: #fff; } @media (x) { :root { --rb-bg: #000; } } @theme { --shadow-card: 0 0 1px black; } .x { color: var(--rb-bg); }`, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			vocabulary := string(labels)
			if tt.name == "table source" {
				vocabulary = strings.ReplaceAll(vocabulary, "ribbit", "splosh")
			}
			for path, source := range map[string]string{"docs/domain/vocabulary.md": vocabulary, tt.path: tt.source} {
				full := filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(source), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			problems, err := check(root)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Join(problems, "\n")
			wantPath := tt.path
			if tt.name == "directory" {
				wantPath = filepath.Dir(tt.path)
			}
			if tt.rule == "" && got != "" || tt.rule != "" && (!strings.Contains(got, wantPath) || !strings.Contains(got, tt.rule)) {
				t.Fatalf("problems %q, want file %q and rule %q", got, tt.path, tt.rule)
			}
		})
	}
}

func TestCurrentTree(t *testing.T) {
	problems, err := check("../..")
	if err != nil || len(problems) != 0 {
		t.Fatalf("problems %q, error %v", problems, err)
	}
}
