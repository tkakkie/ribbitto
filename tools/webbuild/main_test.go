package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evanw/esbuild/pkg/api"
)

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T, source []byte) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "web/src/message-time.ts"), source)
	return root
}

func TestBuild(t *testing.T) {
	source, err := os.ReadFile("../../web/src/message-time.ts")
	if err != nil {
		t.Fatal(err)
	}
	var previous bundle
	var previousBytes []byte
	for i := range 3 {
		if i == 2 {
			source = append(source, []byte("\nconsole.log('changed');\n")...)
		}
		root := fixture(t, source)
		dist := filepath.Join(root, "web/static/dist")
		write(t, filepath.Join(dist, "foreign.js"), nil)
		if err := build(root, "commit", "run", []string{"message-time"}); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dist, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		var m manifest
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		b := m.Entries["message-time"]
		data, err = os.ReadFile(filepath.Join(dist, b.File))
		if err != nil {
			t.Fatal(err)
		}
		files, err := os.ReadDir(dist)
		if err != nil || len(files) != 2 || m.Commit != "commit" || m.RunID != "run" || b.SHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) || !strings.HasPrefix(b.File, "message-time-") || !bytes.Contains(data, []byte("(()=>{")) || bytes.Contains(data, []byte("sourceMappingURL")) {
			t.Fatalf("invalid production output: %+v, %v, %v", m, files, err)
		}
		if i == 1 && (b != previous || !bytes.Equal(data, previousBytes)) || i == 2 && b.File == previous.File {
			t.Fatal("bundle names/bytes violate content hashing")
		}
		previous, previousBytes = b, data
	}
}

func TestValidation(t *testing.T) {
	for _, tc := range []struct{ name, entry, output, file, foreign, want string }{
		{"missing output", "", "", "", "", "entry without output"},
		{"unknown entry", "web/src/other.ts", "web/static/dist/x.js", "x.js", "", "unexplained output"},
		{"foreign file", "web/src/message-time.ts", "web/static/dist/x.js", "x.js", "foreign.js", "foreign file"},
		{"outside output", "web/src/message-time.ts", "web/static/dist/x.js", "../x.js", "", "output outside"},
		{"outside metafile", "web/src/message-time.ts", "web/static/x.js", "x.js", "", "metafile output outside"},
		{"unproduced manifest file", "web/src/message-time.ts", "web/static/dist/missing.js", "x.js", "", "manifest names unproduced"},
		{"unexplained file", "web/src/message-time.ts", "web/static/dist/x.js", "x.js", "", "unexplained produced"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, stage := t.TempDir(), t.TempDir()
			dist := filepath.Join(root, "web/static/dist")
			r := api.BuildResult{Metafile: `{"outputs":{}}`}
			if tc.output != "" {
				r.Metafile = fmtMeta(tc.output, tc.entry)
			}
			if tc.file != "" {
				r.OutputFiles = []api.OutputFile{{Path: filepath.Join(dist, tc.file)}}
			}
			if tc.foreign != "" {
				write(t, filepath.Join(stage, tc.foreign), nil)
			}
			if tc.name == "unexplained file" {
				r.OutputFiles = append(r.OutputFiles, api.OutputFile{Path: filepath.Join(dist, "extra.js")})
			}
			_, err := validate(root, dist, stage, map[string]string{"web/src/message-time.ts": "message-time"}, r)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s, got %v", tc.want, err)
			}
		})
	}
}

func fmtMeta(output, entry string) string {
	return `{"outputs":{"` + output + `":{"entryPoint":"` + entry + `"}}}`
}

func TestRejectedEntries(t *testing.T) {
	for _, name := range []string{"missing", "../escape", `dir\escape`, ""} {
		root := fixture(t, nil)
		path := filepath.Join(root, "web/static/dist/manifest.json")
		write(t, path, []byte("old manifest"))
		err := build(root, "commit", "run", []string{name})
		want := "invalid entry name"
		if name == "missing" {
			want = "reading entry"
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("expected entry rejection for %q: %v", name, err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("failed build retained manifest: %v", err)
		}
	}
	root := fixture(t, []byte(`import("./other.ts");`))
	write(t, filepath.Join(root, "web/src/other.ts"), []byte(`console.log("other");`))
	if err := build(root, "commit", "run", []string{"message-time"}); err == nil || !strings.Contains(err.Error(), "dynamic imports") {
		t.Fatalf("expected dynamic import rejection: %v", err)
	}
}

func TestNoNode(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "webbuild")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("building command: %v: %s", err, out)
	}
	root := fixture(t, []byte(`console.log("classic");`))
	cmd := exec.Command(binary, root, "commit", "run", "message-time")
	cmd.Env = []string{"PATH=" + t.TempDir()}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building without Node: %v: %s", err, out)
	}
}

func TestApplicationModuleExcludesEsbuild(t *testing.T) {
	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join("../..", name))
		if err != nil || bytes.Contains(data, []byte("esbuild")) {
			t.Fatalf("application %s must not depend on esbuild: %v", name, err)
		}
	}
}
