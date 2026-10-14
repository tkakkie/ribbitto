// Command buildicons rebuilds the pinned Fluent icon sprite and its licence in
// web/static/vendor from the pinned npm tarball and licence, refusing either
// on a SHA-256 mismatch. It is development only: the server never runs it,
// and nothing else in the repository needs npm. Run it with the repository
// root as its argument.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	version    = "1.1.344"
	tarball    = "https://registry.npmjs.org/@fluentui/svg-icons/-/svg-icons-" + version + ".tgz"
	tarballSHA = "6fe61f489a6d0d065ec2182794fdadf0e15db7d3388997c5ba8039dcfaaa35e8"
	// npm omits LICENSE; gitHead in the pinned package metadata identifies its source.
	licenceURL = "https://raw.githubusercontent.com/microsoft/fluentui-system-icons/f1260014e8a4fcfa6777182fd6cf082fc5e952d8/LICENSE"
	licenceSHA = "69bc45dc42b9acb96a69823adbc6ae538374e3c0bde169b855b32c48eaaef52f"
	namespace  = "http://www.w3.org/2000/svg"
)

var icons = []string{
	"home", "add", "search", "settings", "people", "panel_right", "info",
	"chevron_down", "chevron_right", "checkmark", "send", "arrow_up",
	"branch", "dismiss", "pin", "sign_out",
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: buildicons <repository root>")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "buildicons:", err)
		os.Exit(1)
	}
}

func run(root string) error {
	client := &http.Client{Timeout: time.Minute}
	archive, err := fetch(client, tarball, tarballSHA)
	if err != nil {
		return err
	}
	licence, err := fetch(client, licenceURL, licenceSHA)
	if err != nil {
		return err
	}
	sprite, err := build(archive, icons)
	if err != nil {
		return err
	}
	vendor := filepath.Join(root, "web/static/vendor")
	if err := os.WriteFile(filepath.Join(vendor, "fluent-icons-"+version+".svg"), sprite, 0o644); err != nil {
		return fmt.Errorf("writing the sprite: %w", err)
	}
	if err := os.WriteFile(filepath.Join(vendor, "fluent-icons-LICENSE"), licence, 0o644); err != nil {
		return fmt.Errorf("writing the licence: %w", err)
	}
	return nil
}

func fetch(client *http.Client, url, want string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", url, err)
	}
	if err := verify(data, want); err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	return data, nil
}

func verify(data []byte, want string) error {
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("SHA-256 mismatch: got %s, want %s", got, want)
	}
	return nil
}

// build copies each icon's regular and filled SVG children unchanged into a
// named symbol with the original viewBox. The output keeps the layout of the
// Python ElementTree script it replaced (two-space indentation, " />" for
// empty elements), so regenerating the committed sprite changes no byte.
func build(archive []byte, names []string) ([]byte, error) {
	files, err := svgs(archive)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.WriteString(`<svg xmlns="` + namespace + `">` + "\n")
	for _, name := range names {
		for _, style := range []string{"regular", "filled"} {
			path := "package/icons/" + name + "_20_" + style + ".svg"
			source, ok := files[path]
			if !ok {
				return nil, fmt.Errorf("%s is not in the tarball", path)
			}
			root, err := parse(source)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			viewBox, ok := root.attr("viewBox")
			if !ok {
				return nil, fmt.Errorf("%s has no viewBox", path)
			}
			symbol := &element{name: "symbol", children: root.children, attrs: []xml.Attr{
				{Name: xml.Name{Local: "id"}, Value: strings.ReplaceAll(name, "_", "-") + "-" + style},
				{Name: xml.Name{Local: "viewBox"}, Value: viewBox},
				{Name: xml.Name{Local: "fill"}, Value: "currentColor"},
			}}
			symbol.write(&out, 1)
		}
	}
	out.WriteString("</svg>\n")
	return out.Bytes(), nil
}

func svgs(archive []byte) (map[string][]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("reading the tarball: %w", err)
	}
	files := map[string][]byte{}
	r := tar.NewReader(gz)
	for {
		header, err := r.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading the tarball: %w", err)
		}
		if header.Typeflag == tar.TypeReg && strings.HasSuffix(header.Name, ".svg") {
			data, err := io.ReadAll(r)
			if err != nil {
				return nil, fmt.Errorf("reading %s: %w", header.Name, err)
			}
			files[header.Name] = data
		}
	}
}

// element is the part of an SVG the sprite copies: elements and attributes.
// The pinned icons hold no text, comments or foreign namespaces.
type element struct {
	name     string
	attrs    []xml.Attr
	children []*element
}

func (e *element) attr(name string) (string, bool) {
	for _, a := range e.attrs {
		if a.Name.Local == name && a.Name.Space == "" {
			return a.Value, true
		}
	}
	return "", false
}

func parse(source []byte) (*element, error) {
	d := xml.NewDecoder(bytes.NewReader(source))
	var stack []*element
	var root *element
	for {
		token, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Space != namespace {
				return nil, fmt.Errorf("element %s is outside the SVG namespace", t.Name.Local)
			}
			e := &element{name: t.Name.Local}
			for _, a := range t.Attr {
				// The namespace declaration is written once, on the sprite.
				if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
					continue
				}
				if a.Name.Space != "" {
					return nil, fmt.Errorf("attribute %s:%s is namespaced", a.Name.Space, a.Name.Local)
				}
				e.attrs = append(e.attrs, xml.Attr{Name: xml.Name{Local: a.Name.Local}, Value: a.Value})
			}
			if len(stack) == 0 {
				if root != nil {
					return nil, errors.New("more than one root element")
				}
				root = e
			} else {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, e)
			}
			stack = append(stack, e)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if strings.TrimSpace(string(t)) != "" {
				return nil, errors.New("text content is not supported")
			}
		}
	}
	if root == nil || root.name != "svg" {
		return nil, errors.New("no svg root element")
	}
	return root, nil
}

func (e *element) write(out *bytes.Buffer, depth int) {
	indent := strings.Repeat("  ", depth)
	out.WriteString(indent + "<" + e.name)
	for _, a := range e.attrs {
		out.WriteString(" " + a.Name.Local + `="` + escape(a.Value) + `"`)
	}
	if len(e.children) == 0 {
		out.WriteString(" />\n")
		return
	}
	out.WriteString(">\n")
	for _, c := range e.children {
		c.write(out, depth+1)
	}
	out.WriteString(indent + "</" + e.name + ">\n")
}

// escape matches ElementTree's attribute escaping.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "\n", "&#10;", "\r", "&#13;", "\t", "&#09;").Replace(s)
}
