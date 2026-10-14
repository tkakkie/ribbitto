package main

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"testing"
)

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		ok   bool
	}{
		{"valid", []string{"-templ", "../bin/templ", ".."}, true},
		{"equals", []string{"-templ=../bin/templ", ".."}, true},
		{"no arguments", nil, false},
		{"no binary", []string{".."}, false},
		{"empty binary", []string{"-templ=", ".."}, false},
		{"missing value", []string{"-templ"}, false},
		{"no root", []string{"-templ", "../bin/templ"}, false},
		{"empty root", []string{"-templ", "../bin/templ", ""}, false},
		{"extra root", []string{"-templ", "../bin/templ", "..", ".."}, false},
		{"unknown flag", []string{"-unknown", ".."}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, templ, err := parse(tc.args)
			if (err == nil) != tc.ok {
				t.Fatalf("parse error = %v, want success = %v", err, tc.ok)
			}
			if tc.ok && (root != ".." || templ != "../bin/templ") {
				t.Fatalf("parse = (%q, %q)", root, templ)
			}
		})
	}
}

func TestRun(t *testing.T) {
	templ, err := filepath.Abs("../../bin/templ")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(templ); err != nil {
		t.Fatalf("build the pinned formatter with make bin/templ first: %v", err)
	}
	const goodGo = "package fixture\n"
	const badGo = "package fixture\nvar x=1\n"
	const goodTempl = "package fixture\n\ntempl example() {\n\t<p>Hello</p>\n}\n"
	const badTempl = "package fixture\ntempl example(){<p>Hello</p>}\n"
	const goMessage = "These files need gofmt:\n"
	const templMessage = "These files need templ fmt (run ./bin/templ fmt on them):\n"
	const docMessage = "These internal package directories are missing doc.go:\n"
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"formatted", map[string]string{"internal/p/doc.go": goodGo, "internal/p/p.go": goodGo, "p.templ": goodTempl}, ""},
		{"Go", map[string]string{"p.go": badGo}, goMessage + "./p.go"},
		{"Go test", map[string]string{"p_test.go": badGo}, goMessage + "./p_test.go"},
		{"Go testdata", map[string]string{"internal/testdata/p.go": badGo}, goMessage + "./internal/testdata/p.go"},
		{"templ", map[string]string{"p.templ": badTempl}, templMessage + "./p.templ"},
		{"templ testdata", map[string]string{"internal/testdata/p.templ": badTempl}, templMessage + "./internal/testdata/p.templ"},
		{"templ parse error", map[string]string{"p.templ": "not a template"}, templMessage + "./p.templ"},
		{"empty templ", map[string]string{"p.templ": ""}, templMessage + "./p.templ"},
		{"bin skipped", map[string]string{"bin/p.go": badGo, "bin/p.templ": badTempl, "bin/cache/p.templ": "invalid"}, ""},
		{"nested bin checked", map[string]string{"other/bin/p.go": badGo}, goMessage + "./other/bin/p.go"},
		{"missing doc", map[string]string{"internal/p/p.go": goodGo}, docMessage + "internal/p"},
		{"generated needs doc", map[string]string{"internal/p/p.go": "// Code generated. DO NOT EDIT.\n" + goodGo}, docMessage + "internal/p"},
		{"test only", map[string]string{"internal/p/p_test.go": goodGo}, ""},
		{"testdata exempt", map[string]string{"internal/p/testdata/nested/p.go": goodGo}, ""},
		{"commands exempt", map[string]string{"cmd/p/main.go": goodGo}, ""},
		{"Go first", map[string]string{"p.go": badGo, "p.templ": badTempl, "internal/p/p.go": goodGo}, goMessage + "./p.go"},
		{"templ before doc", map[string]string{"p.templ": badTempl, "internal/p/p.go": goodGo}, templMessage + "./p.templ"},
		{"Go list", map[string]string{"z.go": badGo, "a.go": badGo}, goMessage + "./a.go\n./z.go"},
		{"templ list", map[string]string{"z.templ": badTempl, "a.templ": badTempl}, templMessage + "./a.templ\n./z.templ"},
		{"doc list", map[string]string{"internal/z/a.go": goodGo, "internal/a/a.go": goodGo, "internal/a/b.go": goodGo}, docMessage + "internal/a\ninternal/z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for path, source := range tc.files {
				path = filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			err := run(root, templ)
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != tc.want {
				t.Errorf("run = %q, want %q", got, tc.want)
			}
			after := map[string]string{}
			err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
				if err != nil || entry.IsDir() {
					return err
				}
				rel, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				content, err := os.ReadFile(path)
				after[rel] = string(content)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(after, tc.files) {
				t.Fatal("check changed the tree")
			}
		})
	}
}
