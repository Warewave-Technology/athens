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
	// AnnotationAccess mirrors authz.annotationAccess: whether access
	// annotations replace the config rules.
	AnnotationAccess bool `json:"annotationAccess"`
}

func (s *Server) adminDiscovery(w http.ResponseWriter, r *http.Request) error {
	if err := s.requireAdmin(r); err != nil {
		return err
	}
	s.auditAdminView(r, "discovery", nil)
	httpx.WriteJSON(w, http.StatusOK, adminDiscoveryResponse{
		Targets: s.Registry.List(), Namespaces: s.Registry.Status(), Synced: s.Ready == nil || s.Ready(),
		AnnotationAccess: s.Config.Authz.AnnotationAccess,
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
	// Read everything before writing so a store error is still a clean error
	// response.
	var recs []store.AuditRecord
	f.Limit = 1000
	for len(recs) < maxAuditExport {
		page, err := s.Store.ListAudit(r.Context(), f)
		if err != nil {
			return err
		}
		recs = append(recs, page...)
		if len(page) < f.Limit {
			break
		}
		f.Before = page[len(page)-1].ID
	}
	capped := len(recs) >= maxAuditExport
	if capped {
		recs = recs[:maxAuditExport]
	}

	details := map[string]any{"format": format, "records": len(recs), "capped": capped}
	if len(used) > 0 {
		details["filters"] = used
	}
	b, _ := json.Marshal(details)
	n := int64(len(recs))
	s.Audit.Write(r.Context(), store.AuditRecord{Event: audit.EventAuditExport, Result: audit.ResultOK, RowsAffected: &n, Details: string(b)})

	name := "athens-audit-" + time.Now().UTC().Format("20060102-150405") + "." + format
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	if capped {
		w.Header().Set("X-Athens-Export-Capped", "true")
	}
	if format == "jsonl" {
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)
		for _, rec := range recs {
			if err := enc.Encode(rec); err != nil {
				return nil //nolint:nilerr // the client went away mid-stream
			}
		}
		return nil
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	cw := csv.NewWriter(w)
	_ = cw.Write(auditCSVHeader)
	for _, rec := range recs {
		_ = cw.Write([]string{
			strconv.FormatInt(rec.ID, 10), rec.TS.UTC().Format(time.RFC3339Nano), rec.Event, rec.Result, rec.UserID, rec.Email,
			rec.ClientIP, csvSafe(rec.UserAgent), rec.TargetID, rec.Database, csvSafe(rec.Object), rec.StatementClass,
			strconv.FormatBool(rec.Destructive), csvSafe(rec.SQL), optInt(rec.RowsAffected), optInt(rec.DurationMs), rec.ErrorCode,
			csvSafe(rec.Details),
		})
	}
	cw.Flush()
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
