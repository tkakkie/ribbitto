package sqlwalk

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Split separates sqlc named queries using module.QueryName keys. Comments before
// the first header are allowed, but executable unnamed SQL and duplicates fail.
func Split(sql, module string) (map[string]string, error) {
	header := regexp.MustCompile(`(?m)^-- name: (\w+) :\w+[^\n]*`)
	matches := header.FindAllStringSubmatchIndex(sql, -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("no named queries")
	}
	prefix, err := Parse(sql[:matches[0][0]])
	if err != nil || len(Statements(prefix)) != 0 {
		return nil, fmt.Errorf("SQL before first query: %v", err)
	}
	queries := map[string]string{}
	for i, match := range matches {
		name, end := module+"."+sql[match[2]:match[3]], len(sql)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		if _, exists := queries[name]; exists {
			return nil, fmt.Errorf("duplicate query: %s", name)
		}
		queries[name] = sql[match[1]:end]
	}
	return queries, nil
}

// Load reads db/queries/<module>/*.sql and rejects duplicates across files.
// A non-nil modules registry also rejects unregistered query directories.
func Load(root string, modules map[string]bool) (map[string]string, error) {
	queries := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".sql" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 2 {
			return fmt.Errorf("%s: expected module query directory", path)
		}
		module := parts[0]
		if modules != nil && !modules[module] {
			return fmt.Errorf("%s: unregistered query directory", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		named, err := Split(string(data), module)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		for name, sql := range named {
			if _, exists := queries[name]; exists {
				return fmt.Errorf("duplicate query: %s", name)
			}
			queries[name] = sql
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(queries) == 0 {
		return nil, fmt.Errorf("no named queries in %s", root)
	}
	return queries, nil
}
