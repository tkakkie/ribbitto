package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImages(t *testing.T) {
	for _, tt := range []struct {
		name  string
		files map[string]string
		want  string // a substring of the only problem, or "" for none
	}{
		{"no images directory", map[string]string{"README.md": "# Readme"}, ""},
		{"oversize", map[string]string{"README.md": "![alt](docs/images/big.webp)", "docs/images/big.webp": strings.Repeat("a", imageLimit+1)}, "docs/images/big.webp: 307201 bytes, over its limit of 307200"},
		{"unlinked oversize", map[string]string{"docs/images/big.png": strings.Repeat("a", imageLimit+1)}, "docs/images/big.png: 307201 bytes"},
		{"at the limit", map[string]string{"README.md": "![alt](docs/images/x.webp)", "docs/images/x.webp": strings.Repeat("a", imageLimit)}, ""},
		{"missing file", map[string]string{"README.md": "![alt](docs/images/x.png)"}, "README.md:1: docs/images/x.png does not exist"},
		{"https URL", map[string]string{"README.md": "![alt](https://host/x.png)"}, "external image URL"},
		{"https single slash scheme", map[string]string{"README.md": "![alt](https:/../docs/images/x.png)", "docs/images/x.png": "x"}, "external image URL"},
		{"x single slash scheme", map[string]string{"README.md": "![alt](x:/../docs/images/x.png)", "docs/images/x.png": "x"}, "external image URL"},
		{"javascript single slash scheme", map[string]string{"README.md": "![alt](javascript:/../docs/images/x.png)", "docs/images/x.png": "x"}, "external image URL"},
		{"scheme relative URL", map[string]string{"README.md": "![alt](//host/x.png)"}, "external image URL"},
		{"mailto URL", map[string]string{"README.md": "![alt](mailto:x.png)"}, "external image URL"},
		{"data URL", map[string]string{"README.md": "![alt](data:image/png;base64,eA==)"}, "external image URL"},
		{"absolute path", map[string]string{"README.md": "![alt](/docs/images/x.png)", "docs/images/x.png": "x"}, "absolute image path"},
		{"wrong root directory", map[string]string{"README.md": "![alt](images/x.png)", "images/x.png": "x"}, "images/x.png outside docs/images/"},
		{"wrong docs directory", map[string]string{"README.md": "![alt](docs/x.png)", "docs/x.png": "x"}, "docs/x.png outside docs/images/"},
		{"wrong web directory", map[string]string{"README.md": "![alt](web/x.png)", "web/x.png": "x"}, "web/x.png outside docs/images/"},
		{"normalises outside images", map[string]string{"README.md": "![alt](docs/images/../x.png)", "docs/x.png": "x"}, "docs/x.png outside docs/images/"},
		{"two parents", map[string]string{"README.md": "![alt](../../x.png)"}, "../../x.png escapes repository root"},
		{"climbs out", map[string]string{"README.md": "![alt](../x.png)"}, "../x.png escapes repository root"},
		{"subdirectory", map[string]string{"README.md": "![alt](docs/images/sub/x.png)", "docs/images/sub/x.png": "x"}, "docs/images/sub/x.png outside docs/images/"},
		{"backslash destination", map[string]string{"README.md": `![alt](docs\images\x.png)`, "docs/images/x.png": "x"}, "outside docs/images/"},
		{"jpg format", map[string]string{"README.md": "![alt](docs/images/x.jpg)", "docs/images/x.jpg": "x"}, "not a PNG or WebP image"},
		{"gif format", map[string]string{"README.md": "![alt](docs/images/x.gif)", "docs/images/x.gif": "x"}, "not a PNG or WebP image"},
		{"svg format", map[string]string{"README.md": "![alt](docs/images/x.svg)", "docs/images/x.svg": "x"}, "not a PNG or WebP image"},
		{"uppercase format", map[string]string{"README.md": "![alt](docs/images/x.PNG)", "docs/images/x.PNG": "x"}, "not a PNG or WebP image"},
		{"no extension", map[string]string{"README.md": "![alt](docs/images/x)", "docs/images/x": "x"}, "not a PNG or WebP image"},
		{"empty alt", map[string]string{"README.md": "# Readme\n\n![](docs/images/x.png)", "docs/images/x.png": "x"}, "README.md:3: image has no alt text"},
		{"empty alt later paragraph line", map[string]string{"README.md": "# Readme\n\nParagraph first line\n![](docs/images/x.png)", "docs/images/x.png": "x"}, "README.md:4: image has no alt text"},
		{"whitespace alt", map[string]string{"README.md": "![ ](docs/images/x.png)", "docs/images/x.png": "x"}, "README.md:1: image has no alt text"},
		{"rendered whitespace alt", map[string]string{"README.md": "![&#32;](docs/images/x.png)", "docs/images/x.png": "x"}, "image has no alt text"},
		{"README relative WebP", map[string]string{"README.md": "![alt](docs/images/screenshot.webp)", "docs/images/screenshot.webp": "x"}, ""},
		{"docs relative PNG", map[string]string{"docs/development.md": "![alt](images/screenshot.png)", "docs/images/screenshot.png": "x"}, ""},
		{"docs relative WebP", map[string]string{"docs/development.md": "![alt](images/screenshot.webp)", "docs/images/screenshot.webp": "x"}, ""},
		{"normalises into images", map[string]string{"README.md": "![alt](docs/images/../images/screenshot.png)", "docs/images/screenshot.png": "x"}, ""},
		{"nested Markdown parent", map[string]string{"docs/a/b.md": "![alt](../images/x.png)", "docs/images/x.png": "x"}, ""},
		{"encoded path", map[string]string{"README.md": "![alt](docs/images/%73creenshot.png)", "docs/images/screenshot.png": "x"}, ""},
		{"encoded escape", map[string]string{"README.md": "![alt](docs/images%2F..%2Fx.png)", "docs/x.png": "x"}, "docs/x.png outside docs/images/"},
		{"fragment", map[string]string{"README.md": "![alt](docs/images/x.png#preview)", "docs/images/x.png": "x"}, ""},
		{"query", map[string]string{"README.md": "![alt](docs/images/x.png?raw=1)", "docs/images/x.png": "x"}, ""},
		{"colon after slash", map[string]string{"README.md": "![alt](docs/images/a:b.png)", "docs/images/a:b.png": "x"}, ""},
		{"angle bracket destination", map[string]string{"README.md": "![alt](<docs/images/x.png>)", "docs/images/x.png": "x"}, ""},
		{"space in angle bracket destination", map[string]string{"README.md": "![alt](<docs/images/my x.png>)", "docs/images/my x.png": "x"}, ""},
		{"table cell missing image", map[string]string{"README.md": "| Image |\n| --- |\n| ![alt](docs/images/missing.png) |"}, "does not exist"},
		{"reference image", map[string]string{"README.md": "![alt][r]\n\n[r]: docs/images/x.png", "docs/images/x.png": "x"}, ""},
		{"reference image missing target", map[string]string{"README.md": "![alt][r]\n\n[r]: docs/images/missing.png"}, "does not exist"},
		{"collapsed reference image", map[string]string{"README.md": "![alt][]\n\n[alt]: docs/images/x.png", "docs/images/x.png": "x"}, ""},
		{"shortcut reference image", map[string]string{"README.md": "![alt]\n\n[alt]: docs/images/x.png", "docs/images/x.png": "x"}, ""},
		{"unused reference", map[string]string{"README.md": "[r]: docs/images/missing.png"}, ""},
		{"emphasis alt", map[string]string{"README.md": "![*chat* **screen**](docs/images/x.png)", "docs/images/x.png": "x"}, ""},
		{"fenced image ignored", map[string]string{"README.md": "```\n![](missing.png)\n```"}, ""},
		{"code span ignored", map[string]string{"README.md": "`![](missing.png)`"}, ""},
		{"schema image ignored", map[string]string{"docs/schema/x.md": "![](missing.png)"}, ""},
		{"ordinary link ignored", map[string]string{"README.md": "[alt](docs/images/missing.png)"}, ""},
		{"directory is not a file", map[string]string{"README.md": "![alt](docs/images/x.png)", "docs/images/x.png/child": "x"}, "does not exist as a regular file"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			problems, err := checkImages(repo(t, tt.files))
			if err != nil {
				t.Fatal(err)
			}
			if tt.want == "" && len(problems) != 0 || tt.want != "" && (len(problems) != 1 || !strings.Contains(problems[0], tt.want)) {
				t.Fatalf("problems %q, want %q", problems, tt.want)
			}
		})
	}
}

func TestImageSymlink(t *testing.T) {
	root := repo(t, map[string]string{"README.md": "![alt](docs/images/x.png)", "docs/images/real.png": "x"})
	if err := os.Symlink("real.png", filepath.Join(root, "docs/images/x.png")); err != nil {
		t.Fatal(err)
	}
	problems, err := checkImages(root)
	if err != nil || len(problems) != 1 || !strings.Contains(problems[0], "does not exist as a regular file") {
		t.Fatalf("symlink: problems %q, error %v", problems, err)
	}
}
