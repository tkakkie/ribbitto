// Command formatcheck checks Go and templ formatting and internal package
// documentation without rewriting files. It uses the repository's pinned templ.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

func main() {
	root, templ, err := parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "usage: formatcheck -templ <binary> <repository root>")
		os.Exit(2)
	}
	if err := run(root, templ); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func parse(args []string) (string, string, error) {
	flags := flag.NewFlagSet("formatcheck", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	templ := flags.String("templ", "", "path to the pinned templ binary")
	if err := flags.Parse(args); err != nil {
		return "", "", err
	}
	if flags.NArg() != 1 || flags.Arg(0) == "" || *templ == "" {
		return "", "", fmt.Errorf("invalid arguments")
	}
	return flags.Arg(0), *templ, nil
}

func run(root, templ string) error {
	templ, err := filepath.Abs(templ)
	if err != nil {
		return fmt.Errorf("resolving templ: %w", err)
	}
	var goFiles, templates, dirs []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "bin" && entry.IsDir() {
			return filepath.SkipDir
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		switch filepath.Ext(rel) {
		case ".go":
			goFiles = append(goFiles, "./"+filepath.ToSlash(rel))
			if strings.HasPrefix(rel, "internal/") && !strings.HasSuffix(rel, "_test.go") && !strings.Contains(rel, "/testdata/") {
				dirs = append(dirs, filepath.Dir(rel))
			}
		case ".templ":
			templates = append(templates, "./"+filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("listing checked files: %w", err)
	}
	if len(goFiles) > 0 {
		// Use PATH's gofmt, as the old recipe did, including its Go-version rules.
		cmd := exec.Command("gofmt", append([]string{"-l"}, goFiles...)...)
		cmd.Dir, cmd.Stderr = root, os.Stderr
		out, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("running gofmt: %w", err)
		}
		if len(out) > 0 {
			return fmt.Errorf("These files need gofmt:\n%s", strings.TrimSuffix(string(out), "\n"))
		}
	}
	var unformatted []string
	for _, file := range templates {
		source, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			return fmt.Errorf("reading %s: %w", file, err)
		}
		cmd := exec.Command(templ, "fmt", "-stdout", "-stdin-filepath", file)
		cmd.Dir, cmd.Stdin, cmd.Stderr = root, bytes.NewReader(source), os.Stderr
		out, err := cmd.Output()
		// A parse error can produce no stdout, even for an empty source file.
		if err != nil || !bytes.Equal(out, source) {
			unformatted = append(unformatted, file)
		}
	}
	if len(unformatted) > 0 {
		return fmt.Errorf("These files need templ fmt (run ./bin/templ fmt on them):\n%s", strings.Join(unformatted, "\n"))
	}
	slices.Sort(dirs)
	var missing []string
	for _, dir := range slices.Compact(dirs) {
		info, err := os.Stat(filepath.Join(root, dir, "doc.go"))
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("checking %s/doc.go: %w", dir, err)
		}
		if err != nil || !info.Mode().IsRegular() {
			missing = append(missing, dir)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("These internal package directories are missing doc.go:\n%s", strings.Join(missing, "\n"))
	}
	return nil
}
