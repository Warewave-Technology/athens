package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// timeFormat is fixed width so text comparison in SQL orders correctly.
const timeFormat = "2006-01-02T15:04:05.000000Z"

func ts(t time.Time) string { return t.UTC().Format(timeFormat) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(timeFormat, s)
	return t
}

type SQLite struct {
	db *sql.DB
}

var _ Store = (*SQLite)(nil)

// OpenSQLite opens (creating if needed) the database at path and migrates it.
func OpenSQLite(ctx context.Context, path string) (*SQLite, error) {
	q := url.Values{}
	for _, p := range []string{"journal_mode(WAL)", "foreign_keys(1)", "busy_timeout(5000)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	s := &SQLite{db: db}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLite) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	entries, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(entries)
	for _, name := range entries {
		base := strings.TrimPrefix(name, "migrations/")
		v, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %s: bad version prefix", base)
		}
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version = ?`, v).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		body, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %s: %w", base, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, v, ts(time.Now())); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLite) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *SQLite) Close() error                   { return s.db.Close() }

func (s *SQLite) UpsertUser(ctx context.Context, u User) error {
	now := ts(u.LastLoginAt)
	_, err := s.db.ExecContext(ctx, `INSERT INTO users(id, email, name, created_at, last_login_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET email = excluded.email, name = excluded.name, last_login_at = excluded.last_login_at`,
		u.ID, u.Email, u.Name, now, now)
	return err
}

func (s *SQLite) CreateSession(ctx context.Context, x Session) error {
	groups, err := json.Marshal(nonNil(x.Groups))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO sessions(id, user_id, groups_json, csrf_token, created_at, last_seen_at, expires_at, client_ip, user_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		x.ID, x.UserID, string(groups), x.CSRFToken, ts(x.CreatedAt), ts(x.LastSeenAt), ts(x.ExpiresAt), x.ClientIP, x.UserAgent)
	return err
}

func (s *SQLite) GetSession(ctx context.Context, id string) (*Session, error) {
	var x Session
	var groups, created, seen, expires string
	err := s.db.QueryRowContext(ctx, `SELECT s.id, s.user_id, u.email, u.name, s.groups_json, s.csrf_token, s.created_at, s.last_seen_at, s.expires_at, s.client_ip, s.user_agent
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id = ?`, id).
		Scan(&x.ID, &x.UserID, &x.Email, &x.Name, &groups, &x.CSRFToken, &created, &seen, &expires, &x.ClientIP, &x.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(groups), &x.Groups); err != nil {
		return nil, fmt.Errorf("session groups: %w", err)
	}
	x.CreatedAt, x.LastSeenAt, x.ExpiresAt = parseTS(created), parseTS(seen), parseTS(expires)
	return &x, nil
}

func (s *SQLite) TouchSession(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id = ?`, ts(at), id)
	return err
}

func (s *SQLite) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

func (s *SQLite) PurgeSessions(ctx context.Context, now time.Time, idleTTL time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ? OR last_seen_at <= ?`, ts(now), ts(now.Add(-idleTTL)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// historyBytes caps the SQL text kept in one user's history (ADR-027).
const historyBytes = 16 << 20

func (s *SQLite) AddHistory(ctx context.Context, h HistoryItem, keep int) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `INSERT INTO query_history(user_id, target_id, database, sql, class, result, duration_ms, rows, error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		h.UserID, h.TargetID, h.Database, h.SQL, h.Class, h.Result, h.DurationMs, h.Rows, h.Error, ts(h.CreatedAt))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM query_history WHERE user_id = ? AND id NOT IN
		(SELECT id FROM query_history WHERE user_id = ? ORDER BY id DESC LIMIT ?)`, h.UserID, h.UserID, keep); err != nil {
		return 0, err
	}
	// and at most historyBytes of SQL per user, oldest first
	if _, err := tx.ExecContext(ctx, `DELETE FROM query_history WHERE id IN (
		SELECT id FROM (SELECT id, sum(length(CAST(sql AS BLOB))) OVER (ORDER BY id DESC) AS total FROM query_history WHERE user_id = ?)
		WHERE total > ?)`, h.UserID, historyBytes); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *SQLite) ListHistory(ctx context.Context, userID string, f HistoryFilter) ([]HistoryItem, error) {
	q := `SELECT id, user_id, target_id, database, sql, class, result, duration_ms, rows, error, created_at FROM query_history WHERE user_id = ?`
	args := []any{userID}
	if f.TargetID != "" {
		q += ` AND target_id = ?`
		args = append(args, f.TargetID)
	}
	if f.Database != "" {
		q += ` AND database = ?`
		args = append(args, f.Database)
	}
	if f.Before > 0 {
		q += ` AND id < ?`
		args = append(args, f.Before)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, clampLimit(f.Limit, 50, 500))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []HistoryItem{}
	for rows.Next() {
		var h HistoryItem
		var created string
		if err := rows.Scan(&h.ID, &h.UserID, &h.TargetID, &h.Database, &h.SQL, &h.Class, &h.Result, &h.DurationMs, &h.Rows, &h.Error, &created); err != nil {
			return nil, err
		}
		h.CreatedAt = parseTS(created)
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *SQLite) DeleteHistory(ctx context.Context, userID string, id int64) error {
	return expectOne(s.db.ExecContext(ctx, `DELETE FROM query_history WHERE user_id = ? AND id = ?`, userID, id))
}

func (s *SQLite) ListSavedQueries(ctx context.Context, userID string) ([]SavedQuery, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, name, sql, target_id, database, created_at, updated_at
		FROM saved_queries WHERE user_id = ? ORDER BY name`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []SavedQuery{}
	for rows.Next() {
		q, err := scanSaved(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *q)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scanSaved(r scanner) (*SavedQuery, error) {
	var q SavedQuery
	var created, updated string
	if err := r.Scan(&q.ID, &q.UserID, &q.Name, &q.SQL, &q.TargetID, &q.Database, &created, &updated); err != nil {
		return nil, err
	}
	q.CreatedAt, q.UpdatedAt = parseTS(created), parseTS(updated)
	return &q, nil
}

func (s *SQLite) getSaved(ctx context.Context, userID string, id int64) (*SavedQuery, error) {
	q, err := scanSaved(s.db.QueryRowContext(ctx, `SELECT id, user_id, name, sql, target_id, database, created_at, updated_at
		FROM saved_queries WHERE user_id = ? AND id = ?`, userID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return q, err
}

func (s *SQLite) CreateSavedQuery(ctx context.Context, q SavedQuery) (*SavedQuery, error) {
	now := ts(time.Now())
	res, err := s.db.ExecContext(ctx, `INSERT INTO saved_queries(user_id, name, sql, target_id, database, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, q.UserID, q.Name, q.SQL, q.TargetID, q.Database, now, now)
	if err != nil {
		return nil, mapConstraint(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.getSaved(ctx, q.UserID, id)
}

func (s *SQLite) UpdateSavedQuery(ctx context.Context, q SavedQuery) (*SavedQuery, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE saved_queries SET name = ?, sql = ?, target_id = ?, database = ?, updated_at = ?
		WHERE user_id = ? AND id = ?`, q.Name, q.SQL, q.TargetID, q.Database, ts(time.Now()), q.UserID, q.ID)
	if err := expectOne(res, mapConstraint(err)); err != nil {
		return nil, err
	}
	return s.getSaved(ctx, q.UserID, q.ID)
}

func (s *SQLite) DeleteSavedQuery(ctx context.Context, userID string, id int64) error {
	return expectOne(s.db.ExecContext(ctx, `DELETE FROM saved_queries WHERE user_id = ? AND id = ?`, userID, id))
}

func (s *SQLite) GetDraft(ctx context.Context, userID, targetID, database string) (*Draft, error) {
	var d Draft
	var updated string
	err := s.db.QueryRowContext(ctx, `SELECT sql, updated_at FROM drafts WHERE user_id = ? AND target_id = ? AND database = ?`,
		userID, targetID, database).Scan(&d.SQL, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.UpdatedAt = parseTS(updated)
	return &d, nil
}

func (s *SQLite) UserItems(ctx context.Context, userID string) (UserItems, error) {
	var u UserItems
	err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM saved_queries WHERE user_id = ?),
		(SELECT count(*) FROM drafts WHERE user_id = ?),
		(SELECT count(*) FROM ui_state WHERE user_id = ?),
		(SELECT coalesce(sum(length(CAST(sql AS BLOB))), 0) FROM saved_queries WHERE user_id = ?) +
		(SELECT coalesce(sum(length(CAST(sql AS BLOB))), 0) FROM drafts WHERE user_id = ?) +
		(SELECT coalesce(sum(length(CAST(value_json AS BLOB))), 0) FROM ui_state WHERE user_id = ?)`,
		userID, userID, userID, userID, userID, userID).Scan(&u.Saved, &u.Drafts, &u.UIState, &u.Bytes)
	return u, err
}

func (s *SQLite) PutDraft(ctx context.Context, userID, targetID, database, sqlText string) (*Draft, error) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err := s.db.ExecContext(ctx, `INSERT INTO drafts(user_id, target_id, database, sql, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id, target_id, database) DO UPDATE SET sql = excluded.sql, updated_at = excluded.updated_at`,
		userID, targetID, database, sqlText, ts(now))
	if err != nil {
		return nil, err
	}
	return &Draft{SQL: sqlText, UpdatedAt: now}, nil
}

func (s *SQLite) GetUIState(ctx context.Context, userID, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value_json FROM ui_state WHERE user_id = ? AND key = ?`, userID, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

func (s *SQLite) PutUIState(ctx context.Context, userID, key, valueJSON string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO ui_state(user_id, key, value_json, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
		userID, key, valueJSON, ts(time.Now()))
	return err
}

func (s *SQLite) InsertAudit(ctx context.Context, r AuditRecord) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_log(ts, event, user_id, email, client_ip, user_agent, target_id, database, object,
		statement_class, destructive, sql, rows_affected, duration_ms, result, error_code, details_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ts(r.TS), r.Event, nullStr(r.UserID), nullStr(r.Email), nullStr(r.ClientIP), nullStr(r.UserAgent), nullStr(r.TargetID),
		nullStr(r.Database), nullStr(r.Object), nullStr(r.StatementClass), r.Destructive, nullStr(r.SQL), r.RowsAffected,
		r.DurationMs, r.Result, nullStr(r.ErrorCode), nullStr(r.Details))
	return err
}

// auditWhere builds the WHERE clause shared by ListAudit and CountAudit.
func auditWhere(f AuditFilter) (string, []any) {
	q := ` WHERE 1=1`
	var args []any
	if f.User != "" {
		q += ` AND (user_id = ? OR email = ? COLLATE NOCASE)`
		args = append(args, f.User, f.User)
	}
	if f.TargetID != "" {
		q += ` AND target_id = ?`
		args = append(args, f.TargetID)
	}
	if f.Event != "" {
		q += ` AND event = ?`
		args = append(args, f.Event)
	}
	if !f.From.IsZero() {
		q += ` AND ts >= ?`
		args = append(args, ts(f.From))
	}
	if !f.To.IsZero() {
		q += ` AND ts < ?`
		args = append(args, ts(f.To))
	}
	if f.Before > 0 {
		q += ` AND id < ?`
		args = append(args, f.Before)
	}
	return q, args
}

func (s *SQLite) CountAudit(ctx context.Context, f AuditFilter) (int, error) {
	where, args := auditWhere(f)
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_log`+where, args...).Scan(&n)
	return n, err
}

func (s *SQLite) ListAudit(ctx context.Context, f AuditFilter) ([]AuditRecord, error) {
	where, args := auditWhere(f)
	// #nosec G202 -- where is built from constant fragments; every value is a bound parameter
	q := `SELECT id, ts, event, user_id, email, client_ip, user_agent, target_id, database, object, statement_class, destructive,
		sql, rows_affected, duration_ms, result, error_code, details_json FROM audit_log` + where
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, clampLimit(f.Limit, 100, 1000))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []AuditRecord{}
	for rows.Next() {
		var r AuditRecord
		var t string
		var userID, email, ip, ua, target, db, obj, class, sqlText, code, details sql.NullString
		if err := rows.Scan(&r.ID, &t, &r.Event, &userID, &email, &ip, &ua, &target, &db, &obj, &class, &r.Destructive,
			&sqlText, &r.RowsAffected, &r.DurationMs, &r.Result, &code, &details); err != nil {
			return nil, err
		}
		r.TS = parseTS(t)
		r.UserID, r.Email, r.ClientIP, r.UserAgent = userID.String, email.String, ip.String, ua.String
		r.TargetID, r.Database, r.Object, r.StatementClass = target.String, db.String, obj.String, class.String
		r.SQL, r.ErrorCode, r.Details = sqlText.String, code.String, details.String
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLite) PurgeAudit(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM audit_log WHERE ts < ?`, ts(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func expectOne(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func mapConstraint(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return ErrConflict
	}
	return err
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func clampLimit(n, def, maxN int) int {
	if n <= 0 {
		return def
	}
	return min(n, maxN)
}
