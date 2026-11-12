package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	pgxconn "github.com/jackc/pgx/v5/pgconn"

	"github.com/Warewave-Technology/athens/internal/audit"
	"github.com/Warewave-Technology/athens/internal/catalog"
	"github.com/Warewave-Technology/athens/internal/discovery"
	"github.com/Warewave-Technology/athens/internal/httpx"
	"github.com/Warewave-Technology/athens/internal/pgconn"
	"github.com/Warewave-Technology/athens/internal/rows"
	"github.com/Warewave-Technology/athens/internal/sqlclass"
	"github.com/Warewave-Technology/athens/internal/store"
)

const (
	exactCountTimeout = 5 * time.Second
	maxCellValue      = 16 << 20
)

var errInvalidFilter = func(msg string) error { return httpx.NewError(http.StatusBadRequest, "invalid_filter", msg) }

// loadTable reads the column list and primary key of schema.rel.
func loadTable(ctx context.Context, tx pgx.Tx, schema, name string) (rows.Table, uint32, catalog.Kind, error) {
	oid, kind, err := catalog.Lookup(ctx, tx, schema, name)
	if err != nil {
		return rows.Table{}, 0, "", err
	}
	cols, err := catalog.Columns(ctx, tx, oid)
	if err != nil {
		return rows.Table{}, 0, "", err
	}
	t := rows.Table{Schema: schema, Name: name, Columns: cols}
	pk, err := catalog.PrimaryKey(ctx, tx, oid)
	if err != nil {
		return t, 0, "", err
	}
	if pk != nil {
		t.PK = pk.Columns
	}
	return t, oid, kind, nil
}

type gridColumn struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	TypeOID uint32 `json:"typeOid"`
}

type gridResponse struct {
	Columns        []gridColumn `json:"columns"`
	Rows           [][]*string  `json:"rows"`
	TruncatedCells [][2]int     `json:"truncatedCells"`
	Total          *int64       `json:"total"`
	TotalExact     bool         `json:"totalExact"`
	CountTimedOut  bool         `json:"countTimedOut,omitempty"`
	PrimaryKey     []string     `json:"primaryKey"`
	DurationMs     int64        `json:"durationMs"`
}

// credentialsCatalog reports a grid request on pg_authid and the other
// catalogs athens never shows (ADR-026).
func credentialsCatalog(r *http.Request) bool {
	return sqlclass.SensitiveRelation(r.PathValue("schema"), r.PathValue("rel"))
}

func (s *Server) rowsQuery(w http.ResponseWriter, r *http.Request) error {
	if credentialsCatalog(r) {
		return httpx.ErrNotFound
	}
	t, db, lvl, err := s.targetDB(r)
	if err != nil {
		return err
	}
	var q rows.Query
	if err := httpx.DecodeJSON(r, &q); err != nil {
		return err
	}
	start := time.Now()
	var resp gridResponse
	err = s.Pools.Run(r.Context(), s.dataReq(r, t, db, lvl), func(ctx context.Context, tx pgx.Tx) error {
		tbl, oid, _, err := loadTable(ctx, tx, r.PathValue("schema"), r.PathValue("rel"))
		if err != nil {
			return err
		}
		st, err := rows.Select(tbl, q, s.Config.Limits.GridMaxPageSize)
		if err != nil {
			return err
		}
		res, err := pgconn.QueryText(ctx, tx, st.SQL, st.Params, pgconn.TextOptions{
			MaxRows: s.Config.Limits.GridMaxPageSize, MaxCellBytes: s.Config.Limits.MaxCellBytes, ParamOIDs: st.OIDs,
		})
		if err != nil {
			return err
		}
		resp.Rows, resp.TruncatedCells, resp.PrimaryKey = res.Rows, res.TruncatedCells, tbl.PK
		for _, c := range tbl.Columns {
			resp.Columns = append(resp.Columns, gridColumn{Name: c.Name, Type: c.Type, TypeOID: c.TypeOID})
		}
		limit := q.Limit
		if limit <= 0 {
			limit = 100
		}
		if q.Offset == 0 && len(res.Rows) < limit {
			n := int64(len(res.Rows)) // the whole result fits on the first page
			resp.Total, resp.TotalExact = &n, true
			return nil
		}
		if q.ExactCount {
			n, timedOut, err := exactCount(ctx, tx, tbl, q.Filters, s.Config.Limits.StatementTimeout.D())
			if err != nil {
				return err
			}
			if !timedOut {
				resp.Total, resp.TotalExact = &n, true
				return nil
			}
			resp.CountTimedOut = true
		}
		resp.Total, err = estimateCount(ctx, tx, tbl, oid, q.Filters)
		return err
	})
	if err != nil {
		var ie *rows.InvalidError
		if errors.As(err, &ie) {
			return errInvalidFilter(ie.Msg)
		}
		return s.mapDBError(err, "")
	}
	if resp.Rows == nil {
		resp.Rows = [][]*string{}
	}
	if resp.TruncatedCells == nil {
		resp.TruncatedCells = [][2]int{}
	}
	resp.DurationMs = time.Since(start).Milliseconds()
	s.auditRead(r, audit.EventRowsRead, t, db, int64(len(resp.Rows)), resp.DurationMs, s.readDetails(q))
	httpx.WriteJSON(w, http.StatusOK, resp)
	return nil
}

// auditRead records a successful read when audit.logReads is on. Negative n
// or durationMs mean not measured.
func (s *Server) auditRead(r *http.Request, event string, t discovery.Target, db string, n, durationMs int64, details map[string]any) {
	if !s.Config.Audit.LogReads {
		return
	}
	rec := store.AuditRecord{Event: event, TargetID: t.ID, Database: db, Object: r.PathValue("schema") + "." + r.PathValue("rel"),
		Result: audit.ResultOK}
	if n >= 0 {
		rec.RowsAffected = &n
	}
	if durationMs >= 0 {
		rec.DurationMs = &durationMs
	}
	if details != nil {
		b, _ := json.Marshal(details)
		rec.Details = string(b)
	}
	s.Audit.Write(r.Context(), rec)
}

// readDetails describes a grid page: paging, sort and filters. Filter values
// are only kept with audit.logValues.
func (s *Server) readDetails(q rows.Query) map[string]any {
	d := map[string]any{"offset": q.Offset, "limit": q.Limit}
	if len(q.Sort) > 0 {
		d["sort"] = q.Sort
	}
	if len(q.Filters) > 0 {
		fs := q.Filters
		if !s.Config.Audit.LogValues {
			fs = make([]rows.Filter, len(q.Filters))
			for i, f := range q.Filters {
				fs[i] = rows.Filter{Column: f.Column, Op: f.Op}
			}
		}
		d["filters"] = fs
	}
	return d
}

// exactCount runs count(*) under a 5s statement_timeout inside a savepoint so
// a timeout does not abort the surrounding transaction.
func exactCount(ctx context.Context, tx pgx.Tx, tbl rows.Table, filters []rows.Filter, restore time.Duration) (int64, bool, error) {
	st, err := rows.Count(tbl, filters)
	if err != nil {
		return 0, false, err
	}
	if _, err := tx.Exec(ctx, "SAVEPOINT athens_count"); err != nil {
		return 0, false, err
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('statement_timeout', $1, true)", ms(exactCountTimeout)); err != nil {
		return 0, false, err
	}
	res, err := pgconn.QueryText(ctx, tx, st.SQL, st.Params, pgconn.TextOptions{MaxRows: 1, ParamOIDs: st.OIDs})
	var pe *pgxconn.PgError
	if errors.As(err, &pe) && pe.Code == "57014" && ctx.Err() == nil {
		_, rerr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT athens_count")
		return 0, true, rerr
	}
	if err != nil {
		return 0, false, err
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('statement_timeout', $1, true)", ms(restore)); err != nil {
		return 0, false, err
	}
	if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT athens_count"); err != nil {
		return 0, false, err
	}
	var n int64
	if len(res.Rows) == 1 && res.Rows[0][0] != nil {
		_ = json.Unmarshal([]byte(*res.Rows[0][0]), &n)
	}
	return n, false, nil
}

func ms(d time.Duration) string {
	b, _ := json.Marshal(d.Milliseconds())
	return string(b)
}

// estimateCount uses reltuples without filters and the planner's row
// estimate with filters.
func estimateCount(ctx context.Context, tx pgx.Tx, tbl rows.Table, oid uint32, filters []rows.Filter) (*int64, error) {
	if len(filters) == 0 {
		var n *int64
		err := tx.QueryRow(ctx, `SELECT CASE WHEN reltuples >= 0 THEN reltuples::int8 END FROM pg_class WHERE oid = $1`, oid).Scan(&n)
		return n, err
	}
	st, err := rows.Count(tbl, filters)
	if err != nil {
		return nil, err
	}
	res, err := pgconn.QueryText(ctx, tx, "EXPLAIN (FORMAT JSON) "+st.SQL, st.Params, pgconn.TextOptions{MaxRows: 1, ParamOIDs: st.OIDs})
	if err != nil || len(res.Rows) == 0 || res.Rows[0][0] == nil {
		return nil, err
	}
	var plan []struct {
		Plan struct {
			Plans []struct {
				Rows float64 `json:"Plan Rows"`
			} `json:"Plans"`
			Rows float64 `json:"Plan Rows"`
		} `json:"Plan"`
	}
	if err := json.Unmarshal([]byte(*res.Rows[0][0]), &plan); err != nil || len(plan) == 0 {
		return nil, nil //nolint:nilerr // an unreadable plan just means "no estimate"
	}
	// count(*) is an Aggregate on top of the scan: use the scan's estimate.
	est := plan[0].Plan.Rows
	if len(plan[0].Plan.Plans) > 0 {
		est = plan[0].Plan.Plans[0].Rows
	}
	n := int64(est)
	return &n, nil
}

// decodePK parses the base64url(JSON) primary key object of the cell endpoint.
func decodePK(s string) (map[string]*string, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		if b, err = base64.URLEncoding.DecodeString(s); err != nil {
			return nil, httpx.BadRequest("pk must be base64url encoded JSON")
		}
	}
	var pk map[string]*string
	if err := json.Unmarshal(b, &pk); err != nil {
		return nil, httpx.BadRequest("pk must be a JSON object of strings")
	}
	return pk, nil
}

func (s *Server) rowsCell(w http.ResponseWriter, r *http.Request) error {
	if credentialsCatalog(r) {
		return httpx.ErrNotFound
	}
	t, db, lvl, err := s.targetDB(r)
	if err != nil {
		return err
	}
	pk, err := decodePK(r.URL.Query().Get("pk"))
	if err != nil {
		return err
	}
	var value *string
	var truncated bool
	err = s.Pools.Run(r.Context(), s.dataReq(r, t, db, lvl), func(ctx context.Context, tx pgx.Tx) error {
		tbl, _, _, err := loadTable(ctx, tx, r.PathValue("schema"), r.PathValue("rel"))
		if err != nil {
			return err
		}
		st, err := rows.Cell(tbl, r.URL.Query().Get("column"), pk)
		if err != nil {
			return err
		}
		res, err := pgconn.QueryText(ctx, tx, st.SQL, st.Params, pgconn.TextOptions{MaxRows: 1, MaxCellBytes: maxCellValue, ParamOIDs: st.OIDs})
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return catalog.ErrNotFound
		}
		value, truncated = res.Rows[0][0], len(res.TruncatedCells) > 0
		return nil
	})
	if err != nil {
		var ie *rows.InvalidError
		if errors.As(err, &ie) {
			return httpx.BadRequest(ie.Msg)
		}
		return s.mapDBError(err, "")
	}
	s.auditRead(r, audit.EventCellRead, t, db, 1, -1, map[string]any{"column": r.URL.Query().Get("column"), "pk": pk})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"value": value, "truncated": truncated})
	return nil
}
