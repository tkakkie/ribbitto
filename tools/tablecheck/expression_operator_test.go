package tablecheck

import (
	"fmt"

	"github.com/tkakkie/ribbitto/tools/internal/sqlwalk"
)

// Operator spelling is shared, but production and migration permissions differ.
// An empty result denotes an IN/EXISTS/scalar subquery or an ordinary sort.
func expressionOperator(tag string, n map[string]any) (string, error) {
	field := "name"
	switch tag {
	case "SubLink":
		field = "operName"
	case "SortBy":
		field = "useOp"
	}
	if n[field] == nil && tag != "A_Expr" {
		if n["sortby_dir"] == "SORTBY_USING" || n["subLinkType"] == "ALL_SUBLINK" || n["subLinkType"] == "ROWCOMPARE_SUBLINK" {
			return "", fmt.Errorf("unsupported operator shape in %s", tag)
		}
		return "", nil
	}
	parts := sqlwalk.List(n[field])
	if len(parts) != 1 {
		return "", fmt.Errorf("unsupported qualified operator or operator shape in %s", tag)
	}
	name, ok := sqlwalk.Node(parts[0], "String")["sval"].(string)
	if !ok || name == "" {
		return "", fmt.Errorf("unsupported operator name in %s", tag)
	}
	return name, nil
}
