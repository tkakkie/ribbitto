package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

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

func declare(t *testing.T, root string, names ...string) {
	t.Helper()
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = fmt.Sprintf("%q", name)
	}
	script := fmt.Sprintf("package static\nfunc NewScripts() {\n\tnames := []string{%s}\n\t_ = names\n}\n", strings.Join(quoted, ", "))
	write(t, filepath.Join(root, "web/static/scripts.go"), []byte(script))
}

func fixture(t *testing.T, source []byte) string {
	t.Helper()
	root := t.TempDir()
	declare(t, root, "message-time")
	write(t, filepath.Join(root, "web/src/message-time.ts"), source)
	write(t, filepath.Join(root, "web/src/composer-queue.ts"), []byte("export const queued = true;\n"))
	write(t, filepath.Join(root, "web/src/contracts.gen.ts"), []byte("export const id = 'x';\n"))
	write(t, filepath.Join(root, "web/src/globals.d.ts"), []byte("declare var htmx: unknown;\n"))
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
		if err := build(root, "commit", "run", false); err != nil {
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
		if err != nil || len(files) != 2 || len(m.Entries) != 1 || m.Commit != "commit" || m.RunID != "run" || b.Map != "" || b.SHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) || !strings.HasPrefix(b.File, "message-time-") || !bytes.Contains(data, []byte("(()=>{")) || bytes.Contains(data, []byte("sourceMappingURL")) {
			t.Fatalf("invalid production output: %+v, %v, %v", m, files, err)
		}
		if i == 1 && (b != previous || !bytes.Equal(data, previousBytes)) || i == 2 && b.File == previous.File {
			t.Fatal("bundle names/bytes violate content hashing")
		}
		previous, previousBytes = b, data
	}
	declared, err := declaredEntries("../..")
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := os.CopyFS(filepath.Join(repo, "web/src"), os.DirFS("../../web/src")); err != nil {
		t.Fatal(err)
	}
	registry, err := os.ReadFile("../../web/static/scripts.go")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, "web/static/scripts.go"), registry)
	if err := build(repo, "commit", "run", false); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(filepath.Join(repo, "web/static/dist"))
	if err != nil || len(files) != len(declared)+1 {
		t.Fatalf("unexpected repository bundle count: %v, %v", files, err)
	}
}

type chanWriter chan string

func (w chanWriter) Write(p []byte) (int, error) {
	w <- string(p)
	return len(p), nil
}

func TestBuildDevelopmentAndWatch(t *testing.T) {
	root := fixture(t, []byte("const first = 'alpha';\nconsole.log(first);\n"))
	dist := filepath.Join(root, "web/static/dist")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errs := make(chanWriter, 32)
	done := make(chan error, 1)
	go func() { done <- watch(ctx, root, errs) }()

	waitBundle := func(prev string) (bundle, []byte, []byte) {
		t.Helper()
		for range 250 {
			var m manifest
			if data, err := os.ReadFile(filepath.Join(dist, "manifest.json")); err == nil && json.Unmarshal(data, &m) == nil {
				b := m.Entries["message-time"]
				js, err1 := os.ReadFile(filepath.Join(dist, b.File))
				sm, err2 := os.ReadFile(filepath.Join(dist, b.Map))
				if b.File != "" && b.File != prev && b.Map == b.File+".map" && err1 == nil && err2 == nil {
					return b, js, sm
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for bundle change from %q", prev)
		return bundle{}, nil, nil
	}

	b1, js1, sm1 := waitBundle("")
	lock := filepath.Join(root, "web/src/.#message-time.ts")
	if err := os.Symlink(filepath.Join(root, "web/src/missing.ts"), lock); err != nil {
		t.Fatal(err)
	}
	if msg := <-errs; !strings.Contains(msg, ".#message-time.ts") {
		t.Fatalf("unexpected snapshot error: %s", msg)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "web/src/message-time.ts"), []byte("const broken = ;\n"))
	for {
		if msg := <-errs; strings.Contains(msg, "building bundles:") {
			break
		}
	}
	if kept, _, _ := waitBundle(""); kept != b1 {
		t.Fatalf("failed rebuild altered previous bundle: got %+v, want %+v", kept, b1)
	}
	write(t, filepath.Join(root, "web/src/message-time.ts"), []byte("const second = 'beta';\nconsole.log(second);\n"))
	b2, js2, sm2 := waitBundle(b1.File)
	if !bytes.Contains(js1, []byte("//# sourceMappingURL="+b1.Map)) || !bytes.Contains(sm1, []byte(`"sourcesContent"`)) || !bytes.Contains(sm1, []byte("alpha")) || !bytes.Contains(js2, []byte("//# sourceMappingURL="+b2.Map)) || !bytes.Contains(sm2, []byte("beta")) {
		t.Fatalf("development bundle or map missing expected content: %s / %s", js2, sm2)
	}
	if _, err := os.Stat(filepath.Join(dist, b1.File)); !os.IsNotExist(err) {
		t.Fatalf("stale development bundle survived rebuild: %v", err)
	}
	cancel()
	if err := <-done; err != nil || build(root, "commit", "run", false) != nil {
		t.Fatalf("watch or production replacement failed: %v", err)
	}
	files, err := os.ReadDir(dist)
	data, err2 := os.ReadFile(filepath.Join(dist, "manifest.json"))
	if err != nil || err2 != nil || len(files) != 2 || bytes.Contains(data, []byte(`"map"`)) {
		t.Fatalf("production replacement failed: %v, %s", files, data)
	}
}

func TestBuildSecondDeclaredEntry(t *testing.T) {
	root := fixture(t, []byte(`console.log("first");`))
	declare(t, root, "message-time", "second-entry")
	write(t, filepath.Join(root, "web/src/second-entry.ts"), []byte(`console.log("second");`))
	if err := build(root, "commit", "run", false); err != nil {
		t.Fatal(err)
	}
	dist := filepath.Join(root, "web/static/dist")
	data, err := os.ReadFile(filepath.Join(dist, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(dist)
	if err != nil || len(files) != 3 || len(m.Entries) != 2 {
		t.Fatalf("expected two bundles and manifest: %+v, %v, %v", m, files, err)
	}
	for _, name := range []string{"message-time", "second-entry"} {
		b := m.Entries[name]
		bundleData, err := os.ReadFile(filepath.Join(dist, b.File))
		if err != nil || !strings.HasPrefix(b.File, name+"-") || b.SHA256 != fmt.Sprintf("%x", sha256.Sum256(bundleData)) {
			t.Fatalf("invalid bundle for %s: %+v, %v", name, b, err)
		}
	}
}

func TestBuildClassicIIFE(t *testing.T) {
	root := fixture(t, []byte(`const value = 1; console.log(value);`))
	if err := build(root, "commit", "run", false); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(root, "web/static/dist/*.js"))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected one classic bundle: %v, %v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("(()=>{")) || bytes.Contains(data, []byte("export")) || bytes.Contains(data, []byte("import")) {
		t.Fatalf("expected classic IIFE without module statements, got %s", data)
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
			_, err := validate(root, dist, stage, map[string]string{"web/src/message-time.ts": "message-time"}, r, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s, got %v", tc.want, err)
			}
		})
	}
	t.Run("missing development map", func(t *testing.T) {
		root, stage := t.TempDir(), t.TempDir()
		dist := filepath.Join(root, "web/static/dist")
		r := api.BuildResult{
			Metafile:    fmtMeta("web/static/dist/x.js", "web/src/message-time.ts"),
			OutputFiles: []api.OutputFile{{Path: filepath.Join(dist, "x.js")}},
		}
		if _, err := validate(root, dist, stage, map[string]string{"web/src/message-time.ts": "message-time"}, r, true); err == nil || !strings.Contains(err.Error(), "bundle without source map") {
			t.Fatalf("expected missing source map error, got %v", err)
		}
	})
}

func fmtMeta(output, entry string) string {
	return `{"outputs":{"` + output + `":{"entryPoint":"` + entry + `"}}}`
}

func TestRejectedEntries(t *testing.T) {
	for _, name := range []string{"missing", "../escape", `dir\escape`, "", "message-time"} {
		root := fixture(t, []byte(`console.log("ok");`))
		declare(t, root, "message-time", name)
		path := filepath.Join(root, "web/static/dist/manifest.json")
		write(t, path, []byte("old manifest"))
		err := build(root, "commit", "run", false)
		want := "invalid entry name"
		switch name {
		case "missing":
			want = "reading entry"
		case "message-time":
			want = "duplicate entry"
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("expected entry rejection for %q: %v", name, err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("failed build retained manifest: %v", err)
		}
	}
	for _, tc := range []struct{ script, want string }{
		{"package static\nfunc NewScripts() {}\n", "missing entry declaration"},
		{"package static\nfunc NewScripts() {\n\tvar names = []string{\"message-time\", \"second-entry\"}\n\t{\n\t\tnames := []string{\"message-time\"}\n\t\t_ = names\n\t}\n\t_ = names\n}\n", "missing entry declaration"},
		{"package static\nfunc NewScripts() { names := []string{}; _ = names }\n", "entries are required"},
		{"package static\nfunc NewScripts() {\n\tnames := []string{\"a\"}\n\t{\n\t\tnames := []string{\"b\"}\n\t\t_ = names\n\t}\n\t_ = names\n}\n", "multiple entry declarations"},
		{"package static\nfunc NewScripts() { names := [1]string{\"a\"}; _ = names }\n", "string slice literal"},
		{"package static\nfunc NewScripts() { entry := \"a\"; names := []string{entry}; _ = names }\n", "string literal"},
	} {
		root := fixture(t, []byte(`console.log("ok");`))
		path := filepath.Join(root, "web/static/dist/manifest.json")
		write(t, path, []byte("old manifest"))
		write(t, filepath.Join(root, "web/static/scripts.go"), []byte(tc.script))
		if err := build(root, "commit", "run", false); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("want %s, got %v", tc.want, err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("failed build retained manifest: %v", err)
		}
	}
	root := fixture(t, []byte(`import("./other.ts");`))
	write(t, filepath.Join(root, "web/src/other.ts"), []byte(`console.log("other");`))
	if err := build(root, "commit", "run", false); err == nil || !strings.Contains(err.Error(), "dynamic imports") {
		t.Fatalf("expected dynamic import rejection: %v", err)
	}
	t.Run("non-literal dynamic import", func(t *testing.T) {
		root := fixture(t, []byte(`declare const x: string; import(x);`))
		if err := build(root, "commit", "run", false); err == nil || !strings.Contains(err.Error(), "dynamic imports are not supported") {
			t.Fatalf("expected non-literal dynamic import rejection: %v", err)
		}
	})
}

func TestNoNode(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "webbuild")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("building command: %v: %s", err, out)
	}
	root := fixture(t, []byte(`console.log("classic");`))
	if out, err := exec.Command(binary, root, "commit", "run", "extra").CombinedOutput(); err == nil || !bytes.Contains(out, []byte("usage: webbuild <repository root> <commit SHA> <run id>")) {
		t.Fatalf("accepted entry argument on command line: %v: %s", err, out)
	}
	cmd := exec.Command(binary, root, "commit", "run")
	cmd.Env = []string{"PATH=" + t.TempDir()}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building without Node: %v: %s", err, out)
	}
	watchCmd := exec.Command(binary, "-watch", root)
	watchCmd.Env = []string{"PATH=" + t.TempDir()}
	if err := watchCmd.Start(); err != nil {
		t.Fatal(err)
	}
	for range 250 {
		if data, err := os.ReadFile(filepath.Join(root, "web/static/dist/manifest.json")); err == nil && bytes.Contains(data, []byte(`"map"`)) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := watchCmd.Process.Signal(syscall.SIGTERM); err != nil || watchCmd.Wait() != nil {
		t.Fatalf("watch process exit on SIGTERM: %v", err)
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
