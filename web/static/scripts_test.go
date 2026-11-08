package static_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/tkakkie/ribbitto/web/static"
)

func TestScriptManifest(t *testing.T) {
	const file = "message-time-ABCDEFGH.js"
	const bundle = "(()=>{})();"
	sum := sha256.Sum256([]byte(bundle))
	var embedded struct{ Entries map[string]struct{} }
	if data, err := fs.ReadFile(static.FS(), "dist/manifest.json"); err != nil || json.Unmarshal(data, &embedded) != nil {
		t.Fatal("reading embedded manifest")
	}
	entry := fmt.Sprintf(`"message-time":{"file":%q,"sha256":"%x"}`, file, sum)
	devEntry := fmt.Sprintf(`"message-time":{"file":%q,"sha256":"%x","map":%q}`, file, sum, file+".map")
	for _, tc := range []struct {
		name, entries, previous, file, mapFile, want string
	}{
		{"valid", entry, "", file, "", ""},
		{"valid development map", devEntry, "", file, file + ".map", ""},
		{"valid previous generation", devEntry, `[{"message-time":{"file":"message-time-OLDHASH1.js","map":"message-time-OLDHASH1.js.map"}}]`, file, file + ".map", ""},
		{"missing declaration", "", "", file, "", "missing declared entry"},
		{"missing composer", entry, "", file, "", "missing declared entry"},
		{"missing bundle", entry, "", "other.js", "", "reading script bundle"},
		{"missing source map", devEntry, "", file, "", "reading source map"},
		{"invalid source map filename", fmt.Sprintf(`"message-time":{"file":%q,"sha256":"%x","map":"other.js.map"}`, file, sum), "", file, "other.js.map", "invalid source map filename"},
		{"unknown entry", entry + `,"other":{}`, "", file, "", "unknown entry"},
		{"invalid previous entry name", devEntry, `[{"../other":{"file":"../other-OLDHASH1.js"}}]`, file, file + ".map", "invalid bundle filename"},
		{"invalid previous filename", devEntry, `[{"message-time":{"file":"../escape.js"}}]`, file, file + ".map", "invalid bundle filename"},
		{"invalid previous map filename", devEntry, `[{"message-time":{"file":"message-time-OLDHASH1.js","map":"other.js.map"}}]`, file, file + ".map", "invalid source map filename"},
		{"invalid filename", `"message-time":{"file":"../escape.js"}`, "", file, "", "invalid bundle filename"},
		{"wrong content", `"message-time":{"file":"message-time-ABCDEFGH.js","sha256":"bad"}`, "", file, "", "hash mismatch"},
		{"invalid JSON", "{", "", file, "", "decoding script manifest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := tc.entries
			assets := fstest.MapFS{"dist/" + tc.file: {Data: []byte(bundle)}}
			if tc.mapFile != "" {
				assets["dist/"+tc.mapFile] = &fstest.MapFile{Data: []byte("{}")}
			}
			for name := range embedded.Entries {
				if name == "composer" && tc.name == "missing composer" {
					continue
				}
				if name != "message-time" && entries != "" && entries != "{" {
					entries += fmt.Sprintf(`,%q:{"file":"%s-ABCDEFGH.js","sha256":"%x"}`, name, name, sum)
					assets["dist/"+name+"-ABCDEFGH.js"] = &fstest.MapFile{Data: []byte(bundle)}
				}
			}
			raw := `{"entries":{` + entries + `}`
			if tc.previous != "" {
				raw += `,"previous":` + tc.previous
			}
			if entries != "{" {
				raw += `}`
			}
			assets["dist/manifest.json"] = &fstest.MapFile{Data: []byte(raw)}
			scripts, err := static.NewScripts(assets)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("want %s, got %v", tc.want, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx := static.WithScripts(t.Context(), scripts)
			url, err := static.ScriptURL(ctx, "message-time")
			if err != nil || url != "/static/dist/"+file || !scripts.IsBundle("dist/"+file) {
				t.Fatalf("URL = %q, %v", url, err)
			}
			if scripts.IsMap("dist/"+file+".map") != (tc.mapFile != "") || scripts.IsMap("dist/manifest.json") {
				t.Fatalf("IsMap = %v for mapFile %q", scripts.IsMap("dist/"+file+".map"), tc.mapFile)
			}
			if tc.previous != "" && (!scripts.IsBundle("dist/message-time-OLDHASH1.js") || !scripts.IsMap("dist/message-time-OLDHASH1.js.map") || scripts.IsBundle("dist/message-time-00000000.js")) {
				t.Fatal("previous generation bundle/map registration mismatch")
			}
			if _, err := static.ScriptURL(ctx, "undeclared"); err == nil {
				t.Fatal("undeclared script accepted")
			}
			if scripts.IsBundle("dist/manifest.json") {
				t.Fatal("manifest is a public bundle")
			}
		})
	}
	if _, err := static.NewScripts(fstest.MapFS{}); err == nil {
		t.Fatal("missing manifest accepted")
	}
}

func TestEmbeddedProductionScripts(t *testing.T) {
	scripts, err := static.NewScripts(static.FS())
	if err != nil {
		t.Fatal(err)
	}
	url, err := static.ScriptURL(t.Context(), "message-time")
	if err != nil || !scripts.IsBundle(strings.TrimPrefix(url, "/static/")) {
		t.Fatalf("embedded URL = %q, %v", url, err)
	}
	err = fs.WalkDir(static.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, ".map") {
			t.Errorf("embedded source map: %s", path)
		}
		if !entry.IsDir() && strings.HasPrefix(path, "dist/") && strings.HasSuffix(path, ".js") {
			data, err := fs.ReadFile(static.FS(), path)
			if err != nil {
				return err
			}
			if strings.Contains(string(data), "sourceMappingURL") {
				t.Errorf("source-map link in %s", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir("dist", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, ".map") || bytes.Contains(data, []byte("sourceMappingURL")) || bytes.Contains(data, []byte(".js.map")) {
			t.Errorf("public dist leak in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("../../bin/sourcemaps/index.json"); err != nil {
		t.Errorf("missing production source map index in bin/sourcemaps/index.json: %v", err)
	}
	if out, err := exec.Command("git", "-C", "../..", "ls-files", "--", "*.map").CombinedOutput(); err != nil || len(bytes.TrimSpace(out)) != 0 {
		t.Errorf("tracked .map files in git: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "check-ignore", "-q", "../../bin/sourcemaps/index.json").CombinedOutput(); err != nil {
		t.Errorf("bin/sourcemaps not ignored by git: %v: %s", err, out)
	}
	yml, err := filepath.Glob("../../.github/workflows/*.yml")
	yaml, err2 := filepath.Glob("../../.github/workflows/*.yaml")
	workflows := append(yml, yaml...)
	if err != nil || err2 != nil || len(workflows) == 0 {
		t.Fatalf("listing workflows: %v / %v", err, err2)
	}
	var hasStoreWF bool
	for _, wf := range workflows {
		data, err := os.ReadFile(wf)
		if err != nil {
			t.Fatal(err)
		}
		isStore := filepath.Base(wf) == "sourcemaps-store.yml"
		hasStoreWF = hasStoreWF || isStore
		if bytes.Contains(data, []byte("upload-artifact")) || bytes.Contains(data, []byte("bin/sourcemaps")) || bytes.Count(data, []byte("make sourcemaps-store")) != map[bool]int{true: 1}[isStore] {
			t.Errorf("workflow %s exposes or miswires source maps", wf)
		}
	}
	makefile, err := os.ReadFile("../../Makefile")
	if err != nil || !hasStoreWF || bytes.Count(makefile, []byte("bin/sourcemaps")) != 1 || !bytes.Contains(makefile, []byte("\nsourcemaps-store:\n\tgo -C tools run ./sourcemaps ../bin/sourcemaps ")) {
		t.Errorf("unexpected sourcemaps wiring in workflow or Makefile: %v", err)
	}
}

func TestSourceChangeUpdatesScriptURL(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "webbuild")
	cmd := exec.Command("go", "-C", "../../tools", "build", "-o", binary, "./webbuild")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building asset builder: %v: %s", err, out)
	}
	root := t.TempDir()
	if err := os.CopyFS(filepath.Join(root, "web/src"), os.DirFS("../src")); err != nil {
		t.Fatal(err)
	}
	registry, err := os.ReadFile("scripts.go")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "web/static"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "web/static/scripts.go"), registry, 0644); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "web/src/message-time.ts")
	previous := ""
	for _, text := range []string{`console.log("first");`, `console.log("changed");`} {
		if err := os.WriteFile(source, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary, root, "commit", "run")
		cmd.Env = []string{"PATH=" + t.TempDir()}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("generating without Node: %v: %s", err, out)
		}
		scripts, err := static.NewScripts(os.DirFS(filepath.Join(root, "web/static")))
		if err != nil {
			t.Fatal(err)
		}
		url, err := scripts.URL("message-time")
		if err != nil || url == "" || url == previous {
			t.Fatalf("source change did not change URL: %q, %v", url, err)
		}
		previous = url
	}
	prodManifest, err := os.ReadFile(filepath.Join(root, "web/static/dist/manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	devCmd := exec.Command(binary, "-dev", root)
	devCmd.Env = []string{"PATH=" + t.TempDir()}
	if out, err := devCmd.CombinedOutput(); err != nil {
		t.Fatalf("generating dev bundles: %v: %s", err, out)
	}
	afterManifest, err := os.ReadFile(filepath.Join(root, "web/static/dist/manifest.json"))
	if err != nil || string(afterManifest) != string(prodManifest) {
		t.Fatalf("dev build altered production manifest: %v", err)
	}
	if scripts, err := static.NewScripts(os.DirFS(filepath.Join(root, "web/static"))); err != nil {
		t.Fatalf("production scripts invalid after dev build: %v", err)
	} else if url, err := scripts.URL("message-time"); err != nil || url != previous || scripts.IsMap(strings.TrimPrefix(url, "/static/")+".map") {
		t.Fatalf("production URL or map changed after dev build: %q, %v", url, err)
	}
	// Publish an old per-script generation, then migrate back to the current registry.
	oldRegistry := strings.ReplaceAll(string(registry), `"message-time"`, `"retired-composer"`)
	if err := os.WriteFile(filepath.Join(root, "web/static/scripts.go"), []byte(oldRegistry), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "web/src/retired-composer.ts"), []byte(`console.log("old generation")`), 0644); err != nil {
		t.Fatal(err)
	}
	buildDev := func() {
		t.Helper()
		if out, err := exec.Command(binary, "-dev", root).CombinedOutput(); err != nil {
			t.Fatalf("building dev migration: %v: %s", err, out)
		}
	}
	buildDev()
	devRoot := filepath.Join(root, "bin/dev-dist")
	var old struct {
		Entries map[string]struct{ File, Map string }
	}
	data, err := os.ReadFile(filepath.Join(devRoot, "manifest.json"))
	if err != nil || json.Unmarshal(data, &old) != nil {
		t.Fatal("reading old development manifest")
	}
	retired := old.Entries["retired-composer"]
	if retired.File == "" || retired.Map == "" {
		t.Fatal("old generation missing bundle/map")
	}
	if err := os.WriteFile(filepath.Join(root, "web/static/scripts.go"), registry, 0644); err != nil {
		t.Fatal(err)
	}
	buildDev()
	devAssets := fstest.MapFS{}
	files, err := os.ReadDir(devRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(devRoot, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		devAssets["dist/"+file.Name()] = &fstest.MapFile{Data: data}
	}
	scripts, err := static.NewScripts(devAssets)
	if err != nil {
		t.Fatal(err)
	}
	if !scripts.IsBundle("dist/"+retired.File) || !scripts.IsMap("dist/"+retired.Map) {
		t.Fatal("retired bundle/map unavailable")
	}
	for _, file := range []string{retired.File, retired.Map} {
		if _, err := os.ReadFile(filepath.Join(devRoot, file)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := scripts.URL("retired-composer"); err == nil {
		t.Fatal("retired entry became current")
	}

}
