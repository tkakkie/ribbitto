// Command languagecheck checks tracked file kinds by location, bans shebangs
// and validates exact-path approval records as described by decisions 42 and 44.
// Commands, configuration contents and dependencies remain review items.
// Changing the table requires an issue approved by the maintainer.
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"
)

const exceptionsFile = "docs/language-exceptions.txt"
const explanation = "A new language, runtime or format needs the maintainer's explicit approval. Add one exact file to docs/language-exceptions.txt as: <exact path> #<issue> https://github.com/tkakkie/ribbitto/issues/<issue>#issuecomment-<digits>. Shebangs have no exception."

func main() {
	if len(os.Args) != 2 || os.Args[1] == "" {
		fmt.Fprintln(os.Stderr, "usage: languagecheck <repository root>")
		os.Exit(2)
	}
	problems, err := check(os.Args[1])
	if err != nil {
		problems = append(problems, err.Error())
	}
	sortProblems(problems)
	for _, problem := range problems {
		fmt.Fprintln(os.Stderr, problem)
	}
	if len(problems) > 0 {
		fmt.Fprintln(os.Stderr, explanation)
		os.Exit(1)
	}
}

func git(root string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"--no-pager", "--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "diff.external="}, args...)...)
	cmd.Dir = root
	// Hooks can export repository/index selectors; neither the gate nor its
	// temporary-repository tests may inherit a different repository's state.
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_PAGER=cat", "GIT_EXTERNAL_DIFF=")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %v: %w: %s", args, err, stderr.String())
	}
	return out, nil
}

func tracked(root string) ([]string, error) {
	out, err := git(root, "ls-files", "-z", "--cached")
	if err != nil {
		return nil, err
	}
	return parseListing(out)
}

func parseListing(out []byte) ([]string, error) {
	if len(out) == 0 {
		return nil, fmt.Errorf("git ls-files returned no tracked files")
	}
	if out[len(out)-1] != 0 {
		return nil, fmt.Errorf("git ls-files returned an unterminated name")
	}
	names := strings.Split(string(out[:len(out)-1]), "\x00")
	seen := map[string]bool{}
	for _, name := range names {
		if !validName(name) {
			return nil, fmt.Errorf("git ls-files returned invalid name %q", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("git ls-files returned duplicate name %q (unmerged index)", name)
		}
		seen[name] = true
	}
	return names, nil
}

func validName(name string) bool {
	if name == "" || !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func check(root string) ([]string, error) {
	names, err := tracked(root)
	if err != nil {
		return nil, err
	}
	return checkFiles(root, names)
}

func checkFiles(root string, names []string) ([]string, error) {
	repo, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("opening repository: %w", err)
	}
	defer func() { _ = repo.Close() }()
	trackedNames := map[string]bool{}
	for _, name := range names {
		trackedNames[name] = true
	}
	exceptions, problems := readExceptions(repo, trackedNames)
	for _, name := range names {
		report := func(reason string) { problems = append(problems, fmt.Sprintf("%q: %s", name, reason)) }
		if !validName(name) {
			report("invalid repository path")
			continue
		}
		kind := allowedKind(name)
		f, err := openRegular(repo, name)
		if err != nil {
			report(err.Error())
			continue
		}
		// Only the two-byte signature is needed for ordinary files. Fuzz data has
		// a short, fixed first line; a bounded read cannot swallow arbitrary input.
		size := 2
		if kind == fuzzKind {
			size = len("go test fuzz v1\n")
		}
		head, readErr := io.ReadAll(io.LimitReader(f, int64(size)))
		closeErr := f.Close()
		if readErr != nil {
			report(fmt.Errorf("reading head: %w", readErr).Error())
			continue
		}
		if closeErr != nil {
			report(fmt.Errorf("closing file: %w", closeErr).Error())
			continue
		}
		if bytes.HasPrefix(head, []byte("#!")) {
			report("shebang is forbidden")
			continue
		}
		if kind == fuzzKind && string(head) != "go test fuzz v1\n" && string(head) != "go test fuzz v1" {
			report("fuzz corpus first line must be go test fuzz v1")
		}
		if kind == "" && !exceptions[name] {
			report("path and kind are not in the language table")
		}
	}
	sortProblems(problems)
	return problems, nil
}

// Sort by the real git path, before quoting makes newlines printable.
func sortProblems(problems []string) {
	key := func(problem string) string {
		if quoted, err := strconv.QuotedPrefix(problem); err == nil {
			name, _ := strconv.Unquote(quoted)
			return name
		}
		name, _, _ := strings.Cut(problem, ":")
		return name
	}
	sort.Slice(problems, func(i, j int) bool {
		a, b := key(problems[i]), key(problems[j])
		if a == b {
			return problems[i] < problems[j]
		}
		return a < b
	})
}

// Lstat refuses special files before opening; nonblocking/no-follow flags
// protect against replacing the leaf with a FIFO or symlink between calls.
// OpenRoot confines parent traversal, and SameFile rejects a replaced file.
func openRegular(repo *os.Root, name string) (*os.File, error) {
	if !validName(name) {
		return nil, fmt.Errorf("invalid repository path %q", name)
	}
	parts := strings.Split(name, "/")
	for i := 1; i < len(parts); i++ {
		info, err := repo.Lstat(strings.Join(parts[:i], "/"))
		if err != nil {
			return nil, fmt.Errorf("checking parent: %w", err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("parent is not a directory (symlinks are forbidden)")
		}
	}
	before, err := repo.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("checking file: %w", err)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file (symlinks and special files are forbidden)")
	}
	f, err := repo.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("opening file: %w", err)
	}
	after, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("checking opened file: %w", err)
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		_ = f.Close()
		return nil, fmt.Errorf("file changed while opening or is not regular")
	}
	return f, nil
}
