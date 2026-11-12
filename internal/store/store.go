// Package store persists users, sessions, history, saved queries, drafts,
// UI state and the audit log. Store is an interface so a Postgres backend can
// be added later (ADR-003); SQLite is the only implementation in v1.
package store

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

type User struct {
	ID          string
	Email       string
	Name        string
	CreatedAt   time.Time
	LastLoginAt time.Time
}

// Session is a server-side session. ID is sha256(cookie value), never the raw token.
type Session struct {
	ID         string
	UserID     string
	Email      string // from users, filled by GetSession
	Name       string // from users, filled by GetSession
	Groups     []string
	CSRFToken  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	ClientIP   string
	UserAgent  string
}

type HistoryItem struct {
	ID         int64     `json:"id"`
	UserID     string    `json:"-"`
	TargetID   string    `json:"targetId"`
	Database   string    `json:"database"`
	SQL        string    `json:"sql"`
	Class      string    `json:"class"`
	Result     string    `json:"result"`
	DurationMs *int64    `json:"durationMs"`
	Rows       *int64    `json:"rows"`
	Error      *string   `json:"error"`
	CreatedAt  time.Time `json:"createdAt"`
}

type HistoryFilter struct {
	TargetID string
	Database string
	Limit    int
	Before   int64 // id; 0 = newest
}

type SavedQuery struct {
	ID        int64     `json:"id"`
	UserID    string    `json:"-"`
	Name      string    `json:"name"`
	SQL       string    `json:"sql"`
	TargetID  *string   `json:"targetId"`
	Database  *string   `json:"database"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Draft struct {
	SQL       string    `json:"sql"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type AuditRecord struct {
	ID             int64     `json:"id"`
	TS             time.Time `json:"ts"`
	Event          string    `json:"event"`
	UserID         string    `json:"userId,omitempty"`
	Email          string    `json:"email,omitempty"`
	ClientIP       string    `json:"clientIp,omitempty"`
	UserAgent      string    `json:"userAgent,omitempty"`
	TargetID       string    `json:"targetId,omitempty"`
	Database       string    `json:"database,omitempty"`
	Object         string    `json:"object,omitempty"`
	StatementClass string    `json:"statementClass,omitempty"`
	Destructive    bool      `json:"destructive"`
	SQL            string    `json:"sql,omitempty"`
	RowsAffected   *int64    `json:"rowsAffected,omitempty"`
	DurationMs     *int64    `json:"durationMs,omitempty"`
	Result         string    `json:"result"`
	ErrorCode      string    `json:"errorCode,omitempty"`
	Details        string    `json:"details,omitempty"` // JSON
}

type AuditFilter struct {
	User     string // user id or email (exact)
	TargetID string
	Event    string
	From, To time.Time
	Limit    int
	Before   int64
}

type Store interface {
	Ping(ctx context.Context) error
	Close() error

	UpsertUser(ctx context.Context, u User) error

	CreateSession(ctx context.Context, s Session) error
	GetSession(ctx context.Context, id string) (*Session, error)
	TouchSession(ctx context.Context, id string, at time.Time) error
	DeleteSession(ctx context.Context, id string) error
	// PurgeSessions deletes sessions past their absolute expiry or idle longer than idleTTL.
	PurgeSessions(ctx context.Context, now time.Time, idleTTL time.Duration) (int64, error)

	// AddHistory appends and trims the user's history to keep items.
	AddHistory(ctx context.Context, h HistoryItem, keep int) (int64, error)
	ListHistory(ctx context.Context, userID string, f HistoryFilter) ([]HistoryItem, error)
	DeleteHistory(ctx context.Context, userID string, id int64) error

	ListSavedQueries(ctx context.Context, userID string) ([]SavedQuery, error)
	CreateSavedQuery(ctx context.Context, q SavedQuery) (*SavedQuery, error)
	UpdateSavedQuery(ctx context.Context, q SavedQuery) (*SavedQuery, error)
	DeleteSavedQuery(ctx context.Context, userID string, id int64) error

	GetDraft(ctx context.Context, userID, targetID, database string) (*Draft, error)
	PutDraft(ctx context.Context, userID, targetID, database, sql string) (*Draft, error)

	// UserItemCounts counts a user's saved queries, drafts and UI state keys
	// (per-user caps).
	UserItemCounts(ctx context.Context, userID string) (saved, drafts, uiState int, err error)

	GetUIState(ctx context.Context, userID, key string) (string, error)
	PutUIState(ctx context.Context, userID, key, valueJSON string) error

	InsertAudit(ctx context.Context, r AuditRecord) error
	ListAudit(ctx context.Context, f AuditFilter) ([]AuditRecord, error)
	PurgeAudit(ctx context.Context, before time.Time) (int64, error)
}
