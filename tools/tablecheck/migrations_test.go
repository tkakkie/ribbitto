package tablecheck

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	pgquery "github.com/pganalyze/pg_query_go/v6"
	"github.com/tkakkie/ribbitto/tools/internal/sqlwalk"
)

// Statement ordinals count parsed Up statements, including DDL, not source lines.
const reviewedMigrationAccess = `00006.backfill.statement2 organization read Maintainer ruling #677: default-channel backfill.
00007.function.organization_event_seq_logged event_log read Maintainer ruling #666: deferred event-sequence invariant.`

type migration struct{ name, sql string }

type migrationExemption struct{ object, table, mode, reason string }
type migrationPolicy struct {
	owners    map[string]string
	functions map[string]string
	sequences map[string]string
	allow     map[migrationExemption]bool
}

func migrationExemptions(text string) (map[migrationExemption]bool, error) {
	allow := map[migrationExemption]bool{}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		p := strings.SplitN(line, " ", 4)
		if len(p) != 4 {
			return nil, fmt.Errorf("invalid migration exemption: %q", line)
		}
		e := migrationExemption{p[0], p[1], p[2], p[3]}
		for old := range allow {
			if old.object == e.object && old.table == e.table && old.mode == e.mode {
				return nil, fmt.Errorf("duplicate migration exemption: %s", e.object)
			}
		}
		if e.object == "" || e.table == "" || (e.mode != "read" && e.mode != "write") {
			return nil, fmt.Errorf("invalid migration exemption: %q", line)
		}
		if err := (sqlwalk.ReasonRules{ReviewComments: true}).Check(e.reason); err != nil {
			return nil, fmt.Errorf("migration exemption %s: %w", e.object, err)
		}
		allow[e] = false
	}
	return allow, nil
}

func (p *migrationPolicy) access(object, table string, write bool) error {
	mode := "read"
	if write {
		mode = "write"
	}
	for e := range p.allow {
		if e.object == object && e.table == table && e.mode == mode {
			p.allow[e] = true
			return nil
		}
	}
	return fmt.Errorf("foreign table %s (write=%v, owner=%s)", table, write, p.owners[table])
}

func (p *migrationPolicy) calls(tree any) error {
	return sqlwalk.Walk(tree, sqlwalk.Scope{}, sqlwalk.Options{NodesOnly: true, Visit: func(tag string, n map[string]any, _ sqlwalk.Scope) error {
		if tag == "FuncCall" {
			name := sqlwalk.Names(n["funcname"])
			// Definitions are unqualified; public qualification cannot evade the call ban.
			if p.functions[strings.TrimPrefix(name, "public.")] != "" {
				return fmt.Errorf("call to migration-defined function %s", name)
			}
		}
		return nil
	}})
}

func (p *migrationPolicy) visit(tag string, n map[string]any, module string) error {
	switch tag {
	case "SelectStmt", "InsertStmt", "UpdateStmt", "DeleteStmt":
		if n["intoClause"] != nil {
			return fmt.Errorf("unsupported SELECT INTO")
		}
	case "FuncCall":
		if err := p.calls(map[string]any{tag: n}); err != nil {
			return err
		}
		switch sqlwalk.Names(n["funcname"]) {
		case "nextval", "currval":
			return p.sequenceCall(n, module)
		case "count", "max", "lower", "row_number":
		default:
			return fmt.Errorf("unsupported migration function %s", sqlwalk.Names(n["funcname"]))
		}
	case "A_Expr":
		switch sqlwalk.Names(n["name"]) {
		case "=", "<", ">", "<=", ">=", "<>", "+", "-", "||":
		default:
			return fmt.Errorf("unsupported migration operator %s", sqlwalk.Names(n["name"]))
		}
	case "ReturnStmt", "RangeVar", "ResTarget", "ColumnRef", "A_Star", "A_Const", "String", "Integer", "ParamRef", "BoolExpr", "NullTest", "SubLink", "RangeSubselect", "JoinExpr", "Alias", "SortBy", "List", "CoalesceExpr", "MinMaxExpr", "OnConflictClause", "InferClause", "IndexElem", "WindowDef":
	default:
		return fmt.Errorf("unsupported migration SQL node %s", tag)
	}
	return nil
}

// Only bare literal names can be resolved without search_path or runtime SQL.
func sequenceName(n map[string]any) (string, error) {
	args := sqlwalk.List(n["args"])
	if len(args) != 1 {
		return "", fmt.Errorf("migration sequence call requires one literal name")
	}
	name, _ := sqlwalk.Object(sqlwalk.Node(args[0], "A_Const")["sval"])["sval"].(string)
	if !regexp.MustCompile(`^[a-z_][a-z0-9_]*$`).MatchString(name) {
		return "", fmt.Errorf("migration sequence call requires an unqualified literal name")
	}
	return name, nil
}

func (p *migrationPolicy) sequenceCall(n map[string]any, module string) error {
	name, err := sequenceName(n)
	if err != nil {
		return err
	}
	owner := p.sequences[name]
	if owner == "" || module == "" || owner != module {
		return fmt.Errorf("unknown or foreign migration sequence %s (owner=%s)", name, owner)
	}
	return nil
}

func (p *migrationPolicy) target(r map[string]any, kind string, tables map[string]bool) (string, error) {
	table, _ := r["relname"].(string)
	if r["schemaname"] != nil || r["catalogname"] != nil {
		return "", fmt.Errorf("unsupported qualified migration %s target %s", kind, table)
	}
	if !tables[table] || p.owners[table] == "" {
		return "", fmt.Errorf("unknown migration %s target %s", kind, table)
	}
	return table, nil
}

func (p *migrationPolicy) foreignKey(n map[string]any, module string, tables map[string]bool) error {
	if n["contype"] != "CONSTR_FOREIGN" {
		return nil
	}
	// REFERENCES embeds its relation without a RangeVar tag, like DDL targets.
	table, err := p.target(sqlwalk.Object(n["pktable"]), "foreign key", tables)
	if err != nil {
		return err
	}
	for _, action := range []struct{ field, clause string }{{"fk_del_action", "ON DELETE"}, {"fk_upd_action", "ON UPDATE"}} {
		name := ""
		// PostgreSQL encodes referential actions as single-character strings.
		switch n[action.field] {
		case "a", "r": // NO ACTION (including the default), RESTRICT.
			continue
		case "c":
			name = "CASCADE"
		case "n":
			name = "SET NULL"
		case "d":
			name = "SET DEFAULT"
		default:
			return fmt.Errorf("unsupported foreign key %s action %v", action.clause, n[action.field])
		}
		// These actions write referencing rows on the referenced table's behalf;
		// migration access exemptions cannot permit this hidden module crossing.
		if module != p.owners[table] {
			return fmt.Errorf("cross-module foreign key to %s (%s -> %s): %s %s writes referencing rows", table, module, p.owners[table], action.clause, name)
		}
	}
	return nil
}

func (p *migrationPolicy) ddlExpressions(tree any, module string, tables map[string]bool) error {
	// Check calls first so sequence ownership refusals survive even when a
	// containing expression uses an unsupported operator or node shape.
	err := sqlwalk.Walk(tree, sqlwalk.Scope{}, sqlwalk.Options{NodesOnly: true, Visit: func(tag string, n map[string]any, _ sqlwalk.Scope) error {
		if tag != "FuncCall" {
			return nil
		}
		switch sqlwalk.Names(n["funcname"]) {
		case "uuidv7", "now", "length", "lower", "octet_length", "starts_with", "btrim":
			return nil
		case "pg_catalog.normalize":
			if len(sqlwalk.List(n["funcname"])) == 2 {
				return nil
			}
		case "nextval", "currval":
			return p.sequenceCall(n, module)
		}
		return fmt.Errorf("unsupported migration DDL function %s", sqlwalk.Names(n["funcname"]))
	}})
	if err != nil {
		return err
	}
	return sqlwalk.Walk(tree, sqlwalk.Scope{}, sqlwalk.Options{NodesOnly: true, Visit: func(tag string, n map[string]any, _ sqlwalk.Scope) error {
		switch tag {
		case "A_Expr":
			name := sqlwalk.Names(n["name"])
			if len(sqlwalk.List(n["name"])) != 1 {
				return fmt.Errorf("unsupported qualified migration DDL operator %s", name)
			}
			// Only operators observed in production DDL are safe to admit here.
			switch name {
			case "=", "<>", ">", "<=", ">=", "~", "!~", "BETWEEN":
			default:
				return fmt.Errorf("unsupported migration DDL operator %s", name)
			}
		case "TypeCast":
			// Production DDL expressions use no casts; type input functions can
			// hide table access just as functions and operators can.
			return fmt.Errorf("unsupported migration DDL type %s", sqlwalk.Names(sqlwalk.Object(n["typeName"])["names"]))
		case "Constraint":
			return p.foreignKey(n, module, tables)
		case "CreateStmt", "AlterTableStmt", "AlterTableCmd", "ColumnDef", "IndexStmt", "IndexElem", "CreateTrigStmt":
			// These are the DDL containers around the expressions, not query nodes.
		case "FuncCall", "A_Const", "ColumnRef", "BoolExpr", "NullTest", "String", "List":
		default:
			return fmt.Errorf("unsupported migration DDL node %s", tag)
		}
		return nil
	}})
}

func checkMigrations(migrations []string, owners map[string]string) error {
	files := make([]migration, len(migrations))
	for i, sql := range migrations {
		files[i] = migration{fmt.Sprintf("%05d", i+1), sql}
	}
	return checkMigrationsWithAccess(files, owners, nil)
}

func migrationUp(sql string) (string, error) {
	var up strings.Builder
	inUp, foundUp := false, false
	for _, line := range strings.Split(sql, "\n") {
		if strings.Contains(line, "+goose") {
			// Goose v3.28 rejects leading spaces/tabs but allows trailing whitespace.
			// Restrict annotations to these whole lines so executed SQL cannot differ.
			switch strings.TrimRightFunc(line, unicode.IsSpace) {
			case "-- +goose Up":
				inUp, foundUp = true, true
			case "-- +goose Down":
				inUp = false
			case "-- +goose StatementBegin", "-- +goose StatementEnd":
			default:
				return "", fmt.Errorf("unsupported migration goose annotation: %q", line)
			}
			continue
		}
		if inUp {
			up.WriteString(line + "\n")
		}
	}
	if !foundUp {
		return "", fmt.Errorf("missing migration Up section")
	}
	return up.String(), nil
}

func checkMigrationsWithAccess(migrations []migration, owners map[string]string, allow map[migrationExemption]bool) error {
	p := &migrationPolicy{owners: owners, functions: map[string]string{}, sequences: map[string]string{}, allow: allow}
	type statement struct {
		tree              any
		kind, object, sql string
		node              map[string]any
	}
	var statements []statement
	tables := map[string]bool{}
	// Collect every definition before checking callers or trigger bindings.
	for _, file := range migrations {
		up, err := migrationUp(file.sql)
		if err != nil {
			return err
		}
		tree, err := sqlwalk.Parse(up)
		if err != nil {
			return err
		}
		for j, raw := range sqlwalk.Statements(tree) {
			r := sqlwalk.Object(raw)
			stmt := sqlwalk.Object(r["stmt"])
			start, _ := r["stmt_location"].(float64)
			length, _ := r["stmt_len"].(float64)
			end := len(up)
			if length != 0 {
				end = int(start + length)
			}
			s := statement{tree: stmt, object: fmt.Sprintf("%s.ddl.statement%d", file.name, j+1), sql: up[int(start):end]}
			for s.kind = range stmt {
				s.node = sqlwalk.Object(stmt[s.kind])
			}
			if s.kind == "InsertStmt" || s.kind == "UpdateStmt" {
				s.object = fmt.Sprintf("%s.backfill.statement%d", file.name, j+1)
			}
			if s.kind == "CreateFunctionStmt" {
				name := sqlwalk.Names(s.node["funcname"])
				if s.node["is_procedure"] == true {
					return fmt.Errorf("unsupported migration procedure %s", name)
				}
				if strings.Contains(name, ".") || p.functions[name] != "" || s.node["replace"] == true || s.node["parameters"] != nil {
					return fmt.Errorf("unsupported migration function definition %s", name)
				}
				s.object = fmt.Sprintf("%s.function.%s", file.name, name)
				p.functions[name] = s.object
			}
			statements = append(statements, s)
		}
	}
	modules := map[string]string{}
	for _, s := range statements {
		n := s.node
		switch s.kind {
		case "CreateStmt":
			for _, option := range []string{"inhRelations", "partbound", "partspec", "ofTypename"} {
				if n[option] != nil {
					return fmt.Errorf("unsupported migration CREATE TABLE %s", option)
				}
			}
			for _, element := range sqlwalk.List(n["tableElts"]) {
				if sqlwalk.Node(element, "TableLikeClause") != nil {
					return fmt.Errorf("unsupported migration CREATE TABLE LIKE")
				}
			}
			r := sqlwalk.Object(n["relation"])
			table, _ := r["relname"].(string)
			if r["schemaname"] != nil || r["catalogname"] != nil || tables[table] {
				return fmt.Errorf("unsupported qualified or duplicate migration table %s", table)
			}
			tables[table] = true
		case "IndexStmt":
		case "CreateSeqStmt":
			r := sqlwalk.Object(n["sequence"])
			name, _ := r["relname"].(string)
			if r["schemaname"] != nil || r["catalogname"] != nil {
				return fmt.Errorf("unsupported qualified migration sequence %s", name)
			}
			if _, exists := p.sequences[name]; exists {
				return fmt.Errorf("duplicate migration sequence %s", name)
			}
			p.sequences[name] = ""
		case "AlterTableStmt":
			if n["objtype"] != "OBJECT_TABLE" {
				return fmt.Errorf("unsupported migration ALTER object %v", n["objtype"])
			}
			for _, cmd := range sqlwalk.List(n["cmds"]) {
				sub := sqlwalk.Node(cmd, "AlterTableCmd")["subtype"]
				switch sub {
				case "AT_AddColumn", "AT_SetNotNull", "AT_AddConstraint":
				default:
					return fmt.Errorf("unsupported migration ALTER TABLE %v", sub)
				}
			}
		case "InsertStmt", "UpdateStmt", "CreateFunctionStmt":
		case "CreateTrigStmt":
			name := sqlwalk.Names(n["funcname"])
			r := sqlwalk.Object(n["relation"])
			module := owners[fmt.Sprint(r["relname"])]
			if p.functions[name] == "" || module == "" || r["schemaname"] != nil || r["catalogname"] != nil {
				return fmt.Errorf("unsupported migration trigger binding %s", name)
			}
			if modules[name] != "" && modules[name] != module {
				return fmt.Errorf("ambiguous trigger function owner %s", name)
			}
			modules[name] = module
		default:
			return fmt.Errorf("unsupported migration statement %s", s.kind)
		}
	}
	// DDL target relations are embedded records, so the ownership walk does not
	// visit them as RangeVar nodes. Resolve them only against created, owned tables.
	for _, s := range statements {
		switch s.kind {
		case "IndexStmt", "AlterTableStmt", "CreateTrigStmt":
			if _, err := p.target(sqlwalk.Object(s.node["relation"]), s.kind, tables); err != nil {
				return fmt.Errorf("%s: %w", s.object, err)
			}
		}
	}
	for table := range tables {
		if owners[table] == "" {
			return fmt.Errorf("migration table has no owner: %s", table)
		}
	}
	for table := range owners {
		if !tables[table] {
			return fmt.Errorf("owned table not created by migrations: %s", table)
		}
	}
	// A direct column default establishes logical module ownership, independently
	// of PostgreSQL OWNED BY (00010 deliberately keeps its sequence unowned).
	for _, s := range statements {
		if s.kind != "CreateStmt" && s.kind != "AlterTableStmt" {
			continue
		}
		module := owners[fmt.Sprint(sqlwalk.Object(s.node["relation"])["relname"])]
		err := sqlwalk.Walk(s.tree, sqlwalk.Scope{}, sqlwalk.Options{NodesOnly: true, Visit: func(tag string, n map[string]any, _ sqlwalk.Scope) error {
			if tag != "ColumnDef" {
				return nil
			}
			for _, constraint := range sqlwalk.List(n["constraints"]) {
				c := sqlwalk.Node(constraint, "Constraint")
				call := sqlwalk.Node(c["raw_expr"], "FuncCall")
				if c["contype"] != "CONSTR_DEFAULT" || sqlwalk.Names(call["funcname"]) != "nextval" {
					continue
				}
				name, err := sequenceName(call)
				if err != nil {
					return err
				}
				owner, created := p.sequences[name]
				if !created || module == "" {
					return fmt.Errorf("unknown migration sequence owner %s", name)
				}
				if owner != "" && owner != module {
					return fmt.Errorf("ambiguous migration sequence owner %s", name)
				}
				p.sequences[name] = module
			}
			return nil
		}})
		if err != nil {
			return fmt.Errorf("%s: %w", s.object, err)
		}
	}
	// Foreign keys can refer only to tables already created at this point,
	// including the current table for self references.
	created := map[string]bool{}
	for _, s := range statements {
		if err := p.calls(s.tree); err != nil {
			return fmt.Errorf("%s: %w", s.object, err)
		}
		switch s.kind {
		case "CreateStmt", "AlterTableStmt", "IndexStmt", "CreateTrigStmt":
			table := fmt.Sprint(sqlwalk.Object(s.node["relation"])["relname"])
			module := owners[table]
			if s.kind == "CreateStmt" {
				created[table] = true
			}
			if err := p.ddlExpressions(s.tree, module, created); err != nil {
				return fmt.Errorf("%s: %w", s.object, err)
			}
		case "CreateFunctionStmt":
			name := sqlwalk.Names(s.node["funcname"])
			module := modules[name]
			if module == "" {
				return fmt.Errorf("unbound migration function %s", name)
			}
			if err := p.body(s.sql, s.node, module, s.object); err != nil {
				return fmt.Errorf("%s: %w", s.object, err)
			}
		case "InsertStmt", "UpdateStmt":
			module := owners[fmt.Sprint(sqlwalk.Object(s.node["relation"])["relname"])]
			if module == "" {
				return fmt.Errorf("unknown backfill owner %s", s.object)
			}
			if err := checkOwnedSQL(s.sql, module, s.object, owners, nil, p); err != nil {
				return fmt.Errorf("%s: %w", s.object, err)
			}
		}
	}
	for e, used := range allow {
		if !used {
			return fmt.Errorf("stale migration exemption: %s %s %s", e.object, e.table, e.mode)
		}
	}
	return nil
}

func (p *migrationPolicy) body(sql string, fn map[string]any, module, object string) error {
	language, body := "", ""
	for _, option := range sqlwalk.List(fn["options"]) {
		def := sqlwalk.Node(option, "DefElem")
		switch def["defname"] {
		case "language":
			language, _ = sqlwalk.Node(def["arg"], "String")["sval"].(string)
		case "as":
			items := sqlwalk.List(sqlwalk.Node(def["arg"], "List")["items"])
			if len(items) != 1 {
				return fmt.Errorf("unsupported migration routine body")
			}
			body, _ = sqlwalk.Node(items[0], "String")["sval"].(string)
		default:
			return fmt.Errorf("unsupported migration function option %v", def["defname"])
		}
	}
	if language == "sql" {
		if fn["sql_body"] != nil {
			return walkOwnedSQL(fn["sql_body"], module, object, p.owners, nil, p, nil)
		}
		tree, err := sqlwalk.Parse(body)
		if err != nil {
			return err
		}
		for _, raw := range sqlwalk.Statements(tree) {
			if err := walkOwnedSQL(sqlwalk.Object(raw)["stmt"], module, object, p.owners, nil, p, nil); err != nil {
				return err
			}
		}
		if len(sqlwalk.Statements(tree)) == 0 {
			return fmt.Errorf("empty migration routine body")
		}
		return nil
	}
	// The pinned PL parser asserts if AS is missing; validate before entering cgo.
	if language != "plpgsql" || body == "" || fn["sql_body"] != nil {
		return fmt.Errorf("unsupported migration routine language or body")
	}
	parsed, err := pgquery.ParsePlPgSqlToJSON(sql)
	if err != nil {
		return fmt.Errorf("parsing migration routine: %w", err)
	}
	var tree any
	if err := json.Unmarshal([]byte(parsed), &tree); err != nil {
		return err
	}
	return p.plpgsql(tree, module, object)
}

func (p *migrationPolicy) plpgsql(tree any, module, object string) error {
	switch v := tree.(type) {
	case []any:
		for _, child := range v {
			if err := p.plpgsql(child, module, object); err != nil {
				return err
			}
		}
	case map[string]any:
		for tag, child := range v {
			n := sqlwalk.Object(child)
			if tag == "PLpgSQL_expr" {
				query, ok := n["query"].(string)
				if !ok {
					return fmt.Errorf("unsupported migration routine expression")
				}
				switch n["parseMode"] {
				case nil, float64(0):
				case float64(2):
					query = "SELECT " + query
				case float64(3), float64(4), float64(5):
					scan, err := pgquery.Scan(query)
					if err != nil {
						return err
					}
					found := false
					for _, token := range scan.Tokens {
						word := query[token.Start:token.End]
						if token.Token == pgquery.Token_SQL_COMMENT || token.Token == pgquery.Token_C_COMMENT {
							continue
						}
						// Array subscripts may themselves contain SQL. Refuse complex
						// targets rather than discard them with the assignment prefix.
						if word != ":=" && word != "=" && word != "." && token.Token != pgquery.Token_IDENT && token.Token != pgquery.Token_NEW && token.Token != pgquery.Token_OLD {
							return fmt.Errorf("unsupported migration routine assignment target")
						}
						if word == ":=" || word == "=" {
							query = "SELECT " + query[token.End:]
							found = true
							break
						}
					}
					if !found {
						return fmt.Errorf("unsupported migration routine assignment")
					}
				default:
					return fmt.Errorf("unsupported migration routine parse mode %v", n["parseMode"])
				}
				if err := checkOwnedSQL(query, module, object, p.owners, nil, p); err != nil {
					return err
				}
				continue
			}
			if strings.HasPrefix(tag, "PLpgSQL_") {
				switch tag {
				case "PLpgSQL_function", "PLpgSQL_var", "PLpgSQL_type", "PLpgSQL_rec", "PLpgSQL_row", "PLpgSQL_recfield", "PLpgSQL_stmt_block", "PLpgSQL_stmt_if", "PLpgSQL_if_elsif", "PLpgSQL_stmt_return", "PLpgSQL_stmt_raise", "PLpgSQL_raise_option", "PLpgSQL_stmt_perform", "PLpgSQL_stmt_execsql", "PLpgSQL_stmt_assign":
				default:
					return fmt.Errorf("unsupported migration routine node %s", tag)
				}
			}
			if err := p.plpgsql(child, module, object); err != nil {
				return err
			}
		}
	}
	return nil
}
