package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// failure is a message safe to show: it names what is wrong and never
// contains credentials. Any other error is reported as a generic failure.
type failure string

func (f failure) Error() string { return string(f) }

const generic = "ai-env failed: check ports, PostgreSQL availability and admin configuration"

// entry is one worktree's reservation in ai-env.json, written by make ai-env.
// Ports stay raw so a non-integer value is refused rather than converted.
type entry struct {
	Path          string            `json:"path"`
	DatabasePath  string            `json:"database_path"`
	Ports         []json.RawMessage `json:"ports"`
	MetadataToken *string           `json:"metadata_token"`
}

var integer = regexp.MustCompile(`^[0-9]+$`)

// runWithEnvironment replaces this process with the command, configured for
// the current worktree when it has a .env.local.
func runWithEnvironment(args []string) error {
	if len(args) == 0 {
		return failure("usage: ai-db run <command> [argument...]")
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	env, err := environment(dir)
	if err != nil {
		return err
	}
	path, err := exec.LookPath(args[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, args, env)
}

// environment returns the process environment with the worktree's database
// and ports. Every local value must match the registry before any URL is
// built, so a hand-edited .env.local cannot point a command elsewhere.
func environment(dir string) ([]string, error) {
	root, err := worktreeRoot(dir)
	if err != nil {
		return nil, err
	}
	local, err := os.ReadFile(filepath.Join(root, ".env.local"))
	if errors.Is(err, os.ErrNotExist) {
		return os.Environ(), nil
	}
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(local), "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, errors.New("malformed .env.local")
		}
		values[key] = value
	}
	common, err := commonDir(root)
	if err != nil {
		return nil, err
	}
	if err := withLock(common, syscall.LOCK_SH, func() error { return validate(root, common, values) }); err != nil {
		return nil, err
	}
	databaseURL, err := databaseURL(os.Getenv("RIBBITTO_TEST_DATABASE_URL"), values["AI_DATABASE"])
	if err != nil {
		return nil, err
	}
	return append(os.Environ(),
		"RIBBITTO_DATABASE_URL="+databaseURL,
		"RIBBITTO_ADDR=127.0.0.1:"+values["AI_APP_PORT"],
		"RIBBITTO_DEV_METRICS_ADDR=127.0.0.1:"+values["AI_METRICS_PORT"]), nil
}

func validate(root, common string, values map[string]string) error {
	data, err := os.ReadFile(filepath.Join(common, "ai-env.json"))
	if err != nil {
		return err
	}
	var entries map[string]entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return err
	}
	live, err := worktrees(root, common)
	if err != nil {
		return err
	}
	key, err := currentID(root, live)
	if err != nil {
		return err
	}
	e, ok := entries[key]
	if ok && key != "main" {
		token, found := metadataToken(common, key)
		if e.MetadataToken == nil || *e.MetadataToken == "" || !found || *e.MetadataToken != token {
			return failure("worktree reservation is stale; run make ai-env")
		}
	}
	if !ok || values["AI_DATABASE"] != databaseName(e.DatabasePath) {
		return failure(".env.local AI_DATABASE does not match this worktree; run make ai-env")
	}
	if len(e.Ports) != 2 {
		return failure("worktree port reservations invalid; run make ai-env")
	}
	for i, field := range []string{"AI_APP_PORT", "AI_METRICS_PORT"} {
		raw := string(e.Ports[i])
		port, err := strconv.Atoi(raw)
		if !integer.MatchString(raw) || err != nil || port < 1 || port > 65535 || values[field] != raw {
			return failure(".env.local " + field + " does not match this worktree's reserved port; run make ai-env")
		}
	}
	return nil
}

// databaseURL replaces only the admin URL's database; dbname and database
// query parameters are dropped so they cannot override it.
func databaseURL(admin, name string) (string, error) {
	if admin == "" {
		return "", errors.New("admin configuration unset")
	}
	u, err := url.Parse(admin)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" {
		return "", failure("admin configuration must be a PostgreSQL URL")
	}
	var kept []string
	for _, pair := range strings.Split(u.RawQuery, "&") {
		key, _, _ := strings.Cut(pair, "=")
		if k, err := url.QueryUnescape(key); pair == "" || err == nil && (k == "dbname" || k == "database") {
			continue
		}
		kept = append(kept, pair)
	}
	u.Path, u.RawPath, u.RawQuery = "/"+name, "/"+url.PathEscape(name), strings.Join(kept, "&")
	return u.String(), nil
}

func worktreeRoot(dir string) (string, error) {
	out, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return resolve(strings.TrimSpace(string(out))), nil
}

func commonDir(root string) (string, error) {
	out, err := git(root, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(string(out))
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return resolve(path), nil
}

// worktrees maps each live worktree's stable ID ("main", or "worktree:" and
// its metadata directory name) to its resolved path. Git keeps each linked
// worktree's gitdir backlink through moves, including for a prunable one.
func worktrees(root, common string) (map[string]string, error) {
	out, err := git(root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, item := range bytes.Split(out, []byte{0}) {
		if path, ok := bytes.CutPrefix(item, []byte("worktree ")); ok {
			paths = append(paths, resolve(string(path)))
		}
	}
	if len(paths) == 0 {
		return nil, failure("cannot identify Git worktree; refusing stale cleanup")
	}
	ids := map[string]string{paths[0]: "main"}
	metadata := filepath.Join(common, "worktrees")
	dirs, err := os.ReadDir(metadata)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, d := range dirs {
		backlink, err := os.ReadFile(filepath.Join(metadata, d.Name(), "gitdir"))
		if err != nil {
			return nil, err
		}
		gitdir := strings.TrimSpace(string(backlink))
		if !filepath.IsAbs(gitdir) {
			gitdir = filepath.Join(metadata, d.Name(), gitdir)
		}
		ids[resolve(filepath.Dir(gitdir))] = "worktree:" + d.Name()
	}
	live := map[string]string{}
	for _, path := range paths {
		id, ok := ids[path]
		if !ok {
			return nil, failure("cannot identify Git worktree; refusing stale cleanup")
		}
		live[id] = path
	}
	return live, nil
}

func currentID(root string, live map[string]string) (string, error) {
	for id, path := range live {
		if path == resolve(root) {
			return id, nil
		}
	}
	return "", failure("current Git worktree not listed; refusing cleanup")
}

// metadataToken reads the token make ai-env stores in the worktree's Git
// metadata. Git keeps it on moves, copies and repair but deletes it with the
// worktree, so a reused metadata name cannot inherit a database.
func metadataToken(common, key string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(common, "worktrees", strings.TrimPrefix(key, "worktree:"), "ribbitto-ai-env"))
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(data)), true
}

func databaseName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "ribbitto_dev_" + hex.EncodeToString(sum[:])[:20]
}

func withLock(common string, how int, fn func() error) error {
	lock, err := os.OpenFile(filepath.Join(common, "ai-env.lock"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), how); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	return fn()
}

// resolve follows symlinks like Python's Path.resolve: as far as the path
// exists, keeping the rest, so a removed (prunable) worktree still resolves.
func resolve(path string) string {
	path, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	var rest []string
	for p := path; ; p = filepath.Dir(p) {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(append([]string{real}, rest...)...)
		}
		if p == filepath.Dir(p) {
			return path
		}
		rest = append([]string{filepath.Base(p)}, rest...)
	}
}

func git(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stderr = nil
	return cmd.Output()
}
