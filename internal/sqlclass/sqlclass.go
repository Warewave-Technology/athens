// Package sqlclass parses one SQL statement with the real PostgreSQL parser
// (libpg_query via wasm, ADR-008) and classifies it (SPEC §7.2).
package sqlclass

import (
	"errors"
	"reflect"
	"regexp"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"
	pgquery "github.com/wasilibs/go-pgquery"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type Class string

const (
	Read    Class = "read"
	DML     Class = "dml"
	DDL     Class = "ddl"
	Session Class = "session" // transaction/session control: rejected
	NonTx   Class = "nontx"   // cannot run inside a transaction block: rejected (ADR-013)
)

type Result struct {
	Class       Class  `json:"class"`
	Destructive bool   `json:"destructive"`
	Summary     string `json:"summary"`
	// Statement is the exact text of the single statement (no trailing ;).
	Statement string `json:"-"`
	// Offset is the byte offset of Statement within the input.
	Offset int `json:"-"`
	// Cursorable is true for plain row-returning reads that may be wrapped in
	// DECLARE CURSOR (SELECT/VALUES/TABLE without INTO or locking).
	Cursorable bool `json:"-"`
	// Reason explains Session/NonTx rejections and side-effect promotions.
	Reason string `json:"reason,omitempty"`
}

var (
	ErrEmpty    = errors.New("empty statement")
	ErrMultiple = errors.New("only one statement per execution is supported")
)

// SyntaxError carries the 1-based character position of the error.
type SyntaxError struct {
	Message  string
	Position int
}

func (e *SyntaxError) Error() string { return e.Message }

// Classify parses sql and classifies its single statement.
func Classify(sql string) (*Result, error) {
	tree, err := pgquery.Parse(sql)
	if err != nil {
		return nil, &SyntaxError{Message: err.Error(), Position: cursorPos(err)}
	}
	switch len(tree.Stmts) {
	case 0:
		return nil, ErrEmpty
	case 1:
	default:
		return nil, ErrMultiple
	}
	raw := tree.Stmts[0]
	stmt, off := statementText(sql, raw)
	r := &Result{Statement: stmt, Offset: off}
	classify(raw.Stmt, r)
	if r.Summary == "" {
		r.Summary = shorten(stmt)
	}
	return r, nil
}

// cursorPos reads Cursorpos from the parser's (internal) error type.
func cursorPos(err error) int {
	v := reflect.ValueOf(err)
	if v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	if v.Kind() == reflect.Struct {
		if f := v.FieldByName("Cursorpos"); f.IsValid() && f.CanInt() {
			return int(f.Int())
		}
	}
	return 0
}

func statementText(sql string, raw *pg.RawStmt) (string, int) {
	start := int(raw.StmtLocation)
	end := len(sql)
	if raw.StmtLen > 0 {
		end = start + int(raw.StmtLen)
	}
	if start < 0 || start > len(sql) || end > len(sql) || start > end {
		start, end = 0, len(sql)
	}
	seg := sql[start:end]
	trimmed := strings.TrimLeft(seg, " \t\r\n")
	start += len(seg) - len(trimmed)
	return strings.TrimRight(trimmed, "; \t\r\n"), start
}

var spaces = regexp.MustCompile(`\s+`)

func shorten(s string) string {
	s = spaces.ReplaceAllString(strings.TrimSpace(s), " ")
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	return s
}

func classify(n *pg.Node, r *Result) {
	switch s := n.Node.(type) {
	case *pg.Node_SelectStmt:
		r.Class = Read
		sel := s.SelectStmt
		switch {
		case sel.IntoClause != nil:
			r.Class, r.Reason = DML, "SELECT INTO creates a table"
		case len(sel.LockingClause) > 0:
			r.Class, r.Reason = DML, "row locking (FOR UPDATE/SHARE)"
		default:
			r.Cursorable = true
		}
	case *pg.Node_VariableShowStmt:
		r.Class = Read
	case *pg.Node_ExplainStmt:
		if explainAnalyze(s.ExplainStmt) {
			classify(s.ExplainStmt.Query, r)
			r.Cursorable = false
			r.Summary = "EXPLAIN ANALYZE " + r.Summary
			return
		}
		// Planning evaluates stable functions, so the explained query is
		// walked below like any other (side effects, credential catalogs).
		r.Class = Read
	case *pg.Node_InsertStmt:
		r.Class = DML
	case *pg.Node_MergeStmt:
		// MERGE can update or delete every matching row; always confirm.
		r.Class, r.Destructive = DML, true
		r.Summary = "MERGE INTO " + rangeVar(s.MergeStmt.Relation)
	case *pg.Node_UpdateStmt:
		r.Class = DML
		if isConstant(s.UpdateStmt.WhereClause) {
			r.Destructive = true
			r.Summary = "UPDATE " + rangeVar(s.UpdateStmt.Relation) + whereNote(s.UpdateStmt.WhereClause)
		}
	case *pg.Node_DeleteStmt:
		r.Class = DML
		if isConstant(s.DeleteStmt.WhereClause) {
			r.Destructive = true
			r.Summary = "DELETE FROM " + rangeVar(s.DeleteStmt.Relation) + whereNote(s.DeleteStmt.WhereClause)
		}
	case *pg.Node_CopyStmt:
		c := s.CopyStmt
		switch {
		case c.Filename == "" && !c.IsProgram:
			r.Class, r.Reason = Session, "COPY FROM STDIN / TO STDOUT is not supported; use INSERT or SELECT"
		case c.IsFrom:
			r.Class = DML
		default:
			r.Class = DDL // writes a server-side file or runs a program
		}
	case *pg.Node_VariableSetStmt, *pg.Node_TransactionStmt, *pg.Node_ListenStmt, *pg.Node_UnlistenStmt,
		*pg.Node_NotifyStmt, *pg.Node_PrepareStmt, *pg.Node_ExecuteStmt, *pg.Node_DeallocateStmt, *pg.Node_DiscardStmt,
		*pg.Node_DeclareCursorStmt, *pg.Node_FetchStmt, *pg.Node_ClosePortalStmt, *pg.Node_LoadStmt, *pg.Node_LockStmt,
		*pg.Node_ConstraintsSetStmt:
		r.Class, r.Reason = Session, "transaction and session control statements are managed by athens"
	case *pg.Node_VacuumStmt:
		r.Class = DDL // plain ANALYZE runs fine in a transaction
		if s.VacuumStmt.IsVacuumcmd {
			r.Class, r.Reason = NonTx, "VACUUM cannot run inside a transaction block"
		}
	case *pg.Node_DropdbStmt, *pg.Node_DropTableSpaceStmt, *pg.Node_DropSubscriptionStmt:
		r.Class, r.Destructive, r.Reason = NonTx, true, "this statement cannot run inside a transaction block"
	case *pg.Node_CreatedbStmt, *pg.Node_CreateTableSpaceStmt, *pg.Node_AlterSystemStmt, *pg.Node_CreateSubscriptionStmt,
		*pg.Node_AlterDatabaseStmt:
		r.Class, r.Reason = NonTx, "this statement cannot run inside a transaction block"
	case *pg.Node_IndexStmt:
		r.Class = DDL
		if s.IndexStmt.Concurrent {
			r.Class, r.Reason = NonTx, "CREATE INDEX CONCURRENTLY cannot run inside a transaction block"
		}
	case *pg.Node_ReindexStmt:
		r.Class = DDL
		for _, p := range s.ReindexStmt.Params {
			if d := p.GetDefElem(); d != nil && strings.EqualFold(d.Defname, "concurrently") && boolArg(d) {
				r.Class, r.Reason = NonTx, "REINDEX CONCURRENTLY cannot run inside a transaction block"
			}
		}
	case *pg.Node_DropStmt:
		r.Class, r.Destructive = DDL, true
		if s.DropStmt.Concurrent {
			r.Class, r.Reason = NonTx, "DROP INDEX CONCURRENTLY cannot run inside a transaction block"
		}
		r.Summary = "DROP " + objectType(s.DropStmt.RemoveType) + " " + objectNames(s.DropStmt.Objects)
	case *pg.Node_DropOwnedStmt, *pg.Node_DropRoleStmt, *pg.Node_DropUserMappingStmt:
		r.Class, r.Destructive = DDL, true
	case *pg.Node_TruncateStmt:
		r.Class, r.Destructive = DDL, true
		r.Summary = "TRUNCATE " + objectNames(s.TruncateStmt.Relations)
	case *pg.Node_AlterTableStmt:
		r.Class = DDL
		for _, c := range s.AlterTableStmt.Cmds {
			cmd := c.GetAlterTableCmd()
			if cmd == nil {
				continue
			}
			switch cmd.Subtype {
			case pg.AlterTableType_AT_DropColumn:
				r.Destructive = true
				r.Summary = "ALTER TABLE " + rangeVar(s.AlterTableStmt.Relation) + " DROP COLUMN " + cmd.Name
			case pg.AlterTableType_AT_DropConstraint:
				r.Destructive = true
				r.Summary = "ALTER TABLE " + rangeVar(s.AlterTableStmt.Relation) + " DROP CONSTRAINT " + cmd.Name
			}
		}
	case *pg.Node_DoStmt:
		// An anonymous code block can do anything: always confirm.
		r.Class, r.Destructive, r.Reason = DDL, true, "DO blocks run arbitrary code"
	default:
		r.Class = DDL // CREATE/ALTER/COMMENT/GRANT/REVOKE/CLUSTER/REFRESH/CALL/... and anything unknown
	}

	// Anything that modifies data anywhere in the tree (data-modifying CTEs,
	// sub-statements) or calls a side-effect function makes a read a write;
	// reading a credentials catalog is refused outright.
	// A plain EXPLAIN only plans: data-modifying statements inside it do not
	// run, but functions may be evaluated while planning.
	planOnly := n.GetExplainStmt() != nil
	credentials := ""
	walk(n.ProtoReflect(), func(m protoreflect.Message) {
		if planOnly {
			switch m.Interface().(type) {
			case *pg.InsertStmt, *pg.MergeStmt, *pg.UpdateStmt, *pg.DeleteStmt, *pg.SelectStmt:
				return
			}
		}
		switch x := m.Interface().(type) {
		case *pg.InsertStmt, *pg.MergeStmt:
			promote(r, "data-modifying statement inside the query")
		case *pg.UpdateStmt:
			promote(r, "data-modifying statement inside the query")
			if isConstant(x.WhereClause) && !r.Destructive {
				r.Destructive, r.Summary = true, "UPDATE "+rangeVar(x.Relation)+whereNote(x.WhereClause)
			}
		case *pg.DeleteStmt:
			promote(r, "data-modifying statement inside the query")
			if isConstant(x.WhereClause) && !r.Destructive {
				r.Destructive, r.Summary = true, "DELETE FROM "+rangeVar(x.Relation)+whereNote(x.WhereClause)
			}
		case *pg.SelectStmt:
			if x.IntoClause != nil || len(x.LockingClause) > 0 {
				promote(r, "SELECT INTO or row locking inside the query")
			}
		case *pg.FuncCall:
			if name := funcName(x); sideEffect(name) {
				promote(r, "calls side-effect function "+name+"()")
			}
		case *pg.RangeVar:
			if SensitiveRelation(x.Schemaname, x.Relname) {
				credentials = x.Relname
			}
		}
	})
	if credentials != "" {
		r.Class, r.Cursorable = Session, false
		r.Reason = "reading " + credentials + " is not allowed: it holds credentials (ADR-026)"
	}
}

// sensitiveRelations are the catalogs that hold password hashes, user mapping
// options or subscription connection strings (with passwords). A superuser
// login, and pg_read_all_data, can read them, so athens refuses them for
// everyone (editor and grid, ADR-026, ADR-027). Keys are schema.name;
// pg_catalog relations also match unqualified names.
var sensitiveRelations = map[string]bool{
	"pg_catalog.pg_authid": true, "pg_catalog.pg_shadow": true, "pg_catalog.pg_user_mapping": true,
	"pg_catalog.pg_user_mappings": true, "pg_catalog.pg_subscription": true,
	"information_schema._pg_user_mappings": true, "information_schema.user_mapping_options": true,
	"information_schema._pg_foreign_servers": true,
}

// SensitiveRelation reports whether schema.name is a credentials catalog.
func SensitiveRelation(schema, name string) bool {
	schema, name = strings.ToLower(schema), strings.ToLower(name)
	if schema == "" {
		schema = "pg_catalog"
	}
	return sensitiveRelations[schema+"."+name]
}

func promote(r *Result, reason string) {
	if r.Class == Read {
		r.Class, r.Reason, r.Cursorable = DML, reason, false
	}
}

func explainAnalyze(e *pg.ExplainStmt) bool {
	for _, o := range e.Options {
		if d := o.GetDefElem(); d != nil && strings.EqualFold(d.Defname, "analyze") && boolArg(d) {
			return true
		}
	}
	return false
}

// boolArg interprets an option argument: absent means true.
func boolArg(d *pg.DefElem) bool {
	if d.Arg == nil {
		return true
	}
	if b := d.Arg.GetBoolean(); b != nil {
		return b.Boolval
	}
	if s := d.Arg.GetString_(); s != nil {
		v := strings.ToLower(s.Sval)
		return v == "true" || v == "on" || v == "1" || v == "yes"
	}
	if i := d.Arg.GetInteger(); i != nil {
		return i.Ival != 0
	}
	return true
}

// isConstant reports a missing WHERE or one that references no column,
// subquery, parameter or function (e.g. WHERE true, WHERE 1 = 1).
func isConstant(where *pg.Node) bool {
	if where == nil {
		return true
	}
	// "WHERE true OR id = 1" matches every row: one constant OR branch is enough.
	if b := where.GetBoolExpr(); b != nil && b.Boolop == pg.BoolExprType_OR_EXPR {
		for _, a := range b.Args {
			if isConstant(a) {
				return true
			}
		}
		return false
	}
	constant := true
	walk(where.ProtoReflect(), func(m protoreflect.Message) {
		switch m.Interface().(type) {
		case *pg.ColumnRef, *pg.SubLink, *pg.ParamRef, *pg.FuncCall, *pg.CurrentOfExpr:
			constant = false
		}
	})
	return constant
}

func whereNote(where *pg.Node) string {
	if where == nil {
		return " without WHERE"
	}
	return " with a constant WHERE"
}

func rangeVar(rv *pg.RangeVar) string {
	if rv == nil {
		return ""
	}
	if rv.Schemaname != "" {
		return rv.Schemaname + "." + rv.Relname
	}
	return rv.Relname
}

func objectType(t pg.ObjectType) string {
	return strings.ReplaceAll(strings.TrimPrefix(t.String(), "OBJECT_"), "_", " ")
}

func objectNames(nodes []*pg.Node) string {
	var names []string
	for _, n := range nodes {
		switch {
		case n.GetRangeVar() != nil:
			names = append(names, rangeVar(n.GetRangeVar()))
		case n.GetList() != nil:
			var parts []string
			for _, it := range n.GetList().Items {
				if s := it.GetString_(); s != nil {
					parts = append(parts, s.Sval)
				}
			}
			names = append(names, strings.Join(parts, "."))
		case n.GetString_() != nil:
			names = append(names, n.GetString_().Sval)
		case n.GetTypeName() != nil:
			var parts []string
			for _, it := range n.GetTypeName().Names {
				if s := it.GetString_(); s != nil {
					parts = append(parts, s.Sval)
				}
			}
			names = append(names, strings.Join(parts, "."))
		case n.GetObjectWithArgs() != nil:
			var parts []string
			for _, it := range n.GetObjectWithArgs().Objname {
				if s := it.GetString_(); s != nil {
					parts = append(parts, s.Sval)
				}
			}
			names = append(names, strings.Join(parts, ".")+"()")
		}
	}
	return strings.Join(names, ", ")
}

func funcName(f *pg.FuncCall) string {
	if len(f.Funcname) == 0 {
		return ""
	}
	if s := f.Funcname[len(f.Funcname)-1].GetString_(); s != nil {
		return strings.ToLower(s.Sval)
	}
	return ""
}

var sideEffectExact = map[string]bool{
	// run SQL given as a string: the classifier cannot see what executes
	// (ADR-027), e.g. query_to_xml('select set_config(''role'',...)')
	"query_to_xml": true, "query_to_xmlschema": true, "query_to_xml_and_xmlschema": true, "table_to_xml": true,
	"table_to_xmlschema": true, "table_to_xml_and_xmlschema": true, "schema_to_xml": true, "schema_to_xmlschema": true,
	"schema_to_xml_and_xmlschema": true, "database_to_xml": true, "database_to_xmlschema": true,
	"database_to_xml_and_xmlschema": true, "cursor_to_xml": true, "cursor_to_xmlschema": true, "ts_stat": true,
	"ts_rewrite": true,
	// server files and logs
	"pg_read_file": true, "pg_read_binary_file": true, "pg_stat_file": true, "pg_current_logfile": true,
	"pg_log_backend_memory_contexts": true,
	// replication slots, backups
	"pg_replication_slot_advance": true, "pg_backup_start": true, "pg_backup_stop": true, "pg_start_backup": true,
	"pg_stop_backup": true,
	// large objects: writes are not blocked by READ ONLY before PostgreSQL 14
	"lowrite": true, "lo_truncate": true, "lo_truncate64": true, "lo_open": true, "lo_close": true, "lo_lseek": true,
	"lo_lseek64": true, "loread": true,
	"pg_terminate_backend": true, "pg_cancel_backend": true, "pg_reload_conf": true, "pg_rotate_logfile": true,
	"pg_switch_wal": true, "pg_create_restore_point": true, "pg_promote": true, "pg_wal_replay_pause": true,
	"pg_wal_replay_resume": true, "pg_drop_replication_slot": true, "pg_logical_emit_message": true,
	"pg_file_write": true, "pg_file_rename": true, "pg_file_unlink": true, "lo_import": true, "lo_export": true,
	"lo_unlink": true, "lo_create": true, "lo_creat": true, "lo_from_bytea": true, "lo_put": true, "set_config": true,
	"pg_notify": true, "nextval": true, "setval": true, "pg_import_system_collations": true, "txid_current": true,
	"pg_current_xact_id": true,
}

func sideEffect(name string) bool {
	switch {
	case name == "":
		return false
	case sideEffectExact[name]:
		return true
	case strings.HasPrefix(name, "dblink"), strings.HasPrefix(name, "pg_replication_origin_"),
		strings.HasPrefix(name, "pg_stat_reset"), strings.HasPrefix(name, "pg_ls_"),
		strings.HasPrefix(name, "pg_logical_slot_"), strings.HasPrefix(name, "pg_copy_") && strings.HasSuffix(name, "_replication_slot"),
		strings.HasSuffix(name, "_reset") && strings.HasPrefix(name, "pg_stat"):
		return true
	case strings.HasPrefix(name, "pg_create_") && strings.HasSuffix(name, "_replication_slot"):
		return true
	case strings.HasPrefix(name, "pg_advisory_") && strings.Contains(name, "lock") && !strings.Contains(name, "unlock"):
		return true
	case strings.HasPrefix(name, "pg_try_advisory_") && !strings.Contains(name, "unlock"):
		return true
	}
	return false
}

// walk visits every message in the tree (pre-order).
func walk(m protoreflect.Message, fn func(protoreflect.Message)) {
	if !m.IsValid() {
		return
	}
	fn(m)
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsList() && fd.Message() != nil:
			l := v.List()
			for i := range l.Len() {
				walk(l.Get(i).Message(), fn)
			}
		case fd.IsMap():
			if fd.MapValue().Message() != nil {
				v.Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
					walk(mv.Message(), fn)
					return true
				})
			}
		case fd.Message() != nil:
			walk(v.Message(), fn)
		}
		return true
	})
}
