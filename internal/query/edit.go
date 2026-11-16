package query

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/Warewave-Technology/athens/internal/catalog"
	"github.com/Warewave-Technology/athens/internal/pgconn"
	"github.com/Warewave-Technology/athens/internal/sqlclass"
)

// EditSource names the table whose rows a result shows, so a writer can edit
// them through the grid write endpoints (ADR-030). It is only a hint for the
// UI: those endpoints check access, editability and conflicts themselves.
type EditSource struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
	// Columns maps each result column to its table column (null: an
	// expression, another table or a repeated column).
	Columns    []*string `json:"columns"`
	PrimaryKey []string  `json:"primaryKey"`
}

// editSource returns where the rows of a single-table SELECT can be edited,
// or nil: the columns must come from one table (not a view) whose whole
// primary key is in the result.
func editSource(ctx context.Context, tx pgx.Tx, cols []pgconn.Column) (*EditSource, error) {
	var oid uint32
	uses := map[uint16]int{}
	for _, c := range cols {
		if c.TableOID == 0 || c.TableColumn == 0 {
			continue
		}
		if oid != 0 && c.TableOID != oid {
			return nil, nil
		}
		oid = c.TableOID
		uses[c.TableColumn]++
	}
	if oid == 0 {
		return nil, nil
	}
	schema, name, kind, err := catalog.LookupOID(ctx, tx, oid)
	if errors.Is(err, catalog.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if (kind != catalog.KindTable && kind != catalog.KindPartitioned) || sqlclass.SensitiveRelation(schema, name) {
		return nil, nil
	}
	pk, err := catalog.PrimaryKey(ctx, tx, oid)
	if err != nil || pk == nil {
		return nil, err
	}
	tcols, err := catalog.Columns(ctx, tx, oid)
	if err != nil {
		return nil, err
	}
	byNum := map[int]string{}
	for _, c := range tcols {
		byNum[c.Position] = c.Name
	}
	src := &EditSource{Schema: schema, Table: name, Columns: make([]*string, len(cols)), PrimaryKey: pk.Columns}
	var found []string
	for i, c := range cols {
		n, ok := byNum[int(c.TableColumn)]
		if c.TableOID != oid || !ok || uses[c.TableColumn] != 1 {
			continue
		}
		src.Columns[i] = &n
		found = append(found, n)
	}
	for _, k := range pk.Columns {
		if !slices.Contains(found, k) {
			return nil, nil
		}
	}
	return src, nil
}
