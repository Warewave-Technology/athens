package httpapi

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Warewave-Technology/athens/internal/audit"
	"github.com/Warewave-Technology/athens/internal/authz"
	"github.com/Warewave-Technology/athens/internal/httpx"
	"github.com/Warewave-Technology/athens/internal/reqctx"
	"github.com/Warewave-Technology/athens/internal/store"
)

const (
	// maxActivity caps the sessions one activity response lists.
	maxActivity = 200
	// maxActivityText caps each statement's text (characters).
	maxActivityText = 8192
)

// passwordText hides statements that may carry a password (ALTER ROLE ...
// PASSWORD, CREATE USER MAPPING ...): other sessions' text is shown as is.
var passwordText = regexp.MustCompile(`(?i)password`)

type activityItem struct {
	PID         uint32     `json:"pid"`
	User        string     `json:"user"`
	Application string     `json:"application"`
	Client      *string    `json:"client"`
	State       string     `json:"state"`
	Wait        *string    `json:"wait"`
	QueryStart  *time.Time `json:"queryStart"`
	XactStart   *time.Time `json:"xactStart"`
	Query       string     `json:"query"`
	// QueryID is set on the caller's own editor query: it can be cancelled.
	QueryID string `json:"queryId,omitempty"`
}

// Non-idle client sessions of the database; text the login may not see is
// "<insufficient privilege>" (pg_read_all_stats shows all of it).
const activitySQL = `SELECT pid, coalesce(usename, ''), coalesce(application_name, ''), host(client_addr),
		coalesce(state, ''), CASE WHEN wait_event IS NOT NULL THEN wait_event_type || ': ' || wait_event END,
		query_start, xact_start, left(coalesce(query, ''), $2), now()
	FROM pg_stat_activity
	WHERE datname = current_database() AND backend_type = 'client backend'
		AND pid <> pg_backend_pid() AND state IS DISTINCT FROM 'idle'
	ORDER BY query_start NULLS LAST LIMIT $1`

// activity lists the running queries of a database (ADR-031). Writers only.
func (s *Server) activity(w http.ResponseWriter, r *http.Request) error {
	t, db, lvl, err := s.targetDB(r)
	if err != nil {
		return err
	}
	if lvl != authz.Write {
		s.Audit.Write(r.Context(), store.AuditRecord{Event: audit.EventAccessDenied, Result: audit.ResultDenied,
			TargetID: t.ID, Database: db, Details: `{"operation":"activity"}`})
		return httpx.NewError(http.StatusForbidden, "forbidden", "running queries need write access to this database")
	}
	items := []activityItem{}
	var now time.Time
	err = s.Pools.Run(r.Context(), s.dbReq(r, t, db, lvl), func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, activitySQL, maxActivity, maxActivityText)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (activityItem, error) {
			var a activityItem
			return a, row.Scan(&a.PID, &a.User, &a.Application, &a.Client, &a.State, &a.Wait,
				&a.QueryStart, &a.XactStart, &a.Query, &now)
		})
		return err
	})
	if err != nil {
		return s.mapDBError(err, "")
	}
	u := reqctx.UserFrom(r.Context())
	editor := s.Query.Registry.OnDatabase(t.ID, db)
	for i := range items {
		// athens editor queries: the user's statement instead of its FETCH
		if q, ok := editor[items[i].PID]; ok {
			items[i].Query = q.SQL
			if rs := []rune(q.SQL); len(rs) > maxActivityText {
				items[i].Query = string(rs[:maxActivityText])
			}
			if q.UserID == u.ID {
				items[i].QueryID = q.ID
			}
		}
		if passwordText.MatchString(items[i].Query) {
			items[i].Query = "<hidden: the statement mentions a password>"
		}
	}
	if s.Config.Audit.LogReads && s.views.due(u.ID+"|activity|"+t.ID+"|"+db, time.Now()) {
		s.Audit.Write(r.Context(), store.AuditRecord{Event: audit.EventActivityView, Result: audit.ResultOK,
			TargetID: t.ID, Database: db})
	}
	if now.IsZero() {
		now = time.Now()
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"now": now, "items": items})
	return nil
}
