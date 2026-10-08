package sqlwalk

import (
	"fmt"
	"maps"
	"strings"
)

// Scope carries CTE visibility and statement nesting (outermost statement is 1).
// Each lexical statement gets a copy, so nested CTEs cannot escape to siblings.
type Scope struct {
	CTEs  map[string]bool
	Level int
}

// Physical distinguishes physical or unknown relations from unqualified CTE reads.
// Missing names must not match a CTE through a formatted placeholder.
func (s Scope) Physical(relation map[string]any) bool {
	name, ok := relation["relname"].(string)
	if !ok {
		return true
	}
	return relation["schemaname"] != nil || relation["catalogname"] != nil || !s.CTEs[name]
}

// Options selects traversal semantics; all acceptance and refusal is in Visit.
type Options struct {
	Visit         func(string, map[string]any, Scope) error
	NodesOnly     bool // Visit tagged nodes rather than JSON field names as well.
	LexicalCTEs   bool // Visit nonrecursive bodies before exposing each CTE name.
	OpaqueInserts bool // Visit WITH first, then let Visit check the INSERT body.
	// Relation visits write targets separately and excludes them from read traversal.
	Relation func(map[string]any, bool, Scope) error
}

// IsNode identifies PostgreSQL node tags rather than lowercase JSON fields.
func IsNode(kind string) bool { return kind != "" && kind[0] >= 'A' && kind[0] <= 'Z' }

// Walk visits statements, CTEs, subqueries and expressions, propagating refusals.
// It never mutates the parsed tree, so multiple policies can inspect it safely.
func Walk(value any, scope Scope, options Options) error {
	switch v := value.(type) {
	case []any:
		for _, child := range v {
			if err := Walk(child, scope, options); err != nil {
				return err
			}
		}
	case map[string]any:
		for kind, child := range v {
			n, current := Object(child), scope
			tagged := IsNode(kind)
			if tagged && strings.HasSuffix(kind, "Stmt") {
				current.Level++
				if options.LexicalCTEs && (kind == "SelectStmt" || kind == "InsertStmt" || kind == "UpdateStmt" || kind == "DeleteStmt") {
					current.CTEs = maps.Clone(scope.CTEs)
					if current.CTEs == nil {
						current.CTEs = map[string]bool{}
					}
					n = maps.Clone(n)
					if with := Object(n["withClause"]); with != nil {
						if with["recursive"] == true {
							return fmt.Errorf("unsupported recursive CTE")
						}
						for _, item := range List(with["ctes"]) {
							cte := Node(item, "CommonTableExpr")
							if err := Walk(cte, current, options); err != nil {
								return err
							}
							current.CTEs[fmt.Sprint(cte["ctename"])] = true
						}
						delete(n, "withClause")
					}
					child = n
				}
			}
			if kind == "InsertStmt" && options.OpaqueInserts {
				if err := Walk(n["withClause"], current, options); err != nil {
					return err
				}
			}
			if options.Visit != nil && (!options.NodesOnly || tagged) {
				if err := options.Visit(kind, n, current); err != nil {
					return err
				}
			}
			if kind == "InsertStmt" && options.OpaqueInserts {
				continue
			}
			if options.Relation != nil && (kind == "InsertStmt" || kind == "UpdateStmt" || kind == "DeleteStmt") {
				if err := options.Relation(Object(n["relation"]), true, current); err != nil {
					return err
				}
				n = maps.Clone(n)
				delete(n, "relation")
				child = n
			}
			if kind == "RangeVar" && options.Relation != nil && current.Physical(n) {
				if err := options.Relation(n, false, current); err != nil {
					return err
				}
			}
			if err := Walk(child, current, options); err != nil {
				return err
			}
		}
	}
	return nil
}

// CollectCTEs preserves scopecheck's statement-wide name collection. Its caller
// rejects shadowing; lexical walkers instead expose names after their bodies.
func CollectCTEs(tree any) (map[string]bool, error) {
	names := map[string]bool{}
	err := Walk(tree, Scope{}, Options{OpaqueInserts: true, Visit: func(kind string, n map[string]any, _ Scope) error {
		if kind == "CommonTableExpr" {
			names[fmt.Sprint(n["ctename"])] = true
		}
		return nil
	}})
	return names, err
}
