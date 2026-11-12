// Package rows builds the data grid SQL (SPEC §8). Identifiers go through
// pgx.Identifier.Sanitize; values are bind parameters sent as text with the
// column's type OID, so the server parses them with the column's input
// function (ADR-018). Column names are validated against the catalog.
package rows

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Warewave-Technology/athens/internal/catalog"
)

const textOID = 25

type Sort struct {
	Column string `json:"column"`
	Dir    string `json:"dir"` // asc | desc
}

type Filter struct {
	Column string `json:"column"`
	Op     string `json:"op"`
	Value  any    `json:"value,omitempty"`
}

type Query struct {
	Limit      int      `json:"limit"`
	Offset     int      `json:"offset"`
	Sort       []Sort   `json:"sort"`
	Filters    []Filter `json:"filters"`
	ExactCount bool     `json:"exactCount"`
}

// InvalidError is a client error (400 invalid_filter).
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return e.Msg }

func invalid(format string, a ...any) error { return &InvalidError{Msg: fmt.Sprintf(format, a...)} }

// Statement is generated SQL plus its text parameters and their type OIDs.
type Statement struct {
	SQL    string
	Params []*string
	OIDs   []uint32
}

func (s *Statement) bind(v *string, oid uint32) string {
	s.Params = append(s.Params, v)
	s.OIDs = append(s.OIDs, oid)
	return "$" + strconv.Itoa(len(s.Params))
}

type Table struct {
	Schema  string
	Name    string
	Columns []catalog.ColumnInfo
	PK      []string // primary key columns, if any
}

func (t Table) ident() string { return pgx.Identifier{t.Schema, t.Name}.Sanitize() }

func (t Table) column(name string) (catalog.ColumnInfo, bool) {
	for _, c := range t.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return catalog.ColumnInfo{}, false
}

func col(name string) string { return pgx.Identifier{name}.Sanitize() }

// Where builds the WHERE clause (without the keyword, "" when no filters).
func Where(t Table, filters []Filter, st *Statement) (string, error) {
	var parts []string
	for _, f := range filters {
		c, ok := t.column(f.Column)
		if !ok {
			return "", invalid("unknown column %q", f.Column)
		}
		id := col(c.Name)
		switch f.Op {
		case "is_null":
			parts = append(parts, id+" IS NULL")
			continue
		case "not_null":
			parts = append(parts, id+" IS NOT NULL")
			continue
		case "in":
			vals, ok := f.Value.([]any)
			if !ok || len(vals) == 0 || len(vals) > 1000 {
				return "", invalid("filter %q: in needs a non-empty array (max 1000)", f.Column)
			}
			strs := make([]string, len(vals))
			for i, v := range vals {
				s, ok := v.(string)
				if !ok {
					return "", invalid("filter %q: in values must be strings", f.Column)
				}
				strs[i] = s
			}
			if c.ArrayOID == 0 {
				return "", invalid("filter %q: in is not supported for type %s", f.Column, c.Type)
			}
			lit := ArrayLiteral(strs)
			parts = append(parts, id+" = ANY("+st.bind(&lit, c.ArrayOID)+")")
			continue
		}
		v, ok := f.Value.(string)
		if !ok {
			return "", invalid("filter %q: value must be a string", f.Column)
		}
		switch f.Op {
		case "eq", "neq", "lt", "lte", "gt", "gte":
			op := map[string]string{"eq": "=", "neq": "<>", "lt": "<", "lte": "<=", "gt": ">", "gte": ">="}[f.Op]
			parts = append(parts, id+" "+op+" "+st.bind(&v, c.TypeOID))
		case "like", "ilike":
			parts = append(parts, id+"::text "+strings.ToUpper(f.Op)+" "+st.bind(&v, textOID))
		default:
			return "", invalid("unknown filter op %q", f.Op)
		}
	}
	return strings.Join(parts, " AND "), nil
}

// Select builds the page query.
func Select(t Table, q Query, maxPage int) (*Statement, error) {
	if q.Limit <= 0 {
		q.Limit = 100
	}
	if q.Limit > maxPage {
		return nil, invalid("limit must be <= %d", maxPage)
	}
	if q.Offset < 0 {
		return nil, invalid("offset must be >= 0")
	}
	st := &Statement{}
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = col(c.Name)
	}
	var b strings.Builder
	b.WriteString("SELECT " + strings.Join(cols, ", ") + " FROM " + t.ident())
	where, err := Where(t, q.Filters, st)
	if err != nil {
		return nil, err
	}
	if where != "" {
		b.WriteString(" WHERE " + where)
	}
	var order []string
	for _, s := range q.Sort {
		c, ok := t.column(s.Column)
		if !ok {
			return nil, invalid("unknown sort column %q", s.Column)
		}
		switch strings.ToLower(s.Dir) {
		case "", "asc":
			order = append(order, col(c.Name)+" ASC")
		case "desc":
			order = append(order, col(c.Name)+" DESC")
		default:
			return nil, invalid("sort direction must be asc or desc")
		}
	}
	// A stable order makes paging deterministic: append the primary key.
	for _, pk := range t.PK {
		if !containsCol(q.Sort, pk) {
			order = append(order, col(pk)+" ASC")
		}
	}
	if len(order) > 0 {
		b.WriteString(" ORDER BY " + strings.Join(order, ", "))
	}
	limit, offset := strconv.Itoa(q.Limit), strconv.Itoa(q.Offset)
	b.WriteString(" LIMIT " + st.bind(&limit, 20) + " OFFSET " + st.bind(&offset, 20))
	st.SQL = b.String()
	return st, nil
}

func containsCol(s []Sort, c string) bool {
	for _, x := range s {
		if x.Column == c {
			return true
		}
	}
	return false
}

// Count builds "SELECT count(*)" for the filtered table.
func Count(t Table, filters []Filter) (*Statement, error) {
	st := &Statement{}
	where, err := Where(t, filters, st)
	if err != nil {
		return nil, err
	}
	st.SQL = "SELECT count(*) FROM " + t.ident()
	if where != "" {
		st.SQL += " WHERE " + where
	}
	return st, nil
}

// PKWhere builds "pk1 = $n AND ..." for a primary key value object.
func PKWhere(t Table, pk map[string]*string, st *Statement) (string, error) {
	if len(t.PK) == 0 {
		return "", invalid("relation has no primary key")
	}
	if len(pk) != len(t.PK) {
		return "", invalid("primary key must have exactly the columns %s", strings.Join(t.PK, ", "))
	}
	parts := make([]string, len(t.PK))
	for i, name := range t.PK {
		v, ok := pk[name]
		if !ok || v == nil {
			return "", invalid("primary key column %q missing", name)
		}
		c, _ := t.column(name)
		parts[i] = col(name) + " = " + st.bind(v, c.TypeOID)
	}
	return strings.Join(parts, " AND "), nil
}

// Cell builds the query for one full cell value.
func Cell(t Table, column string, pk map[string]*string) (*Statement, error) {
	c, ok := t.column(column)
	if !ok {
		return nil, invalid("unknown column %q", column)
	}
	st := &Statement{}
	where, err := PKWhere(t, pk, st)
	if err != nil {
		return nil, err
	}
	st.SQL = "SELECT " + col(c.Name) + " FROM " + t.ident() + " WHERE " + where
	return st, nil
}

// ArrayLiteral renders a Postgres array literal of quoted text elements.
func ArrayLiteral(vals []string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, v := range vals {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}
