package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// Automatic ports use 20000–32767, below the usual OS ephemeral ranges.
const (
	firstPort = 20000
	portRange = 12768
)

// adminServer runs the statements a reservation needs; tests replace it so the
// registry rules run without a database.
type adminServer interface {
	query(statement string) (string, error)
	migrate(name string) error
}

// stored is an entry as make ai-env writes it. Ports are integers here; on
// read, entry keeps them raw so validate can refuse anything else.
type stored struct {
	Path          string  `json:"path"`
	DatabasePath  string  `json:"database_path"`
	Ports         []int   `json:"ports"`
	MetadataToken *string `json:"metadata_token,omitempty"`
}

// reserve creates or refreshes the current worktree's database, ports and
// .env.local, or with release drops them. Every run also drops the databases
// of reservations whose worktree is gone or whose Git metadata name was
// reused, under the exclusive lock, so two worktrees never share one.
func reserve(dir string, release bool, db adminServer) error {
	root, err := worktreeRoot(dir)
	if err != nil {
		return err
	}
	common, err := commonDir(root)
	if err != nil {
		return err
	}
	return withLock(common, syscall.LOCK_EX, func() error { return reserveLocked(root, common, release, db) })
}

func reserveLocked(root, common string, release bool, db adminServer) error {
	registry := filepath.Join(common, "ai-env.json")
	entries, err := readRegistry(registry)
	if err != nil {
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
	for id, e := range entries {
		path, listed := live[id]
		token, found := metadataToken(common, id)
		if !listed || id != "main" && (e.MetadataToken == nil || *e.MetadataToken == "" || !found || *e.MetadataToken != token) {
			if _, err := db.query(`DROP DATABASE IF EXISTS "` + databaseName(e.DatabasePath) + `" WITH (FORCE)`); err != nil {
				return err
			}
			delete(entries, id)
			continue
		}
		e.Path = path
		entries[id] = e
	}
	e, existed := entries[key]
	if !existed {
		e = stored{Path: root, DatabasePath: root, Ports: []int{}}
	}
	name := databaseName(e.DatabasePath)
	if release {
		if _, err := db.query(`DROP DATABASE IF EXISTS "` + name + `" WITH (FORCE)`); err != nil {
			return err
		}
		delete(entries, key)
		if err := os.Remove(filepath.Join(root, ".env.local")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return writeRegistry(registry, entries)
	}
	if key != "main" && !existed {
		token, err := newToken(common, key)
		if err != nil {
			return err
		}
		e.MetadataToken = &token
	}
	used := map[int]bool{}
	for id, other := range entries {
		if id != key {
			for _, port := range other.Ports {
				used[port] = true
			}
		}
	}
	var ports []int
	for i, override := range []string{"AI_APP_PORT", "AI_METRICS_PORT"} {
		port, err := choosePort(override, e.Ports, i, root, used)
		if err != nil {
			return err
		}
		ports = append(ports, port)
		used[port] = true
	}
	count, err := db.query("SELECT count(*) FROM pg_database WHERE datname='" + name + "'")
	if err != nil {
		return err
	}
	if count == "0" {
		if _, err := db.query(`CREATE DATABASE "` + name + `" TEMPLATE template0`); err != nil {
			return err
		}
	}
	if err := db.migrate(name); err != nil {
		return failure("dev database migration failed")
	}
	e.Ports = ports
	entries[key] = e
	local := fmt.Sprintf("AI_DATABASE=%s\nAI_APP_PORT=%d\nAI_METRICS_PORT=%d\n", name, ports[0], ports[1])
	if err := atomicWrite(filepath.Join(root, ".env.local"), local); err != nil {
		return err
	}
	return writeRegistry(registry, entries)
}

// choosePort keeps a free previous reservation, or walks the range from a
// path-derived candidate. An explicit override is checked once and never
// replaced.
func choosePort(override string, previous []int, i int, root string, used map[int]bool) (int, error) {
	explicit := os.Getenv(override)
	candidate, tries := 0, portRange
	switch {
	case explicit != "":
		n, err := strconv.Atoi(explicit)
		if err != nil {
			return 0, failure("port collision or invalid override: " + override)
		}
		candidate, tries = n, 1
	case len(previous) > i:
		candidate = previous[i]
	default:
		candidate = initialPort(root)
	}
	for offset := 0; offset < tries; offset++ {
		port := candidate
		if offset > 0 {
			port = firstPort + ((candidate-firstPort+offset)%portRange+portRange)%portRange
		}
		if port >= 1 && port <= 65535 && !used[port] && free(port) {
			return port, nil
		}
	}
	return 0, failure("port collision or invalid override: " + override)
}

func initialPort(key string) int {
	sum := sha256.Sum256([]byte(key))
	n, _ := strconv.ParseUint(hex.EncodeToString(sum[:])[:8], 16, 64)
	return firstPort + int(n%portRange)
}

// free reports whether the port can be bound on both IPv4 and IPv6, so a
// server listening on either family counts as taken. Go sets SO_REUSEADDR on
// listeners, which lets the wildcard bind succeed beside a server on
// 127.0.0.1; it is cleared to test the port as a plain bind would.
func free(port int) bool {
	config := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var err error
		if controlErr := c.Control(func(fd uintptr) {
			err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 0)
		}); controlErr != nil {
			return controlErr
		}
		return err
	}}
	for _, network := range [][2]string{{"tcp4", "0.0.0.0"}, {"tcp6", "[::]"}} {
		l, err := config.Listen(context.Background(), network[0], network[1]+":"+strconv.Itoa(port))
		if err != nil {
			return false
		}
		_ = l.Close()
	}
	return true
}

// newToken stores a fresh token in the worktree's Git metadata. Git keeps the
// file on moves, copies and repair but deletes it with the worktree, so a
// reused metadata name cannot inherit the old database.
func newToken(common, key string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	path := filepath.Join(common, "worktrees", strings.TrimPrefix(key, "worktree:"), "ribbitto-ai-env")
	return token, atomicWrite(path, token+"\n")
}

func readRegistry(path string) (map[string]stored, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]stored{}, nil
	}
	if err != nil {
		return nil, err
	}
	entries := map[string]stored{}
	return entries, json.Unmarshal(data, &entries)
}

func writeRegistry(path string, entries map[string]stored) error {
	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	return atomicWrite(path, string(data)+"\n")
}

// atomicWrite publishes value with a rename, so a reader never sees half a
// file.
func atomicWrite(path, value string) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".ai-env-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.WriteString(value); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// server is the admin connection, opened on first use, with the same phase
// messages as the statement mode and never the driver error.
type server struct {
	conn *pgx.Conn
}

func (s *server) connect(ctx context.Context) (*pgx.ConnConfig, error) {
	config, err := pgx.ParseConfig(os.Getenv("RIBBITTO_TEST_DATABASE_URL"))
	if os.Getenv("RIBBITTO_TEST_DATABASE_URL") == "" || err != nil {
		return nil, failure("admin URL not set or invalid")
	}
	if s.conn == nil {
		if s.conn, err = pgx.ConnectConfig(ctx, config); err != nil {
			return nil, failure("PostgreSQL stopped or unreachable")
		}
	}
	return config, nil
}

func (s *server) query(statement string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if _, err := s.connect(ctx); err != nil {
		return "", err
	}
	var out string
	rows, err := s.conn.Query(ctx, statement)
	if err != nil {
		return "", failure("PostgreSQL statement failed; check admin permissions")
	}
	defer rows.Close()
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return "", failure("PostgreSQL statement failed; check admin permissions")
		}
		out = fmt.Sprint(values[0])
	}
	if rows.Err() != nil {
		return "", failure("PostgreSQL statement failed; check admin permissions")
	}
	return out, nil
}

func (s *server) migrate(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	config, err := s.connect(ctx)
	if err != nil {
		return err
	}
	config.Database = name
	db := stdlib.OpenDB(*config)
	defer func() { _ = db.Close() }()
	return postgres.Migrate(ctx, db, "up", io.Discard)
}

func (s *server) close() {
	if s.conn != nil {
		_ = s.conn.Close(context.Background())
	}
}
