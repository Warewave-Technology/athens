package pgconn

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Column describes one result column.
type Column struct {
	Name    string `json:"name"`
	TypeOID uint32 `json:"typeOid"`
	Type    string `json:"type"`
}

// TextResult holds rows fetched in Postgres TEXT format (ADR-004). A nil
// cell is SQL NULL.
type TextResult struct {
	Columns        []Column
	Rows           [][]*string
	TruncatedCells [][2]int
	Truncated      bool // more rows than MaxRows existed
	CommandTag     string
	RowsAffected   int64
	typmods        []int32
}

type TextOptions struct {
	MaxRows      int
	MaxCellBytes int
	// Cursor reads a row-returning read statement through a cursor so that
	// at most MaxRows+1 rows are ever fetched from the server.
	Cursor bool
	// ParamOIDs are the parameter types (0 = let the server infer). Values
	// are always sent as text and parsed by the server as that type.
	ParamOIDs []uint32
}

// QueryText runs sql with text parameters (nil = NULL) and returns every
// column as text.
func QueryText(ctx context.Context, tx pgx.Tx, sql string, params []*string, o TextOptions) (*TextResult, error) {
	if o.Cursor {
		return cursorText(ctx, tx, sql, params, o)
	}
	res, err := execText(ctx, tx, sql, params, o, o.MaxRows)
	if err != nil {
		return nil, err
	}
	return res, typeNames(ctx, tx, res)
}

func encodeParams(params []*string) [][]byte {
	out := make([][]byte, len(params))
	for i, p := range params {
		if p != nil {
			out[i] = []byte(*p)
		}
	}
	return out
}

func execText(ctx context.Context, tx pgx.Tx, sql string, params []*string, o TextOptions, keep int) (*TextResult, error) {
	rr := tx.Conn().PgConn().ExecParams(ctx, sql, encodeParams(params), o.ParamOIDs, nil, []int16{0})
	res := &TextResult{}
	// Field descriptions are only valid until Close; copy them first.
	for _, fd := range rr.FieldDescriptions() {
		res.Columns = append(res.Columns, Column{Name: fd.Name, TypeOID: fd.DataTypeOID})
		res.typmods = append(res.typmods, fd.TypeModifier)
	}
	for rr.NextRow() {
		if keep >= 0 && len(res.Rows) >= keep {
			res.Truncated = true
			continue // drain: the statement may be DML whose effect must complete
		}
		res.Rows = append(res.Rows, textRow(rr.Values(), len(res.Rows), o.MaxCellBytes, &res.TruncatedCells))
	}
	tag, err := rr.Close()
	if err != nil {
		return nil, err
	}
	res.CommandTag, res.RowsAffected = tag.String(), tag.RowsAffected()
	return res, nil
}

const cursorName = "athens_result"

func cursorText(ctx context.Context, tx pgx.Tx, sql string, params []*string, o TextOptions) (*TextResult, error) {
	if _, err := tx.Conn().PgConn().ExecParams(ctx, "DECLARE "+cursorName+" NO SCROLL CURSOR FOR "+sql,
		encodeParams(params), o.ParamOIDs, nil, nil).Close(); err != nil {
		return nil, err
	}
	fetch := o
	fetch.ParamOIDs = nil
	res, err := execText(ctx, tx, fmt.Sprintf("FETCH FORWARD %d FROM %s", o.MaxRows+1, cursorName), nil, fetch, o.MaxRows)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "CLOSE "+cursorName); err != nil {
		return nil, err
	}
	res.RowsAffected = int64(len(res.Rows))
	res.CommandTag = fmt.Sprintf("SELECT %d", len(res.Rows))
	if res.Truncated {
		res.CommandTag += "+"
	}
	return res, typeNames(ctx, tx, res)
}

func textRow(vals [][]byte, row, maxCell int, truncated *[][2]int) []*string {
	out := make([]*string, len(vals))
	for i, v := range vals {
		if v == nil {
			continue
		}
		if maxCell > 0 && len(v) > maxCell {
			v = cutUTF8(v, maxCell)
			*truncated = append(*truncated, [2]int{row, i})
		}
		s := string(v)
		out[i] = &s
	}
	return out
}

func cutUTF8(b []byte, n int) []byte {
	for n > 0 && n < len(b) && b[n]&0xC0 == 0x80 {
		n--
	}
	return b[:n]
}

// typeNames fills Column.Type with format_type(oid, typmod).
func typeNames(ctx context.Context, tx pgx.Tx, res *TextResult) error {
	if len(res.Columns) == 0 {
		return nil
	}
	oids := make([]uint32, len(res.Columns))
	mods := res.typmods
	if len(mods) != len(oids) {
		mods = make([]int32, len(oids))
		for i := range mods {
			mods[i] = -1
		}
	}
	for i, c := range res.Columns {
		oids[i] = c.TypeOID
	}
	rows, err := tx.Query(ctx, `SELECT format_type(o, NULLIF(m, -1)) FROM unnest($1::oid[], $2::int4[]) WITH ORDINALITY AS u(o, m, n) ORDER BY n`, oids, mods)
	if err != nil {
		return err
	}
	i := 0
	for rows.Next() {
		var name *string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		if name != nil && i < len(res.Columns) {
			res.Columns[i].Type = *name
		}
		i++
	}
	rows.Close()
	return rows.Err()
}
