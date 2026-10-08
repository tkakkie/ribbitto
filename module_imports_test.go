package ribbitto_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

type module struct {
	root, store, wiring, fixture string
	mayImport, fixtureRoots      []string
	exactWiringStore             bool
}

// moduleManifest is the import source of truth; fixture roots are narrower than module edges.
func moduleManifest() []module {
	return []module{
		{root: "internal/identity", store: "internal/identity/internal/postgres", wiring: "internal/identity/identitypg",
			fixture: "internal/identity/identitytest", exactWiringStore: true},
		{root: "internal/realtime", store: "internal/realtime/internal/postgres", wiring: "internal/realtime/realtimepg"},
		{root: "internal/org", store: "internal/org/internal/postgres", wiring: "internal/org/orgpg",
			fixture: "internal/org/orgtest", mayImport: []string{"identity", "realtime"}},
		{root: "internal/conversation", store: "internal/conversation/internal/postgres", wiring: "internal/conversation/conversationpg",
			fixture: "internal/conversation/conversationtest", mayImport: []string{"identity", "org", "realtime"}, fixtureRoots: []string{"org"}},
	}
}

func within(path, root string) bool { return path == root || strings.HasPrefix(path, root+"/") }

func validateManifest(modules []module, exists func(string) bool, candidates []string) error {
	paths, names := map[string]bool{}, map[string]bool{}
	for _, m := range modules {
		name := filepath.Base(m.root)
		if names[name] || m.root == "" || m.store == "" || m.wiring == "" {
			return fmt.Errorf("%q: duplicate module name or missing root, store or wiring", m.root)
		}
		names[name] = true
		for _, path := range []string{m.root, m.store, m.wiring, m.fixture} {
			if path != "" && (paths[path] || !exists(path)) {
				return fmt.Errorf("%q: duplicate or missing directory", path)
			}
			paths[path] = true
		}
	}
	for _, m := range modules {
		for _, name := range append(slices.Clone(m.mayImport), m.fixtureRoots...) {
			if !names[name] {
				return fmt.Errorf("%q: unknown module edge %q", m.root, name)
			}
		}
	}
	for _, path := range candidates {
		if !paths[path] {
			return fmt.Errorf("%q: unlisted module directory", path)
		}
	}
	return nil
}

func importAllowed(modules []module, from, file, to string) bool {
	test := strings.HasSuffix(file, "_test.go")
	for _, m := range modules {
		// Depguard's inbound denies used raw prefixes, including similar names.
		if strings.HasPrefix(to, m.store) && from != m.wiring && !within(from, m.store) {
			return false
		}
		if strings.HasPrefix(to, m.wiring) && !test && !strings.HasPrefix(from, "cmd/") {
			return false
		}
		if m.fixture != "" && strings.HasPrefix(to, m.fixture) && !test {
			allowed := false
			for _, importer := range modules {
				allowed = allowed || from == importer.fixture && slices.Contains(importer.mayImport, filepath.Base(m.root))
			}
			if !allowed {
				return false
			}
		}
	}
	if test || !strings.HasPrefix(to, "internal/") {
		return true
	}
	for _, m := range modules {
		if !within(from, m.root) {
			continue
		}
		if from == m.fixture {
			if to == "internal/kernel" || to == m.root {
				return true
			}
			for _, other := range modules {
				name := filepath.Base(other.root)
				if to == other.root && slices.Contains(m.fixtureRoots, name) {
					return true
				}
				if other.fixture != "" && to == other.fixture && slices.Contains(m.mayImport, name) {
					return true
				}
			}
			return false
		}
		if from == m.wiring && m.exactWiringStore {
			return to == m.root || to == m.store || to == "internal/kernel" || within(to, "internal/platform")
		}
		if within(to, m.root) || to == "internal/kernel" || within(to, "internal/platform") {
			return true
		}
		return slices.Contains(m.mayImport, strings.TrimPrefix(to, "internal/"))
	}
	return true
}

func syntheticTestMain(pkg *packages.Package, rootDir string) bool {
	const modulePath = "github.com/tkakkie/ribbitto"
	if pkg.Name != "main" || !strings.HasSuffix(pkg.PkgPath, ".test") || len(pkg.GoFiles)+len(pkg.CompiledGoFiles) == 0 {
		return false
	}
	if pkg.PkgPath != modulePath+".test" && !strings.HasPrefix(pkg.PkgPath, modulePath+"/") {
		return false
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(pkg.PkgPath, modulePath), "/")
	_, err := os.Stat(filepath.Join(rootDir, filepath.FromSlash(rel)))
	return os.IsNotExist(err)
}

func TestModuleImportSyntheticTestMain(t *testing.T) {
	rootDir := t.TempDir()
	const modulePath = "github.com/tkakkie/ribbitto"
	generated := filepath.Join(rootDir, "bin", ".cache", "generated-d")
	outside := filepath.Join(rootDir, "..", "cache", "generated-d")
	repository := filepath.Join(rootDir, "internal", "x.test", "main.go")
	if err := os.MkdirAll(filepath.Dir(repository), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repository, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		pkg  packages.Package
		want bool
	}{
		{"generated test main inside repository", packages.Package{Name: "main", PkgPath: modulePath + "/internal/foo.test", GoFiles: []string{generated}}, true},
		{"generated test main outside repository", packages.Package{Name: "main", PkgPath: modulePath + "/internal/foo.test", GoFiles: []string{outside}}, true},
		{"generated root test main", packages.Package{Name: "main", PkgPath: modulePath + ".test", GoFiles: []string{generated}}, true},
		{"repository test-suffixed main", packages.Package{Name: "main", PkgPath: modulePath + "/internal/x.test", GoFiles: []string{repository}}, false},
		{"mixed source files", packages.Package{Name: "main", PkgPath: modulePath + "/internal/x.test", GoFiles: []string{generated, repository}}, false},
		{"repository compiled source", packages.Package{Name: "main", PkgPath: modulePath + "/internal/x.test", GoFiles: []string{generated}, CompiledGoFiles: []string{repository}}, false},
		{"non-main package", packages.Package{Name: "example", PkgPath: modulePath + "/internal/foo.test", GoFiles: []string{generated}}, false},
		{"non-test package", packages.Package{Name: "main", PkgPath: modulePath, GoFiles: []string{generated}}, false},
		{"other module", packages.Package{Name: "main", PkgPath: "example/internal/foo.test", GoFiles: []string{generated}}, false},
		{"no source files", packages.Package{Name: "main", PkgPath: modulePath + ".test"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := syntheticTestMain(&tt.pkg, rootDir); got != tt.want {
				t.Errorf("syntheticTestMain() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestModuleImports(t *testing.T) {
	modules := moduleManifest()
	rootDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	exists := func(path string) bool { info, err := os.Stat(path); return err == nil && info.IsDir() }
	entries, err := os.ReadDir("internal")
	if err != nil {
		t.Fatal(err)
	}
	var candidates []string
	for _, entry := range entries {
		// lintfixture is an existing synthetic store for the bridge lint test.
		if !entry.IsDir() || entry.Name() == "lintfixture" {
			continue
		}
		root := "internal/" + entry.Name()
		for _, path := range []string{root + "/internal/postgres", root + "/" + entry.Name() + "pg", root + "/" + entry.Name() + "test"} {
			if exists(path) {
				candidates = append(candidates, path)
			}
		}
	}
	if err := validateManifest(modules, exists, candidates); err != nil {
		t.Fatalf("incomplete module manifest: %v", err)
	}
	// NeedSyntax gives imports per source file, including in-package and external
	// tests. Package-level Imports would grant production files test exceptions.
	pkgs, err := packages.Load(&packages.Config{Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedSyntax, Tests: true}, "./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) == 0 || packages.PrintErrors(pkgs) != 0 {
		t.Fatal("loading repository packages")
	}
	seenFiles, seenPaths := map[string]bool{}, map[string]bool{}
	for _, pkg := range pkgs {
		// Generated test mains have no package directory, regardless of the cache location.
		if syntheticTestMain(pkg, rootDir) {
			continue
		}
		for _, source := range pkg.Syntax {
			file := pkg.Fset.Position(source.Pos()).Filename
			rel, err := filepath.Rel(rootDir, file)
			if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				t.Fatalf("source file %q is outside repository root %q: %v", file, rootDir, err)
			}
			// Production files occur in both P and P [P.test].
			if seenFiles[file] {
				continue
			}
			seenFiles[file] = true
			rel = filepath.ToSlash(rel)
			from := filepath.ToSlash(filepath.Dir(rel))
			seenPaths[from] = true
			for _, spec := range source.Imports {
				path, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				to := strings.TrimPrefix(path, "github.com/tkakkie/ribbitto/")
				if !importAllowed(modules, from, file, to) {
					t.Errorf("%s: forbidden module import %s", rel, to)
				}
			}
		}
	}
	for _, m := range modules {
		for _, path := range []string{m.root, m.store, m.wiring, m.fixture} {
			if path != "" && !seenPaths[path] {
				t.Errorf("manifest path %q: no source files checked", path)
			}
		}
	}
}

func TestModuleImportFixtures(t *testing.T) {
	for _, m := range moduleManifest() {
		t.Run(filepath.Base(m.root), func(t *testing.T) {
			pairs := [][4]string{
				{m.root, "internal/kernel", m.root, "internal/web"},
				{m.root, m.root + "/child", m.root, "internal/kernel/child"},
				{m.wiring, m.store, m.root, m.store},
				{m.store, m.store + "/sqlcgen", m.wiring + "/child", m.store},
				{m.store, m.store + "/sqlcgen", m.root, m.store + "/sqlcgen"},
				{m.store + "@test", m.store + "/sqlcgen", m.root + "@test", m.store + "/sqlcgen"},
				{"cmd/server", m.wiring, m.root, m.wiring},
				{m.root + "@test", m.wiring, m.root, m.wiring + "extra"},
				{m.root + "@test", "internal/web", m.root, "internal/web"},
				{m.store + "@test", m.store, m.root + "@test", m.store},
			}
			if m.fixture != "" {
				pairs = append(pairs,
					[4]string{m.root + "@test", m.fixture, m.root, m.fixture},
					[4]string{m.root + "@test", m.fixture + "/child", m.root, m.fixture + "/child"},
					[4]string{m.fixture, m.root, m.fixture, m.root + "/child"},
					[4]string{m.fixture, m.root, m.fixture, "internal/platform/postgres"},
					[4]string{m.fixture + "@test", "internal/web", m.fixture, "internal/web"})
			}
			pairs = append(pairs,
				[4]string{"internal/org", "internal/identity", "internal/identity", "internal/org"},
				[4]string{"internal/org", "internal/identity", "internal/org", "internal/identity/child"},
				[4]string{"internal/conversation", "internal/org", "internal/realtime", "internal/org"},
				[4]string{"internal/org/orgtest", "internal/identity/identitytest", "internal/identity/identitytest", "internal/org/orgtest"},
				[4]string{"internal/conversation/conversationtest", "internal/org/orgtest", "internal/org/orgtest", "internal/conversation/conversationtest"},
				[4]string{"internal/conversation/conversationtest", "internal/org", "internal/org/orgtest", "internal/identity"},
				[4]string{"internal/identity/identitypg", "internal/identity/internal/postgres", "internal/identity/identitypg", "internal/identity/internal/postgres/sqlcgen"})
			for _, pair := range pairs {
				for i := 0; i < 4; i += 2 {
					from := strings.TrimSuffix(pair[i], "@test")
					file := strings.ReplaceAll(pair[i], "@test", "_test") + ".go"
					if got := importAllowed(moduleManifest(), from, file, pair[i+1]); got != (i == 0) {
						t.Errorf("%s %s -> %s: allowed=%v", from, file, pair[i+1], got)
					}
				}
			}
		})
	}
}
