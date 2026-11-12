// Package catalog reads schema information from pg_catalog only (SPEC §9).
package catalog

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

var ErrNotFound = errors.New("relation not found")

type Database struct {
	Name      string `json:"name"`
	SizeBytes *int64 `json:"sizeBytes"`
}

// Databases lists connectable, non-template databases.
func Databases(ctx context.Context, tx pgx.Tx) ([]Database, error) {
	rows, err := tx.Query(ctx, `SELECT datname,
		CASE WHEN has_database_privilege(datname, 'CONNECT') THEN pg_database_size(datname) END
		FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY datname`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Database, error) {
		var d Database
		return d, r.Scan(&d.Name, &d.SizeBytes)
	})
}

type Schema struct {
	Name    string  `json:"name"`
	Owner   string  `json:"owner"`
	Comment *string `json:"comment"`
}

const systemSchemaFilter = `(n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\_toast%' AND n.nspname NOT LIKE 'pg\_temp\_%')`

func Schemas(ctx context.Context, tx pgx.Tx, system bool) ([]Schema, error) {
	rows, err := tx.Query(ctx, `SELECT n.nspname, pg_get_userbyid(n.nspowner), obj_description(n.oid, 'pg_namespace')
		FROM pg_namespace n WHERE $1 OR `+systemSchemaFilter+` ORDER BY n.nspname`, system)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Schema, error) {
		var s Schema
		return s, r.Scan(&s.Name, &s.Owner, &s.Comment)
	})
}

type Kind string

const (
	KindTable       Kind = "table"
	KindPartitioned Kind = "partitioned"
	KindView        Kind = "view"
	KindMatview     Kind = "matview"
	KindForeign     Kind = "foreign"
	KindSequence    Kind = "sequence"
)

func kindOf(relkind string) Kind {
	switch relkind {
	case "r":
		return KindTable
	case "p":
		return KindPartitioned
	case "v":
		return KindView
	case "m":
		return KindMatview
	case "f":
		return KindForeign
	case "S":
		return KindSequence
	}
	return Kind(relkind)
}

type RelRef struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
}

type Relation struct {
	Schema      string  `json:"schema"`
	Name        string  `json:"name"`
	Kind        Kind    `json:"kind"`
	RowEstimate *int64  `json:"rowEstimate"`
	SizeBytes   *int64  `json:"sizeBytes"`
	Comment     *string `json:"comment"`
	Parent      *RelRef `json:"parent"`
	HasPK       bool    `json:"-"`
}

// Relations lists relations of all schemas, capped at limit (capped=true
// when more exist).
func Relations(ctx context.Context, tx pgx.Tx, system bool, limit int) ([]Relation, bool, error) {
	rows, err := tx.Query(ctx, `SELECT n.nspname, c.relname, c.relkind::text,
			CASE WHEN c.reltuples >= 0 AND c.relkind IN ('r','p','m','f') THEN c.reltuples::int8 END,
			CASE WHEN c.relkind IN ('r','m','t') THEN pg_total_relation_size(c.oid)
			     WHEN c.relkind = 'p' THEN (SELECT sum(pg_total_relation_size(i.inhrelid))::int8 FROM pg_inherits i WHERE i.inhparent = c.oid) END,
			obj_description(c.oid, 'pg_class'),
			pn.nspname, pc.relname,
			EXISTS (SELECT 1 FROM pg_constraint k WHERE k.conrelid = c.oid AND k.contype = 'p')
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_inherits inh ON c.relispartition AND inh.inhrelid = c.oid
		LEFT JOIN pg_class pc ON pc.oid = inh.inhparent
		LEFT JOIN pg_namespace pn ON pn.oid = pc.relnamespace
		WHERE c.relkind IN ('r','p','v','m','f','S') AND ($1 OR `+systemSchemaFilter+`)
		ORDER BY n.nspname, c.relname
		LIMIT $2`, system, limit+1)
	if err != nil {
		return nil, false, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Relation, error) {
		var rel Relation
		var kind string
		var ps, pn *string
		err := r.Scan(&rel.Schema, &rel.Name, &kind, &rel.RowEstimate, &rel.SizeBytes, &rel.Comment, &ps, &pn, &rel.HasPK)
		rel.Kind = kindOf(kind)
		if ps != nil && pn != nil {
			rel.Parent = &RelRef{Schema: *ps, Name: *pn}
		}
		return rel, err
	})
	if err != nil {
		return nil, false, err
	}
	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, false, nil
}

// Lookup resolves schema.name to its oid and kind.
func Lookup(ctx context.Context, tx pgx.Tx, schema, name string) (uint32, Kind, error) {
	var oid uint32
	var kind string
	err := tx.QueryRow(ctx, `SELECT c.oid, c.relkind::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r','p','v','m','f','S')`, schema, name).Scan(&oid, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ErrNotFound
	}
	return oid, kindOf(kind), err
}
