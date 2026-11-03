// Command webbuild builds classic, content-hashed browser bundles without Node.
// Output is uncommitted and embedded only after successful generation.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/evanw/esbuild/pkg/api"
)

var entryNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

const (
	syntaxTarget           = api.ES2018
	prodDistDir            = "web/static/dist"
	prodMapDir             = "bin/sourcemaps"
	devDistDir             = "bin/dev-dist"
	maxPreviousGenerations = 3
)

type bundle struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Map    string `json:"map,omitempty"`
}

type manifest struct {
	Commit   string              `json:"commit"`
	RunID    string              `json:"run_id"`
	Entries  map[string]bundle   `json:"entries"`
	Previous []map[string]bundle `json:"previous,omitempty"`
}

const usage = "usage: webbuild <repository root> <commit SHA> <run id> | webbuild -dev [-watch] <repository root>"

func main() {
	flags := flag.NewFlagSet("webbuild", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dev := flags.Bool("dev", false, "write local development source maps")
	watchMode := flags.Bool("watch", false, "watch web/src and rebuild development bundles")
	if err := flags.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	args := flags.Args()
	var err error
	switch {
	case *watchMode && len(args) == 1:
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err = watch(ctx, args[0], os.Stderr)
	case *dev && !*watchMode && len(args) == 1:
		var snap string
		if snap, err = sourceSnapshot(args[0]); err == nil {
			err = build(args[0], "dev", snap, true)
		}
	case !*dev && !*watchMode && len(args) == 3:
		err = build(args[0], args[1], args[2], false)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func watch(ctx context.Context, root string, stderr io.Writer) error {
	var last, lastErr string
	if snap, err := sourceSnapshot(root); err == nil {
		if data, err := os.ReadFile(filepath.Join(root, devDistDir, "manifest.json")); err == nil && strings.Contains(string(data), `"run_id": "`+snap+`"`) && strings.Contains(string(data), `"map":`) {
			last = snap
		}
	}
	// Poll web/src every 50 ms so watch needs no external watcher dependency.
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		next, err := sourceSnapshot(root)
		if err != nil {
			if msg := err.Error(); msg != lastErr {
				_, _ = fmt.Fprintln(stderr, err)
				lastErr = msg
			}
		} else {
			lastErr = ""
			if next != last {
				if err := build(root, "dev", next, true); err != nil {
					_, _ = fmt.Fprintln(stderr, err)
				}
				last = next
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func sourceSnapshot(root string) (string, error) {
	h := sha256.New()
	scripts, err := os.ReadFile(filepath.Join(root, "web/static/scripts.go"))
	if err != nil {
		return "", err
	}
	h.Write(scripts)
	err = filepath.WalkDir(filepath.Join(root, "web/src"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		_, _ = fmt.Fprintf(h, "\x00%s\x00%s", path, data)
		return err
	})
	return fmt.Sprintf("%x", h.Sum(nil)), err
}

func build(root, commit, runID string, dev bool) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolving root: %w", err)
	}
	dist := filepath.Join(root, prodDistDir)
	mapDist := filepath.Join(root, prodMapDir)
	if dev {
		dist = filepath.Join(root, devDistDir)
	} else {
		// Invalidate the previous generation even if this run fails before staging.
		if err := os.Remove(filepath.Join(dist, "manifest.json")); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("invalidating manifest: %w", err)
		}
		for _, p := range []string{filepath.Dir(mapDist), mapDist} {
			if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("staging maps: symlinked map path %s", p)
			}
		}
		_ = os.Remove(filepath.Join(mapDist, "index.json"))
	}
	names, err := declaredEntries(root)
	if err != nil {
		return err
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
	sourcemap := api.SourceMapNone
	if dev {
		sourcemap = api.SourceMapLinked
	}
	opts := api.BuildOptions{
		AbsWorkingDir: root, EntryPoints: inputs, Outdir: dist,
		EntryNames: "[name]-[hash]", Bundle: true, Format: api.FormatIIFE,
		Platform: api.PlatformBrowser, Target: syntaxTarget,
		MinifyWhitespace: true, MinifyIdentifiers: true, MinifySyntax: true,
		Sourcemap: sourcemap, SourcesContent: api.SourcesContentInclude,
		LegalComments: api.LegalCommentsNone,
		Metafile:      true, Write: false,
		// Non-literal imports are absent from the metafile; expose their debug diagnostic for validation.
		LogOverride: map[string]api.LogLevel{"unsupported-dynamic-import": api.LogLevelWarning},
	}
	result := api.Build(opts)
	if len(result.Errors) != 0 {
		return fmt.Errorf("building bundles: %s", result.Errors[0].Text)
	}
	if !dev {
		opts.Sourcemap, opts.EntryNames = api.SourceMapExternal, "[name]"
		if result, err = attachExternalMaps(dist, result, api.Build(opts)); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(dist), 0755); err != nil {
		return fmt.Errorf("creating bundle directory: %w", err)
	}
	stage, err := os.MkdirTemp(filepath.Dir(dist), ".dist-")
	if err != nil {
		return fmt.Errorf("staging bundles: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	mapStage := stage
	if !dev {
		if err := os.MkdirAll(filepath.Dir(mapDist), 0755); err != nil {
			return fmt.Errorf("staging maps: %w", err)
		}
		realRoot, err1 := filepath.EvalSymlinks(root)
		realParent, err2 := filepath.EvalSymlinks(filepath.Dir(mapDist))
		if err1 != nil || err2 != nil || realParent != filepath.Join(realRoot, filepath.Dir(prodMapDir)) {
			return fmt.Errorf("staging maps: symlinked map path %s", filepath.Dir(mapDist))
		}
		mapStage, err = os.MkdirTemp(filepath.Dir(mapDist), ".sourcemaps-")
		if err != nil {
			return fmt.Errorf("staging maps: %w", err)
		}
		defer func() { _ = os.RemoveAll(mapStage) }()
	}
	m := manifest{Commit: commit, RunID: runID}
	m.Entries, err = validate(root, dist, stage, mapStage, entries, result, dev)
	if err != nil {
		return err
	}
	if dev {
		return publishDev(dist, stage, m)
	}
	idxData, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding map index: %w", err)
	}
	if err := os.WriteFile(filepath.Join(mapStage, "index.json"), append(idxData, '\n'), 0644); err != nil {
		return fmt.Errorf("writing map index: %w", err)
	}
	for name, b := range m.Entries {
		b.Map = ""
		m.Entries[name] = b
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding manifest: %w", err)
	}
	// The manifest is the completion marker, written only after all validation.
	if err := os.WriteFile(filepath.Join(stage, "manifest.json"), append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}
	// Maps are published before bundles; if bundle replacement fails below,
	// manifest.json was already removed and the build exits non-zero.
	_ = os.RemoveAll(mapDist)
	if err := os.Rename(mapStage, mapDist); err != nil {
		return fmt.Errorf("publishing maps: %w", err)
	}
	old := stage + "-old"
	defer func() { _ = os.RemoveAll(old) }()
	if err := os.Rename(dist, old); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("replacing bundles: %w", err)
	}
	if err := os.Rename(stage, dist); err != nil {
		_ = os.Rename(old, dist)
		return fmt.Errorf("publishing bundles: %w", err)
	}
	return nil
}

func attachExternalMaps(dist string, noneRes, extRes api.BuildResult) (api.BuildResult, error) {
	if len(extRes.Errors) != 0 {
		return noneRes, fmt.Errorf("building source maps: %s", extRes.Errors[0].Text)
	}
	extFiles := make(map[string][]byte, len(extRes.OutputFiles))
	for _, f := range extRes.OutputFiles {
		extFiles[filepath.Base(f.Path)] = f.Contents
	}
	var meta map[string]map[string]json.RawMessage
	if err := json.Unmarshal([]byte(noneRes.Metafile), &meta); err != nil {
		return noneRes, err
	}
	for _, f := range noneRes.OutputFiles {
		base := filepath.Base(f.Path)
		name := base[:max(0, strings.LastIndexByte(base, '-'))]
		if !bytes.Equal(f.Contents, extFiles[name+".js"]) || len(extFiles[name+".js.map"]) == 0 {
			return noneRes, fmt.Errorf("external map mismatch for %s", base)
		}
		noneRes.OutputFiles = append(noneRes.OutputFiles, api.OutputFile{Path: filepath.Join(dist, base+".map"), Contents: extFiles[name+".js.map"]})
		meta["outputs"][filepath.Join(dist, base+".map")] = json.RawMessage(`{}`)
	}
	b, err := json.Marshal(meta)
	noneRes.Metafile = string(b)
	return noneRes, err
}

func publishDev(dist, stage string, m manifest) error {
	if err := os.MkdirAll(dist, 0755); err != nil {
		return fmt.Errorf("creating development bundle directory: %w", err)
	}
	// Serialize concurrent dev publishers (such as webbuild -dev during watch)
	// across reading the previous manifest, publishing files and cleanup; keep
	// the lock file outside dist so it is never served or garbage-collected.
	lock, err := os.OpenFile(filepath.Clean(dist)+".lock", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("locking development bundle directory: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("locking development bundle directory: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	var prev manifest
	if oldData, err := os.ReadFile(filepath.Join(dist, "manifest.json")); err == nil {
		_ = json.Unmarshal(oldData, &prev)
	}
	if len(prev.Entries) > 0 && !maps.Equal(prev.Entries, m.Entries) {
		m.Previous = append([]map[string]bundle{prev.Entries}, prev.Previous...)
		if len(m.Previous) > maxPreviousGenerations {
			m.Previous = m.Previous[:maxPreviousGenerations]
		}
	} else {
		m.Previous = prev.Previous
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stage, "manifest.json"), append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}
	for _, entry := range m.Entries {
		for _, name := range []string{entry.File, entry.Map} {
			if err := os.Rename(filepath.Join(stage, name), filepath.Join(dist, name)); err != nil {
				return fmt.Errorf("publishing development bundle: %w", err)
			}
		}
	}
	// Replacing manifest.json by an atomic rename on the same filesystem
	// switches the active generation without a missing or half-written window.
	if err := os.Rename(filepath.Join(stage, "manifest.json"), filepath.Join(dist, "manifest.json")); err != nil {
		return fmt.Errorf("publishing development manifest: %w", err)
	}
	keep := map[string]bool{"manifest.json": true}
	for _, gen := range append([]map[string]bundle{m.Entries}, m.Previous...) {
		for _, entry := range gen {
			keep[entry.File] = true
			keep[entry.Map] = true
		}
	}
	if files, err := os.ReadDir(dist); err == nil {
		for _, file := range files {
			if !keep[file.Name()] {
				_ = os.RemoveAll(filepath.Join(dist, file.Name()))
			}
		}
	}
	return nil
}

func declaredEntries(root string) ([]string, error) {
	source, err := os.ReadFile(filepath.Join(root, "web/static/scripts.go"))
	if err != nil {
		return nil, fmt.Errorf("reading entry declarations: %w", err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "scripts.go", source, 0)
	if err != nil {
		return nil, fmt.Errorf("parsing entry declarations: %w", err)
	}
	var names []string
	var found bool
	var declarations int
	var parseErr error
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != "NewScripts" || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if parseErr != nil {
				return false
			}
			assign, ok := n.(*ast.AssignStmt)
			if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
				return true
			}
			ident, ok := assign.Lhs[0].(*ast.Ident)
			if !ok || ident.Name != "names" {
				return true
			}
			declarations++
			if declarations > 1 {
				parseErr = fmt.Errorf("multiple entry declarations in scripts.go")
				return false
			}
			if !slices.Contains(fn.Body.List, ast.Stmt(assign)) {
				return false
			}
			found = true
			lit, ok := assign.Rhs[0].(*ast.CompositeLit)
			if !ok {
				parseErr = fmt.Errorf("entry declaration must be a string slice literal")
				return false
			}
			array, ok := lit.Type.(*ast.ArrayType)
			if !ok || array.Len != nil {
				parseErr = fmt.Errorf("entry declaration must be a string slice literal")
				return false
			}
			if elt, ok := array.Elt.(*ast.Ident); !ok || elt.Name != "string" {
				parseErr = fmt.Errorf("entry declaration must be a string slice literal")
				return false
			}
			for _, expr := range lit.Elts {
				basic, ok := expr.(*ast.BasicLit)
				if !ok || basic.Kind != token.STRING {
					parseErr = fmt.Errorf("entry name must be a string literal")
					return false
				}
				name, err := strconv.Unquote(basic.Value)
				if err != nil {
					parseErr = fmt.Errorf("reading entry name: %w", err)
					return false
				}
				names = append(names, name)
			}
			return false
		})
	}
	if parseErr != nil {
		return nil, parseErr
	}
	if !found {
		return nil, fmt.Errorf("missing entry declaration in scripts.go")
	}
	return names, nil
}

func validate(root, dist, stage, mapStage string, entries map[string]string, result api.BuildResult, dev bool) (map[string]bundle, error) {
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
	maps, metaMaps := make(map[string]bool), make(map[string]bool)
	for _, file := range result.OutputFiles {
		name := filepath.Base(file.Path)
		if filepath.Dir(file.Path) != dist || (filepath.Ext(name) != ".js" && !strings.HasSuffix(name, ".js.map")) {
			return nil, fmt.Errorf("output outside bundle tree: %s", file.Path)
		}
		targetDir := stage
		if filepath.Ext(name) == ".js" {
			if !dev && bytes.Contains(file.Contents, []byte("sourceMappingURL")) {
				return nil, fmt.Errorf("production bundle contains sourceMappingURL: %s", name)
			}
			produced[name] = bundle{File: name, SHA256: fmt.Sprintf("%x", sha256.Sum256(file.Contents))}
		} else {
			if !bytes.Contains(file.Contents, []byte(`"sourcesContent"`)) {
				return nil, fmt.Errorf("source map missing sourcesContent: %s", name)
			}
			maps[name] = true
			targetDir = mapStage
		}
		if err := os.WriteFile(filepath.Join(targetDir, name), file.Contents, 0644); err != nil {
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
		base := filepath.Base(path)
		if strings.HasSuffix(base, ".js.map") && output.EntryPoint == "" && !metaMaps[base] {
			metaMaps[base] = true
			continue
		}
		name := entries[filepath.Clean(output.EntryPoint)]
		if name == "" || listed[name].File != "" {
			return nil, fmt.Errorf("unexplained output: %s", path)
		}
		listed[name] = bundle{File: base}
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
		file.Map = line.File + ".map"
		if !metaMaps[file.Map] || !maps[file.Map] {
			return nil, fmt.Errorf("bundle without source map: %s", line.File)
		}
		delete(metaMaps, file.Map)
		delete(maps, file.Map)
		listed[name] = file
		delete(produced, line.File)
	}
	if len(produced) != 0 || len(metaMaps) != 0 || len(maps) != 0 {
		return nil, fmt.Errorf("unexplained produced files")
	}
	files, err := os.ReadDir(stage)
	if err != nil {
		return nil, fmt.Errorf("reading staged bundles: %w", err)
	}
	expected := len(listed)
	if dev {
		expected *= 2
	}
	if len(files) != expected {
		return nil, fmt.Errorf("foreign file in output directory")
	}
	if !dev {
		if mapFiles, err := os.ReadDir(mapStage); err != nil || len(mapFiles) != len(listed) {
			return nil, fmt.Errorf("foreign file in map output directory")
		}
	}
	return listed, nil
}
