package web

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func checkAssets(doc *html.Node) []string {
	var problems []string
	for n := range doc.Descendants() {
		var assetURL string
		switch n.Data {
		case "script":
			assetURL = attr(n, "src")
		case "link":
			if slices.Contains(strings.Fields(strings.ToLower(attr(n, "rel"))), "stylesheet") {
				assetURL = attr(n, "href")
			}
		case "use":
			assetURL = attr(n, "href")
		}
		if !strings.HasPrefix(assetURL, "/static/") {
			continue
		}
		u, err := url.Parse(assetURL)
		if err != nil {
			problems = append(problems, fmt.Sprintf("invalid asset URL %q: %v", assetURL, err))
			continue
		}
		// Queries and SVG fragments are not part of the embedded file path.
		path := strings.TrimPrefix(u.Path, "/static/")
		info, err := fs.Stat(static.FS(), path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("asset %q: %v", assetURL, err))
		} else if info.IsDir() {
			problems = append(problems, fmt.Sprintf("asset %q is a directory, want a file", assetURL))
		} else if n.Data == "use" {
			var sprite struct {
				Symbols []struct {
					ID string `xml:"id,attr"`
				} `xml:"symbol"`
			}
			data, err := fs.ReadFile(static.FS(), path)
			if err == nil {
				err = xml.Unmarshal(data, &sprite)
			}
			found := false
			for _, symbol := range sprite.Symbols {
				found = found || symbol.ID == u.Fragment && u.Fragment != ""
			}
			if err != nil || !found {
				problems = append(problems, fmt.Sprintf("asset %q: missing SVG symbol or invalid sprite: %v", assetURL, err))
			}
		}
	}
	return problems
}

// Inspect every call, including templates not exercised by the page fixtures.
func TestTemplateIcons(t *testing.T) {
	files, err := filepath.Glob("view/*.templ")
	if err != nil {
		t.Fatal(err)
	}
	calls := regexp.MustCompile(`(?s)@Icon\s*\(\s*([^,]+),`)
	for _, file := range files {
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range calls.FindAllSubmatch(source, -1) {
			name, err := strconv.Unquote(strings.TrimSpace(string(call[1])))
			if err != nil {
				t.Errorf("%s: Icon names must be string literals so every reference is checked", file)
				continue
			}
			var markup bytes.Buffer
			if err := view.Icon(name, 24).Render(context.Background(), &markup); err != nil {
				t.Fatal(err)
			}
			doc, err := html.Parse(&markup)
			if err != nil {
				t.Fatal(err)
			}
			svg := find(doc, atom.Svg)
			if svg == nil || attr(svg, "aria-hidden") != "true" || attr(svg, "focusable") != "false" || attr(svg, "fill") != "currentColor" || attr(svg, "width") != "24" || attr(svg, "height") != "24" {
				t.Errorf("%s: icon must be decorative, inherit colour and reserve its requested size", file)
			}
			for _, problem := range checkAssets(doc) {
				t.Errorf("%s: %s", file, problem)
			}
		}
	}
}

func TestCheckIconAssets(t *testing.T) {
	for _, tt := range []struct {
		name, href, want string
	}{
		{"valid", "/static/vendor/fluent-icons-1.1.344.svg?v=1#arrow-up-regular", ""},
		{"missing name", "/static/vendor/fluent-icons-1.1.344.svg#unknown", "missing SVG symbol"},
		{"missing fragment", "/static/vendor/fluent-icons-1.1.344.svg", "missing SVG symbol"},
		{"missing file", "/static/vendor/missing.svg#arrow-up-regular", "asset"},
		{"directory", "/static/vendor#arrow-up-regular", "directory"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := html.Parse(strings.NewReader(`<svg><use href="` + tt.href + `"></use></svg>`))
			if err != nil {
				t.Fatal(err)
			}
			problems := checkAssets(doc)
			if tt.want == "" && len(problems) != 0 || tt.want != "" && (len(problems) != 1 || !strings.Contains(problems[0], tt.want)) {
				t.Fatalf("problems %q, want %q", problems, tt.want)
			}
		})
	}
}
