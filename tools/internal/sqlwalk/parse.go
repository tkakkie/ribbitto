package sqlwalk

import (
	"encoding/json"
	"fmt"
	"strings"

	pgquery "github.com/pganalyze/pg_query_go/v6"
)

// Object returns a JSON object, or nil for another shape.
func Object(v any) map[string]any { m, _ := v.(map[string]any); return m }

// List returns a JSON array, or nil for another shape.
func List(v any) []any { a, _ := v.([]any); return a }

// Node returns the record for a tagged PostgreSQL node.
func Node(v any, key string) map[string]any { return Object(Object(v)[key]) }

// Names joins PostgreSQL String nodes without losing identifier boundaries.
func Names(v any) string {
	var parts []string
	for _, item := range List(v) {
		parts = append(parts, fmt.Sprint(Node(item, "String")["sval"]))
	}
	return strings.Join(parts, ".")
}

// Parse decodes PostgreSQL's JSON parse tree.
func Parse(sql string) (any, error) {
	s, err := pgquery.ParseToJSON(sql)
	if err != nil {
		return nil, fmt.Errorf("parsing SQL: %w", err)
	}
	var tree any
	if err := json.Unmarshal([]byte(s), &tree); err != nil {
		return nil, fmt.Errorf("decoding SQL AST: %w", err)
	}
	return tree, nil
}

// Statements returns the raw statement records in a parsed tree.
func Statements(tree any) []any { return List(Object(tree)["stmts"]) }

// BigintLocations distinguishes parser-rewritten BIGINT from explicit pg_catalog.int8.
func BigintLocations(sql string) (map[float64]bool, error) {
	scan, err := pgquery.Scan(sql)
	if err != nil {
		return nil, fmt.Errorf("scanning SQL: %w", err)
	}
	locations := map[float64]bool{}
	for _, token := range scan.Tokens {
		if token.Token == pgquery.Token_BIGINT {
			locations[float64(token.Start)] = true
		}
	}
	return locations, nil
}
