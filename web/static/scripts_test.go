package static_test

import (
	"crypto/sha256"
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
	entry := fmt.Sprintf(`"message-time":{"file":%q,"sha256":"%x"}`, file, sha256.Sum256([]byte(bundle)))
	for _, tc := range []struct{ name, entries, file, want string }{
		{"valid", entry, file, ""},
		{"missing declaration", "", file, "missing declared entry"},
		{"missing bundle", entry, "other.js", "reading script bundle"},
		{"unknown entry", entry + `,"other":{}`, file, "unknown entry"},
		{"invalid filename", `"message-time":{"file":"../escape.js"}`, file, "invalid bundle filename"},
		{"wrong content", `"message-time":{"file":"message-time-ABCDEFGH.js","sha256":"bad"}`, file, "hash mismatch"},
		{"invalid JSON", "{", file, "decoding script manifest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assets := fstest.MapFS{
				"dist/manifest.json": {Data: []byte(`{"entries":{` + tc.entries + `}}`)},
				"dist/" + tc.file:    {Data: []byte(bundle)},
			}
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
	source := filepath.Join(root, "web/src/message-time.ts")
	if err := os.MkdirAll(filepath.Dir(source), 0755); err != nil {
		t.Fatal(err)
	}
	previous := ""
	for _, text := range []string{`console.log("first");`, `console.log("changed");`} {
		if err := os.WriteFile(source, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary, root, "commit", "run", "message-time")
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
}
