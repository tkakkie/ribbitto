package sqlwalk

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSplit(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"SELECT 1", "no named queries"},
		{"SELECT 1;\n-- name: Q :one\nSELECT 2", "SQL before first query"},
		{"broken syntax\n-- name: Q :one\nSELECT 2", "SQL before first query"},
		{"-- name: Q :one\nSELECT 1;\n-- name: Q :one\nSELECT 2", "duplicate query"},
		{"-- comment\n-- name: Q :one trailing annotation\nSELECT 1;\n-- name: R :exec\nDELETE FROM t", ""},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			queries, err := Split(tc.sql, "org")
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("want %q, got %v", tc.want, err)
				}
				return
			}
			if err != nil || len(queries) != 2 || strings.TrimSpace(queries["org.Q"]) != "SELECT 1;" || strings.TrimSpace(queries["org.R"]) != "DELETE FROM t" {
				t.Fatalf("queries: %v, %v", queries, err)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	for _, tc := range []struct{ name, path, sql, want string }{
		{"valid", "org/a.sql", "-- name: Q :one\nSELECT 1", ""},
		{"duplicate across files", "org/b.sql", "-- name: Q :one\nSELECT 2", "duplicate query"},
		{"unregistered", "absent/a.sql", "-- name: R :one\nSELECT 1", "unregistered query directory"},
		{"unnamed", "org/b.sql", "SELECT 1", "no named queries"},
		{"nested", "org/nested/a.sql", "-- name: R :one\nSELECT 1", "expected module query directory"},
		{"bare", "a.sql", "-- name: R :one\nSELECT 1", "expected module query directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for path, sql := range map[string]string{"org/a.sql": "-- name: Q :one\nSELECT 1", tc.path: tc.sql} {
				full := filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(sql), 0600); err != nil {
					t.Fatal(err)
				}
			}
			queries, err := Load(root, map[string]bool{"org": true})
			if tc.want == "" {
				if err != nil || len(queries) != 1 || queries["org.Q"] == "" {
					t.Fatalf("load: %v, %v", queries, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
	if _, err := Load(t.TempDir(), nil); err == nil {
		t.Fatal("accepted empty query directory")
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing"), nil); err == nil {
		t.Fatal("accepted missing directory")
	}
}

func TestLexicalWalk(t *testing.T) {
	for _, tc := range []struct {
		name, sql     string
		reads, writes []string
	}{
		{"preceding CTE", "WITH a AS (SELECT * FROM physical), b AS (SELECT * FROM a) SELECT * FROM b", []string{"physical"}, nil},
		{"self name is physical", "WITH physical AS (SELECT * FROM physical) SELECT * FROM physical", []string{"physical"}, nil},
		{"forward name is physical", "WITH a AS (SELECT * FROM b), b AS (SELECT 1) SELECT * FROM a", []string{"b"}, nil},
		{"outer visibility", "WITH a AS (SELECT 1), b AS (WITH c AS (SELECT * FROM a) SELECT * FROM c) SELECT * FROM b", nil, nil},
		{"nested scope", "SELECT (WITH a AS (SELECT 1) SELECT * FROM a) FROM a", []string{"a"}, nil},
		{"physical write", "WITH physical AS (SELECT 1) DELETE FROM physical RETURNING (SELECT * FROM physical)", nil, []string{"physical"}},
		{"qualified CTE name", "WITH a AS (SELECT 1) SELECT * FROM public.a", []string{"a"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree, err := Parse(tc.sql)
			if err != nil {
				t.Fatal(err)
			}
			before := fmt.Sprint(tree)
			var reads, writes []string
			err = Walk(tree, Scope{}, Options{LexicalCTEs: true, Relation: func(r map[string]any, write bool, _ Scope) error {
				if write {
					writes = append(writes, r["relname"].(string))
				} else {
					reads = append(reads, r["relname"].(string))
				}
				return nil
			}})
			if err != nil || !reflect.DeepEqual(reads, tc.reads) || !reflect.DeepEqual(writes, tc.writes) {
				t.Fatalf("reads %v, writes %v, err %v", reads, writes, err)
			}
			if fmt.Sprint(tree) != before {
				t.Fatal("walk mutated AST")
			}
		})
	}
}

func TestTraversal(t *testing.T) {
	tree, err := Parse("WITH d AS (DELETE FROM member RETURNING *) INSERT INTO member VALUES ((SELECT 1))")
	if err != nil {
		t.Fatal(err)
	}
	var statements []string
	err = Walk(tree, Scope{}, Options{NodesOnly: true, OpaqueInserts: true, Visit: func(kind string, _ map[string]any, scope Scope) error {
		if strings.HasSuffix(kind, "Stmt") {
			statements = append(statements, fmt.Sprintf("%s:%d", kind, scope.Level))
		}
		return nil
	}})
	if err != nil || !reflect.DeepEqual(statements, []string{"DeleteStmt:2", "InsertStmt:1"}) {
		t.Fatalf("statements %v, err %v", statements, err)
	}
	ctes, err := CollectCTEs(tree)
	if err != nil || !ctes["d"] || len(ctes) != 1 {
		t.Fatalf("CTEs: %v, %v", ctes, err)
	}
	for _, sql := range []string{"SELECT (SELECT 1)", "WITH x AS (SELECT 1) SELECT 1"} {
		tree, err := Parse(sql)
		if err != nil {
			t.Fatal(err)
		}
		err = Walk(tree, Scope{}, Options{LexicalCTEs: true, NodesOnly: true, Visit: func(kind string, _ map[string]any, scope Scope) error {
			if kind == "SelectStmt" && scope.Level == 2 {
				return fmt.Errorf("nested refusal")
			}
			return nil
		}})
		if err == nil || err.Error() != "nested refusal" {
			t.Fatalf("refusal lost: %v", err)
		}
	}
	tree, err = Parse("WITH RECURSIVE x AS (SELECT 1) SELECT * FROM x")
	if err != nil {
		t.Fatal(err)
	}
	if err := Walk(tree, Scope{}, Options{LexicalCTEs: true}); err == nil {
		t.Fatal("accepted recursive CTE")
	}
	if err := Walk(map[string]any{"UnknownNode": map[string]any{}}, Scope{}, Options{NodesOnly: true, Visit: func(kind string, _ map[string]any, _ Scope) error { return fmt.Errorf("refuse %s", kind) }}); err == nil || err.Error() != "refuse UnknownNode" {
		t.Fatalf("node refusal lost: %v", err)
	}
}

func TestReasons(t *testing.T) {
	for _, rules := range []ReasonRules{{PendingColon: true}, {ReviewComments: true}} {
		for _, reason := range []string{"", " ", "Approved by maintainer", "Approved by maintainer #0", "Approved by maintainer in https://github.com/a/b/pull/1", "PENDING-MAINTAINER: #658", "before pending\u00a0maintainer : #658"} {
			if err := rules.Check(reason); err == nil {
				t.Fatalf("accepted %q with %+v", reason, rules)
			}
		}
		for _, reason := range []string{"Reviewed read", "Approved by maintainer #658", "Approved by maintainer decision 26", "Approved by maintainer https://github.com/a/b/pull/1#issuecomment-2"} {
			if err := rules.Check(reason); err != nil {
				t.Fatalf("rejected %q: %v", reason, err)
			}
		}
	}
	// Preserve scopecheck's existing syntax while keeping tablecheck's broader refusal.
	if err := (ReasonRules{PendingColon: true}).Check("pending_maintainer #658"); err != nil {
		t.Fatal(err)
	}
	if err := (ReasonRules{ReviewComments: true}).Check("pending_maintainer #658"); err == nil {
		t.Fatal("accepted bare pending marker")
	}
	if err := (ReasonRules{ReviewComments: true}).Check("Maintainer approved https://github.com/a/b/pull/1#discussion_r2"); err != nil {
		t.Fatal(err)
	}
	if err := (ReasonRules{PendingColon: true}).Check("Maintainer approved https://github.com/a/b/pull/1#discussion_r2"); err == nil {
		t.Fatal("scope provenance changed")
	}
}
