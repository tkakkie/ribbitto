// Command docscheck keeps the repository's documents usable by AI: each
// file stays within a size limit (one topic per file), and every link or
// reference to a Markdown file and its #anchor resolves. make check runs
// it with the repository root as its argument. Markdown is parsed with
// goldmark (GFM); this tools module, not the application's, carries that
// dependency.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"html"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// The limits are in characters (Unicode code points, whitespace and
// newlines included), so long unwrapped lines cannot slip past a line
// count. AGENTS.md is loaded into every session, so it gets the tighter
// limit; both are about 150 and 200 lines of the repository's prose.
const (
	agentsLimit = 8_000
	docLimit    = 11_000
)

const exceptionsFile = "docs/size-exceptions.txt"

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	sizes, err := checkSizes(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "docscheck:", err)
		os.Exit(2)
	}
	links, err := checkLinks(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "docscheck:", err)
		os.Exit(2)
	}
	decisions, err := checkDecisions(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "docscheck:", err)
		os.Exit(2)
	}
	for _, p := range append(append(sizes, links...), decisions...) {
		fmt.Fprintln(os.Stderr, p)
	}
	if len(sizes) > 0 {
		fmt.Fprintln(os.Stderr, "\nA file over its limit keeps its pull request small: open an issue to split it, add \"<path> #<issue>\" to "+exceptionsFile+", and split it in a separate pull request.")
	}
	if len(sizes)+len(links)+len(decisions) > 0 {
		os.Exit(1)
	}
}

// limitFor returns the size limit for a repository path, or 0 when the
// file is not checked (including generated files).
func limitFor(path string) int {
	switch {
	case path == "AGENTS.md":
		return agentsLimit
	case path == "README.md", path == "DECISIONS.md":
		return docLimit
	case path == "docs/dependencies.md", strings.HasPrefix(path, "docs/schema/"):
		return 0
	case strings.HasPrefix(path, "docs/") && strings.HasSuffix(path, ".md"):
		return docLimit
	}
	return 0
}

func checkSizes(root string) ([]string, error) {
	exceptions, problems, err := readExceptions(root)
	if err != nil {
		return nil, err
	}
	files, err := markdownFiles(root)
	if err != nil {
		return nil, err
	}
	for _, path := range files {
		limit := limitFor(path)
		if limit == 0 {
			continue
		}
		content, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			return nil, err
		}
		size := utf8.RuneCount(content)
		_, excepted := exceptions[path]
		switch {
		case size > limit && !excepted:
			problems = append(problems, fmt.Sprintf("%s: %d characters, over its limit of %d", path, size, limit))
		case size <= limit && excepted:
			problems = append(problems, fmt.Sprintf("%s: %d characters, within its limit of %d, but still listed in %s; remove the line", path, size, limit, exceptionsFile))
		}
		delete(exceptions, path)
	}
	for path := range exceptions {
		problems = append(problems, fmt.Sprintf("%s: lists %s, which is not a checked document", exceptionsFile, path))
	}
	sort.Strings(problems)
	return problems, nil
}

var exceptionLine = regexp.MustCompile(`^(\S+) #([1-9][0-9]*)$`)

// readExceptions reads "<path> #<issue>" lines. Blank lines and lines
// starting with "# " are comments. That the issue is open is checked in
// review, so make check never needs GitHub.
func readExceptions(root string) (map[string]int, []string, error) {
	exceptions := map[string]int{}
	var problems []string
	f, err := os.Open(filepath.Join(root, exceptionsFile))
	if os.IsNotExist(err) {
		return exceptions, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "# ") {
			continue
		}
		m := exceptionLine.FindStringSubmatch(line)
		if m == nil {
			problems = append(problems, fmt.Sprintf("%s:%d: want \"<path> #<issue>\", got %q", exceptionsFile, n, line))
			continue
		}
		issue, _ := strconv.Atoi(m[2])
		exceptions[m[1]] = issue
	}
	return exceptions, problems, scanner.Err()
}

// markdownFiles lists the repository's Markdown files, relative to root,
// skipping build output and version control.
func markdownFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == ".git" || rel == "bin" || rel == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(rel, ".md") {
			files = append(files, rel)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

// A bare reference names a Markdown file from the repository root, as
// AGENTS.md, templates, prompts and scripts do in prose, code spans and
// comments. The fragment is taken whole, Unicode included, so an unknown
// anchor is reported rather than silently shortened; it stops at quotes,
// brackets and backslashes (a "\n" in a shell string).
var bareReference = regexp.MustCompile("(?:^|[\\s`(\"'=:])((?:[A-Za-z0-9_.-]+/)*[A-Za-z0-9_.-]+\\.md)(#[^\\s`\"')\\]\\\\]+)?")

// bareReferences returns the path and anchor of each bare reference in
// text. A match must end the file name: docs/x.md.backup is not docs/x.md,
// but sentence punctuation after it is allowed.
func bareReferences(text string) [][2]string {
	var out [][2]string
	for _, m := range bareReference.FindAllStringSubmatchIndex(text, -1) {
		rest := text[m[1]:]
		if m[4] < 0 && rest != "" {
			next, _ := utf8.DecodeRuneInString(rest)
			after := strings.TrimLeft(rest, ".,;:!?")
			if next == '_' || next == '-' || next == '/' || unicode.IsLetter(next) || unicode.IsDigit(next) ||
				(next == '.' && after != "" && !unicode.IsSpace([]rune(after)[0])) {
				continue
			}
		}
		anchor := ""
		if m[4] >= 0 {
			anchor = strings.TrimRight(text[m[4]+1:m[5]], ".,;:!?")
		}
		out = append(out, [2]string{text[m[2]:m[3]], anchor})
	}
	return out
}

var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

// document is a parsed Markdown file.
type document struct {
	source     []byte
	root       ast.Node
	references []parser.Reference
}

func parse(source []byte) document {
	ctx := parser.NewContext()
	root := markdown.Parser().Parse(text.NewReader(source), parser.WithContext(ctx))
	return document{source: source, root: root, references: ctx.References()}
}

// lineOf returns the 1-based line of a byte offset.
func (d document) lineOf(offset int) int {
	return bytes.Count(d.source[:offset], []byte("\n")) + 1
}

// lineOfNode returns the line of the first text inside n, or 0.
func (d document) lineOfNode(n ast.Node) int {
	line := 0
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if t, ok := c.(*ast.Text); ok && entering {
			line = d.lineOf(t.Segment.Start)
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	return line
}

func checkLinks(root string) ([]string, error) {
	files, err := markdownFiles(root)
	if err != nil {
		return nil, err
	}
	anchors := map[string]map[string]bool{}
	var problems []string
	repo, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = repo.Close() }()
	resolve := func(from, target, anchor string, line int) {
		a, ok := anchors[target]
		if !ok {
			// Only regular files inside the repository count: GitHub
			// cannot follow a link out of it, and shows a symlink rather
			// than the file it points to.
			if info, err := repo.Lstat(target); filepath.IsLocal(target) && err == nil && info.Mode().IsRegular() {
				if source, err := repo.ReadFile(target); err == nil {
					a = headingAnchors(parse(source))
				}
			}
			anchors[target] = a
		}
		switch {
		case a == nil:
			problems = append(problems, fmt.Sprintf("%s:%d: %s does not exist", from, line, target))
		case anchor != "" && !a[anchor]:
			problems = append(problems, fmt.Sprintf("%s:%d: %s has no heading #%s", from, line, target, anchor))
		}
	}
	checkDestination := func(from, destination string, line int) {
		file, anchor, _ := strings.Cut(html.UnescapeString(string(util.UnescapePunctuations([]byte(destination)))), "#")
		// The query and a trailing slash do not change the file GitHub
		// opens; only the path decides whether the link leaves the site.
		file, _, _ = strings.Cut(file, "?")
		file = strings.TrimSuffix(file, "/")
		if strings.Contains(file, "://") || strings.HasPrefix(file, "mailto:") {
			return
		}
		if f, err := url.PathUnescape(file); err == nil {
			file = f
		}
		if a, err := url.PathUnescape(anchor); err == nil {
			anchor = a
		}
		switch {
		case file == "":
			resolve(from, from, anchor, line)
		case strings.HasSuffix(strings.TrimSpace(file), ".md"):
			resolve(from, filepath.ToSlash(filepath.Join(filepath.Dir(from), file)), anchor, line)
		}
	}
	for _, path := range files {
		if strings.HasPrefix(path, "docs/schema/") {
			continue // generated by tbls
		}
		source, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			return nil, err
		}
		d := parse(source)
		linked := map[string]bool{}
		_ = ast.Walk(d.root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
			if link, ok := n.(*ast.Link); ok && entering {
				linked[string(link.Destination)] = true
				checkDestination(path, string(link.Destination), d.lineOfNode(link))
			}
			return ast.WalkContinue, nil
		})
		// Definitions no link uses are checked too; goldmark keeps no
		// position for them, hence line 0.
		for _, ref := range d.references {
			if !linked[string(ref.Destination())] {
				checkDestination(path, string(ref.Destination()), 0)
			}
		}
	}
	sources, err := bareSources(root)
	if err != nil {
		return nil, err
	}
	for _, path := range sources {
		source, err := os.ReadFile(filepath.Join(root, path))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, l := range bareText(path, source) {
			for _, ref := range bareReferences(l.text) {
				resolve(path, ref[0], ref[1], l.number)
			}
		}
	}
	sort.Strings(problems)
	return compact(problems), nil
}

type line struct {
	number int
	text   string
}

// bareText returns the text to scan for bare references. In Markdown it is
// the prose and code spans outside links (whose destinations and labels
// the link check covers) and outside code blocks (examples); in any other
// file it is every line.
func bareText(path string, source []byte) []line {
	var lines []line
	if !strings.HasSuffix(path, ".md") {
		for n, t := range strings.Split(string(source), "\n") {
			lines = append(lines, line{n + 1, t})
		}
		return lines
	}
	d := parse(source)
	_ = ast.Walk(d.root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Link, *ast.Image, *ast.AutoLink, *ast.FencedCodeBlock, *ast.CodeBlock:
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			lines = append(lines, line{d.lineOf(n.Segment.Start), string(n.Segment.Value(d.source))})
		case *ast.RawHTML:
			// Inline HTML, such as a template's <!-- comment -->.
			for i := 0; i < n.Segments.Len(); i++ {
				seg := n.Segments.At(i)
				lines = append(lines, line{d.lineOf(seg.Start), string(seg.Value(d.source))})
			}
		case *ast.HTMLBlock:
			for i := 0; i < n.Lines().Len(); i++ {
				seg := n.Lines().At(i)
				lines = append(lines, line{d.lineOf(seg.Start), string(seg.Value(d.source))})
			}
			if n.HasClosure() {
				seg := n.ClosureLine
				lines = append(lines, line{d.lineOf(seg.Start), string(seg.Value(d.source))})
			}
		}
		return ast.WalkContinue, nil
	})
	return lines
}

// bareSources are AGENTS.md and every file under .github/ and scripts/.
func bareSources(root string) ([]string, error) {
	sources := []string{"AGENTS.md"}
	for _, dir := range []string{".github", "scripts"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return filepath.SkipDir
				}
				return err
			}
			if !d.IsDir() {
				rel, _ := filepath.Rel(root, path)
				sources = append(sources, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return sources, nil
}

// headingAnchors returns the anchors GitHub gives a document's headings.
// goldmark finds the headings and their inline content; the anchor itself
// follows github-slugger, not goldmark's own ids: the rendered text (link
// labels and code kept, destinations and emphasis markers gone)
// lower-cased, punctuation removed, spaces turned into hyphens, and -1,
// -2 … appended until it is unique among the anchors allocated so far.
func headingAnchors(d document) map[string]bool {
	occurrences := map[string]int{}
	_ = ast.Walk(d.root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		heading, ok := n.(*ast.Heading)
		if !ok || !entering {
			return ast.WalkContinue, nil
		}
		base := slugify(renderedText(heading, d.source))
		slug := base
		for {
			if _, taken := occurrences[slug]; !taken {
				break
			}
			occurrences[base]++
			slug = fmt.Sprintf("%s-%d", base, occurrences[base])
		}
		occurrences[slug] = 0
		return ast.WalkSkipChildren, nil
	})
	anchors := map[string]bool{}
	for slug := range occurrences {
		anchors[slug] = true
	}
	return anchors
}

// renderedText is the text of a node's inline content as rendered.
func renderedText(n ast.Node, source []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch c := c.(type) {
		case *ast.CodeSpan:
			// Code keeps its content literally, entities included.
			for t := c.FirstChild(); t != nil; t = t.NextSibling() {
				if t, ok := t.(*ast.Text); ok {
					b.Write(t.Segment.Value(source))
				}
			}
			return ast.WalkSkipChildren, nil
		case *ast.AutoLink:
			b.Write(c.Label(source))
		case *ast.Text:
			b.WriteString(html.UnescapeString(string(c.Segment.Value(source))))
		case *ast.String:
			b.WriteString(html.UnescapeString(string(c.Value)))
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

func slugify(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(heading)) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		// github-slugger keeps letters, marks and numbers of every script,
		// hyphens and underscores, and drops other punctuation and symbols.
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsNumber(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

func compact(sorted []string) []string {
	out := sorted[:0]
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}
