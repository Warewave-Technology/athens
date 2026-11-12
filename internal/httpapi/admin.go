package httpapi

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Warewave-Technology/athens/internal/audit"
	"github.com/Warewave-Technology/athens/internal/discovery"
	"github.com/Warewave-Technology/athens/internal/httpx"
	"github.com/Warewave-Technology/athens/internal/reqctx"
	"github.com/Warewave-Technology/athens/internal/store"
)

const (
	// adminViewEvery throttles admin_view records: the admin pages poll.
	adminViewEvery = 5 * time.Minute
	// maxAuditExport caps one export; narrow the filters for more.
	maxAuditExport = 100_000
	// exportPage is how many records an export reads from the store at once.
	exportPage = 500
)

// requireAdmin returns 404 for non-admins (no enumeration, SPEC §2).
func (s *Server) requireAdmin(r *http.Request) error {
	u := reqctx.UserFrom(r.Context())
	if u == nil || !s.Authz.IsAdmin(u.Groups) {
		return httpx.ErrNotFound
	}
	return nil
}

// viewLog remembers when each user last viewed each admin page.
type viewLog struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (v *viewLog) due(key string, now time.Time) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.last == nil || len(v.last) > 10_000 {
		v.last = map[string]time.Time{}
	}
	if t, ok := v.last[key]; ok && now.Sub(t) < adminViewEvery {
		return false
	}
	v.last[key] = now
	return true
}

// auditAdminView records an admin page view (ADR-021). Repeated views of the
// same page with the same filters within adminViewEvery are not recorded.
func (s *Server) auditAdminView(r *http.Request, page string, filters map[string]string) {
	details := map[string]any{"page": page}
	if len(filters) > 0 {
		details["filters"] = filters
	}
	b, _ := json.Marshal(details)
	if !s.views.due(reqctx.UserFrom(r.Context()).ID+"|"+string(b), time.Now()) {
		return
	}
	s.Audit.Write(r.Context(), store.AuditRecord{Event: audit.EventAdminView, Result: audit.ResultOK, Details: string(b)})
}

type adminDiscoveryResponse struct {
	Targets    []discovery.Target          `json:"targets"`
	Namespaces []discovery.NamespaceStatus `json:"namespaces"`
	Synced     bool                        `json:"synced"`
}

func (s *Server) adminDiscovery(w http.ResponseWriter, r *http.Request) error {
	if err := s.requireAdmin(r); err != nil {
		return err
	}
	s.auditAdminView(r, "discovery", nil)
	httpx.WriteJSON(w, http.StatusOK, adminDiscoveryResponse{
		Targets: s.Registry.List(), Namespaces: s.Registry.Status(), Synced: s.Ready == nil || s.Ready(),
	})
	return nil
}

func (s *Server) adminPools(w http.ResponseWriter, r *http.Request) error {
	if err := s.requireAdmin(r); err != nil {
		return err
	}
	s.auditAdminView(r, "pools", nil)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"pools": s.Pools.Stats()})
	return nil
}

// auditFilter parses the shared filters of the audit list and export.
func auditFilter(r *http.Request) (store.AuditFilter, map[string]string, error) {
	q := r.URL.Query()
	f := store.AuditFilter{User: q.Get("user"), TargetID: q.Get("targetId"), Event: q.Get("event")}
	for name, dst := range map[string]*time.Time{"from": &f.From, "to": &f.To} {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return f, nil, httpx.BadRequest(name + " must be RFC3339")
			}
			*dst = t
		}
	}
	used := map[string]string{}
	for _, k := range []string{"user", "targetId", "event", "from", "to"} {
		if v := q.Get(k); v != "" {
			used[k] = v
		}
	}
	return f, used, nil
}

func (s *Server) adminAudit(w http.ResponseWriter, r *http.Request) error {
	if err := s.requireAdmin(r); err != nil {
		return err
	}
	f, used, err := auditFilter(r)
	if err != nil {
		return err
	}
	f.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	f.Before, _ = strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	recs, err := s.Store.ListAudit(r.Context(), f)
	if err != nil {
		return err
	}
	if f.Before == 0 { // first page only; "load more" is the same view
		s.auditAdminView(r, "audit", used)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": recs})
	return nil
}

var auditCSVHeader = []string{"id", "ts", "event", "result", "user_id", "email", "client_ip", "user_agent", "target_id", "database",
	"object", "statement_class", "destructive", "sql", "rows_affected", "duration_ms", "error_code", "details"}

func optInt(p *int64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatInt(*p, 10)
}

// adminAuditExport streams the records matching the audit filters, newest
// first, as CSV or JSON Lines. The export itself is audited.
func (s *Server) adminAuditExport(w http.ResponseWriter, r *http.Request) error {
	if err := s.requireAdmin(r); err != nil {
		return err
	}
	f, used, err := auditFilter(r)
	if err != nil {
		return err
	}
	format := r.URL.Query().Get("format")
	switch format {
	case "":
		format = "csv"
	case "csv", "jsonl":
	default:
		return httpx.BadRequest("format must be csv or jsonl")
	}
	// Stream page by page: memory stays bounded however large the log is.
	// The first page is read before any output, so a store error is still a
	// clean error response.
	total, err := s.Store.CountAudit(r.Context(), f)
	if err != nil {
		return err
	}
	f.Limit = exportPage
	page, err := s.Store.ListAudit(r.Context(), f)
	if err != nil {
		return err
	}
	name := "athens-audit-" + time.Now().UTC().Format("20060102-150405") + "." + format
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	capped := total > maxAuditExport
	if capped {
		w.Header().Set("X-Athens-Export-Capped", "true")
	}
	var emit func(store.AuditRecord) error
	var flush func()
	if format == "jsonl" {
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)
		emit, flush = func(rec store.AuditRecord) error { return enc.Encode(rec) }, func() {}
	} else {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		cw := csv.NewWriter(w)
		_ = cw.Write(auditCSVHeader)
		emit = func(rec store.AuditRecord) error {
			// every string cell may hold user input: defuse them all
			return cw.Write([]string{
				strconv.FormatInt(rec.ID, 10), rec.TS.UTC().Format(time.RFC3339Nano), csvSafe(rec.Event), csvSafe(rec.Result),
				csvSafe(rec.UserID), csvSafe(rec.Email), csvSafe(rec.ClientIP), csvSafe(rec.UserAgent), csvSafe(rec.TargetID),
				csvSafe(rec.Database), csvSafe(rec.Object), csvSafe(rec.StatementClass), strconv.FormatBool(rec.Destructive),
				csvSafe(rec.SQL), optInt(rec.RowsAffected), optInt(rec.DurationMs), csvSafe(rec.ErrorCode), csvSafe(rec.Details),
			})
		}
		flush = cw.Flush
	}

	n, result := 0, audit.ResultOK
stream:
	for {
		for _, rec := range page {
			if n == maxAuditExport {
				break stream
			}
			if err := emit(rec); err != nil {
				break stream // the client went away
			}
			n++
		}
		if len(page) < f.Limit {
			break
		}
		flush()
		f.Before = page[len(page)-1].ID
		if page, err = s.Store.ListAudit(r.Context(), f); err != nil {
			// headers are out: record the failure and abort the response so
			// the download visibly fails instead of looking complete
			s.Log.Error("audit export: store error mid-stream", "err", err)
			result = audit.ResultError
			break
		}
	}
	flush()
	if result == audit.ResultError {
		b, _ := json.Marshal(map[string]any{"format": format, "records": n, "failed": true})
		rows := int64(n)
		s.Audit.Write(r.Context(), store.AuditRecord{Event: audit.EventAuditExport, Result: result, RowsAffected: &rows, Details: string(b)})
		panic(http.ErrAbortHandler)
	}

	details := map[string]any{"format": format, "records": n, "capped": capped}
	if len(used) > 0 {
		details["filters"] = used
	}
	b, _ := json.Marshal(details)
	rows := int64(n)
	s.Audit.Write(r.Context(), store.AuditRecord{Event: audit.EventAuditExport, Result: result, RowsAffected: &rows, Details: string(b)})
	return nil
}

// csvSafe defuses spreadsheet formula injection: user-controlled cells that
// start with = + - @ (or tab/CR) get a leading apostrophe.
func csvSafe(v string) string {
	if v != "" && (v[0] == '=' || v[0] == '+' || v[0] == '-' || v[0] == '@' || v[0] == '\t' || v[0] == '\r') {
		return "'" + v
	}
	return v
}
