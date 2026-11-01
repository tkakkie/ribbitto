// Command webbuild builds classic, content-hashed browser bundles without Node.
// Output is uncommitted and embedded only after successful generation.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/evanw/esbuild/pkg/api"
)

var entryNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

type bundle struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

type manifest struct {
	Commit  string            `json:"commit"`
	RunID   string            `json:"run_id"`
	Entries map[string]bundle `json:"entries"`
}

func main() {
	if len(os.Args) < 5 {
		fmt.Fprintln(os.Stderr, "usage: webbuild <repository root> <commit SHA> <run id> <entry>...")
		os.Exit(2)
	}
	if err := build(os.Args[1], os.Args[2], os.Args[3], os.Args[4:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func build(root, commit, runID string, names []string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolving root: %w", err)
	}
	dist := filepath.Join(root, "web/static/dist")
	// Invalidate the previous generation even if this run fails before staging.
	if err := os.Remove(filepath.Join(dist, "manifest.json")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("invalidating manifest: %w", err)
	}
	if commit == "" || runID == "" || len(names) == 0 {
		return fmt.Errorf("commit, run id and entries are required")
	}
	entries := make(map[string]string)
	var inputs []string
	for _, name := range names {
		if !entryNamePattern.MatchString(name) {
			return fmt.Errorf("invalid entry name %q", name)
		}
		input := filepath.Join("web/src", name+".ts")
		if _, err := os.Stat(filepath.Join(root, input)); err != nil {
			return fmt.Errorf("reading entry %s: %w", name, err)
		}
		if entries[input] != "" {
			return fmt.Errorf("duplicate entry %s", name)
		}
		entries[input] = name
		inputs = append(inputs, input)
	}
	result := api.Build(api.BuildOptions{
		AbsWorkingDir: root, EntryPoints: inputs, Outdir: dist,
		EntryNames: "[name]-[hash]", Bundle: true, Format: api.FormatIIFE,
		Platform: api.PlatformBrowser, Target: api.ES2017,
		MinifyWhitespace: true, MinifyIdentifiers: true, MinifySyntax: true,
		Sourcemap: api.SourceMapNone, LegalComments: api.LegalCommentsNone,
		Metafile: true, Write: false,
		// Non-literal imports are absent from the metafile; expose their debug diagnostic for validation.
		LogOverride: map[string]api.LogLevel{"unsupported-dynamic-import": api.LogLevelWarning},
	})
	if len(result.Errors) != 0 {
		return fmt.Errorf("building bundles: %s", result.Errors[0].Text)
	}
	if err := os.MkdirAll(filepath.Dir(dist), 0755); err != nil {
		return fmt.Errorf("creating bundle directory: %w", err)
	}
	stage, err := os.MkdirTemp(filepath.Dir(dist), ".dist-")
	if err != nil {
		return fmt.Errorf("staging bundles: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	m := manifest{Commit: commit, RunID: runID}
	m.Entries, err = validate(root, dist, stage, entries, result)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding manifest: %w", err)
	}
	// The manifest is the completion marker, written only after all validation.
	if err := os.WriteFile(filepath.Join(stage, "manifest.json"), append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}
	if err := os.RemoveAll(dist); err != nil {
		return fmt.Errorf("replacing bundles: %w", err)
	}
	if err := os.Rename(stage, dist); err != nil {
		return fmt.Errorf("publishing bundles: %w", err)
	}
	return nil
}

func validate(root, dist, stage string, entries map[string]string, result api.BuildResult) (map[string]bundle, error) {
	for _, warning := range result.Warnings {
		if warning.ID == "unsupported-dynamic-import" {
			return nil, fmt.Errorf("dynamic imports are not supported: %s", warning.Text)
		}
	}
	var meta struct {
		Inputs  map[string]struct{ Imports []struct{ Kind string } }
		Outputs map[string]struct{ EntryPoint string }
	}
	if err := json.Unmarshal([]byte(result.Metafile), &meta); err != nil {
		return nil, fmt.Errorf("reading esbuild metafile: %w", err)
	}
	for _, input := range meta.Inputs {
		for _, imp := range input.Imports {
			if imp.Kind == "dynamic-import" {
				return nil, fmt.Errorf("dynamic imports are not supported")
			}
		}
	}
	produced := make(map[string]bundle)
	for _, file := range result.OutputFiles {
		if filepath.Dir(file.Path) != dist || filepath.Ext(file.Path) != ".js" {
			return nil, fmt.Errorf("output outside bundle tree: %s", file.Path)
		}
		name := filepath.Base(file.Path)
		produced[name] = bundle{name, fmt.Sprintf("%x", sha256.Sum256(file.Contents))}
		if err := os.WriteFile(filepath.Join(stage, name), file.Contents, 0644); err != nil {
			return nil, fmt.Errorf("writing bundle: %w", err)
		}
	}
	listed := make(map[string]bundle)
	for path, output := range meta.Outputs {
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		if filepath.Dir(path) != dist {
			return nil, fmt.Errorf("metafile output outside bundle tree: %s", path)
		}
		name := entries[filepath.Clean(output.EntryPoint)]
		if name == "" || listed[name].File != "" {
			return nil, fmt.Errorf("unexplained output: %s", path)
		}
		listed[name] = bundle{File: filepath.Base(path)}
	}
	for _, name := range entries {
		if listed[name].File == "" {
			return nil, fmt.Errorf("entry without output: %s", name)
		}
	}
	for name, line := range listed {
		file, ok := produced[line.File]
		if !ok {
			return nil, fmt.Errorf("manifest names unproduced file: %s", line.File)
		}
		listed[name] = file
		delete(produced, line.File)
	}
	if len(produced) != 0 {
		return nil, fmt.Errorf("unexplained produced files")
	}
	files, err := os.ReadDir(stage)
	if err != nil {
		return nil, fmt.Errorf("reading staged bundles: %w", err)
	}
	if len(files) != len(listed) {
		return nil, fmt.Errorf("foreign file in output directory")
	}
	return listed, nil
}
