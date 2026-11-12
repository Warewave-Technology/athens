package catalog

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type ColumnInfo struct {
	Name      string  `json:"name"`
	Type      string  `json:"type"`
	TypeOID   uint32  `json:"typeOid"`
	NotNull   bool    `json:"notNull"`
	Default   *string `json:"default"`
	Identity  *string `json:"identity"`  // always | by_default
	Generated *string `json:"generated"` // stored
	Comment   *string `json:"comment"`
	Position  int     `json:"position"`
}

type Key struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
}

type ForeignKey struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	RefSchema  string   `json:"refSchema"`
	RefTable   string   `json:"refTable"`
	RefColumns []string `json:"refColumns"`
	OnUpdate   string   `json:"onUpdate"`
	OnDelete   string   `json:"onDelete"`
	Direction  string   `json:"direction"` // out | in
}

type Check struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
}

type Index struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
	Unique     bool   `json:"unique"`
	Primary    bool   `json:"primary"`
	Valid      bool   `json:"valid"`
	SizeBytes  int64  `json:"sizeBytes"`
}

type Trigger struct {
	Name       string `json:"name"`
	Enabled    string `json:"enabled"` // origin | disabled | replica | always
	Definition string `json:"definition"`
}

type Structure struct {
	Schema      string       `json:"schema"`
	Name        string       `json:"name"`
	Kind        Kind         `json:"kind"`
	Columns     []ColumnInfo `json:"columns"`
	PrimaryKey  *Key         `json:"primaryKey"`
	ForeignKeys []ForeignKey `json:"foreignKeys"`
	Uniques     []Key        `json:"uniques"`
	Checks      []Check      `json:"checks"`
	Indexes     []Index      `json:"indexes"`
	Triggers    []Trigger    `json:"triggers"`
}

// Columns returns the live columns of a relation in ordinal order.
func Columns(ctx context.Context, tx pgx.Tx, oid uint32) ([]ColumnInfo, error) {
	rows, err := tx.Query(ctx, `SELECT a.attname, format_type(a.atttypid, a.atttypmod), a.atttypid, a.attnotnull,
			pg_get_expr(d.adbin, d.adrelid),
			CASE a.attidentity WHEN 'a' THEN 'always' WHEN 'd' THEN 'by_default' END,
			CASE a.attgenerated WHEN 's' THEN 'stored' WHEN 'v' THEN 'virtual' END,
			col_description(a.attrelid, a.attnum), a.attnum
		FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum`, oid)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ColumnInfo, error) {
		var c ColumnInfo
		return c, r.Scan(&c.Name, &c.Type, &c.TypeOID, &c.NotNull, &c.Default, &c.Identity, &c.Generated, &c.Comment, &c.Position)
	})
}

// PrimaryKey returns the primary key of a relation or nil.
func PrimaryKey(ctx context.Context, tx pgx.Tx, oid uint32) (*Key, error) {
	rows, err := tx.Query(ctx, `SELECT con.conname, ARRAY(SELECT a.attname FROM unnest(con.conkey) WITH ORDINALITY k(n, o)
			JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.n ORDER BY k.o)
		FROM pg_constraint con WHERE con.conrelid = $1 AND con.contype = 'p'`, oid)
	if err != nil {
		return nil, err
	}
	keys, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Key, error) {
		var k Key
		return k, r.Scan(&k.Name, &k.Columns)
	})
	if err != nil || len(keys) == 0 {
		return nil, err
	}
	return &keys[0], nil
}

var fkAction = map[string]string{"a": "no action", "r": "restrict", "c": "cascade", "n": "set null", "d": "set default"}
var triggerEnabled = map[string]string{"O": "origin", "D": "disabled", "R": "replica", "A": "always"}

// GetStructure returns columns, keys, constraints, indexes and triggers.
func GetStructure(ctx context.Context, tx pgx.Tx, schema, name string) (*Structure, error) {
	oid, kind, err := Lookup(ctx, tx, schema, name)
	if err != nil {
		return nil, err
	}
	s := &Structure{Schema: schema, Name: name, Kind: kind, ForeignKeys: []ForeignKey{}, Uniques: []Key{}, Checks: []Check{},
		Indexes: []Index{}, Triggers: []Trigger{}}
	if s.Columns, err = Columns(ctx, tx, oid); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, `SELECT con.conname, con.contype::text, pg_get_constraintdef(con.oid), con.conrelid = $1,
			ARRAY(SELECT a.attname FROM unnest(con.conkey) WITH ORDINALITY k(n, o)
				JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.n ORDER BY k.o),
			ARRAY(SELECT a.attname FROM unnest(con.confkey) WITH ORDINALITY k(n, o)
				JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.n ORDER BY k.o),
			coalesce(fn.nspname, ''), coalesce(fc.relname, ''), sn.nspname, sc.relname,
			coalesce(con.confupdtype::text, ''), coalesce(con.confdeltype::text, '')
		FROM pg_constraint con
		JOIN pg_class sc ON sc.oid = con.conrelid JOIN pg_namespace sn ON sn.oid = sc.relnamespace
		LEFT JOIN pg_class fc ON fc.oid = con.confrelid LEFT JOIN pg_namespace fn ON fn.oid = fc.relnamespace
		WHERE con.conrelid = $1 OR (con.confrelid = $1 AND con.contype = 'f')
		ORDER BY con.conname`, oid)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var cname, ctype, def, srcSchema, srcTable, upd, del, refSchema, refTable string
		var own bool
		var cols, refCols []string
		if err := rows.Scan(&cname, &ctype, &def, &own, &cols, &refCols, &refSchema, &refTable, &srcSchema, &srcTable, &upd, &del); err != nil {
			rows.Close()
			return nil, err
		}
		switch {
		case ctype == "p" && own:
			s.PrimaryKey = &Key{Name: cname, Columns: cols}
		case ctype == "u" && own:
			s.Uniques = append(s.Uniques, Key{Name: cname, Columns: cols})
		case ctype == "c" && own:
			s.Checks = append(s.Checks, Check{Name: cname, Definition: def})
		case ctype == "f" && own:
			s.ForeignKeys = append(s.ForeignKeys, ForeignKey{Name: cname, Columns: cols, RefSchema: refSchema, RefTable: refTable,
				RefColumns: refCols, OnUpdate: fkAction[upd], OnDelete: fkAction[del], Direction: "out"})
		case ctype == "f":
			// Incoming: our columns are the referenced ones; "ref" is the referencing table.
			s.ForeignKeys = append(s.ForeignKeys, ForeignKey{Name: cname, Columns: refCols, RefSchema: srcSchema, RefTable: srcTable,
				RefColumns: cols, OnUpdate: fkAction[upd], OnDelete: fkAction[del], Direction: "in"})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = tx.Query(ctx, `SELECT ic.relname, pg_get_indexdef(i.indexrelid), i.indisunique, i.indisprimary, i.indisvalid,
			pg_relation_size(i.indexrelid)
		FROM pg_index i JOIN pg_class ic ON ic.oid = i.indexrelid WHERE i.indrelid = $1 ORDER BY ic.relname`, oid)
	if err != nil {
		return nil, err
	}
	if s.Indexes, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Index, error) {
		var x Index
		return x, r.Scan(&x.Name, &x.Definition, &x.Unique, &x.Primary, &x.Valid, &x.SizeBytes)
	}); err != nil {
		return nil, err
	}

	rows, err = tx.Query(ctx, `SELECT t.tgname, t.tgenabled::text, pg_get_triggerdef(t.oid)
		FROM pg_trigger t WHERE t.tgrelid = $1 AND NOT t.tgisinternal ORDER BY t.tgname`, oid)
	if err != nil {
		return nil, err
	}
	if s.Triggers, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Trigger, error) {
		var x Trigger
		var en string
		err := r.Scan(&x.Name, &en, &x.Definition)
		x.Enabled = triggerEnabled[en]
		return x, err
	}); err != nil {
		return nil, err
	}
	return s, nil
}
