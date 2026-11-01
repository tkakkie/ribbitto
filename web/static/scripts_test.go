package static_test

import (
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
		{"missing bundle", entry, "", "other.js", "", "reading script bundle"},
		{"missing source map", devEntry, "", file, "", "reading source map"},
		{"invalid source map filename", fmt.Sprintf(`"message-time":{"file":%q,"sha256":"%x","map":"other.js.map"}`, file, sum), "", file, "other.js.map", "invalid source map filename"},
		{"unknown entry", entry + `,"other":{}`, "", file, "", "unknown entry"},
		{"unknown previous entry", devEntry, `[{"other":{"file":"other-OLDHASH1.js"}}]`, file, file + ".map", "unknown entry"},
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
}
