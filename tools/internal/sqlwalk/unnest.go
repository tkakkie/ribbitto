package sqlwalk

import "fmt"

// UnnestFrom returns the locations of permitted unnest calls and rejects all other
// unnest shapes. Only SELECT and UPDATE FROM calls with bigint[] or uuid[]
// sqlc.arg parameters are eligible; expressions cannot inherit permission from
// a nearby allowed call. WITH ORDINALITY requires one argument and two named
// output columns, so parallel arrays can be joined by position.
// Other functions, table ownership and organisation scope remain caller policies.
func UnnestFrom(tree any, bigints map[float64]bool) (map[float64]bool, error) {
	calls := map[float64]bool{}
	var from func(any) error
	from = func(v any) error {
		if j := Node(v, "JoinExpr"); j != nil {
			if err := from(j["larg"]); err != nil {
				return err
			}
			return from(j["rarg"])
		}
		r := Node(v, "RangeFunction")
		functions := List(r["functions"])
		if len(functions) != 1 {
			return nil
		}
		items := List(Node(functions[0], "List")["items"])
		if len(items) != 2 {
			return nil
		}
		f := Node(items[0], "FuncCall")
		if len(List(f["funcname"])) != 1 || Names(f["funcname"]) != "unnest" {
			return nil
		}
		if r["lateral"] == true || r["is_rowsfrom"] == true || r["coldeflist"] != nil || len(Object(items[1])) != 0 {
			return fmt.Errorf("unsupported unnest relation shape")
		}
		if r["ordinality"] == true {
			if len(List(f["args"])) != 1 {
				return fmt.Errorf("unsupported unnest ordinality: requires one argument")
			}
			if len(List(Object(r["alias"])["colnames"])) != 2 {
				return fmt.Errorf("unsupported unnest ordinality: requires alias with two columns")
			}
		}
		// Reject call decorations rather than inheriting aggregate or window semantics.
		if len(f) != 4 || f["funcformat"] != "COERCE_EXPLICIT_CALL" || len(List(f["args"])) == 0 {
			return fmt.Errorf("unsupported unnest call shape")
		}
		for _, arg := range List(f["args"]) {
			cast := Node(arg, "TypeCast")
			typ := Object(cast["typeName"])
			bounds := List(typ["arrayBounds"])
			bigint := len(List(typ["names"])) == 2 && Names(typ["names"]) == "pg_catalog.int8" && bigints[typ["location"].(float64)]
			uuid := len(List(typ["names"])) == 1 && Names(typ["names"]) == "uuid"
			if (!bigint && !uuid) ||
				len(bounds) != 1 || Node(bounds[0], "Integer")["ival"] != float64(-1) || typ["typmods"] != nil || typ["setof"] == true {
				return fmt.Errorf("unsupported unnest argument: requires bigint[] or uuid[] sqlc.arg parameter")
			}
			parameter := Node(cast["arg"], "FuncCall")
			args := List(parameter["args"])
			if len(parameter) != 4 || len(List(parameter["funcname"])) != 2 || Names(parameter["funcname"]) != "sqlc.arg" ||
				parameter["funcformat"] != "COERCE_EXPLICIT_CALL" || len(args) != 1 {
				return fmt.Errorf("unsupported unnest argument: requires bigint[] or uuid[] sqlc.arg parameter")
			}
			fields := List(Node(args[0], "ColumnRef")["fields"])
			literal := Object(Node(args[0], "A_Const")["sval"])["sval"]
			if !(len(fields) == 1 && Node(fields[0], "String")["sval"] != nil) && literal == nil {
				return fmt.Errorf("unsupported unnest parameter name")
			}
		}
		calls[f["location"].(float64)] = true
		return nil
	}
	err := Walk(tree, Scope{}, Options{NodesOnly: true, Visit: func(tag string, n map[string]any, _ Scope) error {
		if tag == "SelectStmt" || tag == "UpdateStmt" {
			for _, ref := range List(n["fromClause"]) {
				if err := from(ref); err != nil {
					return err
				}
			}
		}
		return nil
	}})
	if err != nil {
		return nil, err
	}
	err = Walk(tree, Scope{}, Options{NodesOnly: true, Visit: func(tag string, n map[string]any, _ Scope) error {
		if tag == "FuncCall" {
			parts := List(n["funcname"])
			if len(parts) > 0 && Node(parts[len(parts)-1], "String")["sval"] == "unnest" && !calls[n["location"].(float64)] {
				return fmt.Errorf("unsupported function unnest: requires unqualified FROM call")
			}
		}
		return nil
	}})
	return calls, err
}
