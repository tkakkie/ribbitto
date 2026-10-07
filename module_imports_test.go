package ribbitto_test

import (
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
		{"internal/identity", "internal/identity/internal/postgres", "internal/identity/identitypg", "internal/identity/identitytest", nil, nil, true},
		{"internal/realtime", "internal/realtime/internal/postgres", "internal/realtime/realtimepg", "", nil, nil, false},
		{"internal/org", "internal/org/internal/postgres", "internal/org/orgpg", "internal/org/orgtest", []string{"identity", "realtime"}, nil, false},
		{"internal/conversation", "internal/conversation/internal/postgres", "internal/conversation/conversationpg", "internal/conversation/conversationtest", []string{"identity", "org", "realtime"}, []string{"org"}, false},
	}
}

func within(path, root string) bool { return path == root || strings.HasPrefix(path, root+"/") }

func validateManifest(modules []module, exists func(string) bool, candidates []string) bool {
	paths, names := map[string]bool{}, map[string]bool{}
	for _, m := range modules {
		name := filepath.Base(m.root)
		if names[name] || m.root == "" || m.store == "" || m.wiring == "" {
			return false
		}
		names[name] = true
		for _, path := range []string{m.root, m.store, m.wiring, m.fixture} {
			if path != "" && (paths[path] || !exists(path)) {
				return false
			}
			paths[path] = true
		}
	}
	for _, m := range modules {
		for _, name := range append(slices.Clone(m.mayImport), m.fixtureRoots...) {
			if !names[name] {
				return false
			}
		}
	}
	for _, path := range candidates {
		if !paths[path] {
			return false
		}
	}
	return true
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
				if to == other.root && slices.Contains(m.fixtureRoots, name) || other.fixture != "" && to == other.fixture && slices.Contains(m.mayImport, name) {
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
	if !validateManifest(modules, exists, candidates) {
		t.Fatal("incomplete module manifest: check paths, duplicates, edges and unlisted module-shaped directories")
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
	for _, pkg := range pkgs {
		if pkg.Name == "main" && strings.HasSuffix(pkg.PkgPath, ".test") && len(pkg.GoFiles) == 1 && !strings.HasSuffix(pkg.GoFiles[0], ".go") { // Cached test main.
			continue
		}
		for _, source := range pkg.Syntax {
			file := pkg.Fset.Position(source.Pos()).Filename
			rel := strings.TrimPrefix(filepath.ToSlash(file), filepath.ToSlash(rootDir)+"/")
			from := filepath.ToSlash(filepath.Dir(rel))
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
}

func TestModuleImportFixtures(t *testing.T) {
	for _, m := range moduleManifest() {
		t.Run(filepath.Base(m.root), func(t *testing.T) {
			pairs := [][4]string{
				{m.root, "internal/kernel", m.root, "internal/web"},
				{m.root, m.root + "/child", m.root, "internal/kernel/child"},
				{m.wiring, m.store, m.root, m.store},
				{m.store, m.store + "/sqlcgen", m.wiring + "/child", m.store},
				{"cmd/server", m.wiring, m.root, m.wiring},
				{m.root + "@test", m.wiring, m.root, m.wiring + "extra"},
				{m.root + "@test", "internal/web", m.root, "internal/web"},
				{m.store + "@test", m.store, m.root + "@test", m.store},
			}
			if m.fixture != "" {
				pairs = append(pairs,
					[4]string{m.root + "@test", m.fixture, m.root, m.fixture},
					[4]string{m.fixture, m.root, m.fixture, "internal/platform/postgres"},
					[4]string{m.fixture + "@test", "internal/web", m.fixture, "internal/web"})
			}
			pairs = append(pairs,
				[4]string{"internal/org", "internal/identity", "internal/identity", "internal/org"},
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
