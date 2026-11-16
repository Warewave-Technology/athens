package query

import (
	"context"
	"errors"
	"sync"
)

var (
	ErrTooMany   = errors.New("too many running queries")
	ErrDuplicate = errors.New("query id already in use")
	ErrNotFound  = errors.New("query not found")
)

type running struct {
	userID string
	cancel context.CancelFunc
	// where the query runs, once its connection is known (Attach)
	targetID, database, sql string
	pid                     uint32
}

// Attached is an editor query running on a known server process.
type Attached struct {
	ID     string
	UserID string
	SQL    string
}

// Registry tracks running editor queries for cancellation and the per-user
// concurrency limit (SPEC §6.4, §12).
type Registry struct {
	mu      sync.Mutex
	byID    map[string]*running
	perUser map[string]int
}

func NewRegistry() *Registry {
	return &Registry{byID: map[string]*running{}, perUser: map[string]int{}}
}

// Start registers a query; the caller must call Finish.
func (r *Registry) Start(userID, id string, limit int, cancel context.CancelFunc) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[id]; ok {
		return ErrDuplicate
	}
	if r.perUser[userID] >= limit {
		return ErrTooMany
	}
	r.byID[id] = &running{userID: userID, cancel: cancel}
	r.perUser[userID]++
	return nil
}

func (r *Registry) Finish(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if q, ok := r.byID[id]; ok {
		delete(r.byID, id)
		if r.perUser[q.userID]--; r.perUser[q.userID] <= 0 {
			delete(r.perUser, q.userID)
		}
	}
}

// Cancel cancels id if userID owns it (or admin is true). It returns the
// owner's id. Unknown or foreign queries are ErrNotFound (no enumeration).
func (r *Registry) Cancel(id, userID string, admin bool) (string, error) {
	r.mu.Lock()
	q, ok := r.byID[id]
	r.mu.Unlock()
	if !ok || (q.userID != userID && !admin) {
		return "", ErrNotFound
	}
	q.cancel()
	return q.userID, nil
}

// Attach records the server process running query id and its statement
// (a read runs through a cursor, so the server only shows the FETCH).
func (r *Registry) Attach(id, targetID, database, sql string, pid uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if q, ok := r.byID[id]; ok {
		q.targetID, q.database, q.sql, q.pid = targetID, database, sql, pid
	}
}

// OnDatabase maps the server process ids of the editor queries running on
// targetID/database to them.
func (r *Registry) OnDatabase(targetID, database string) map[uint32]Attached {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[uint32]Attached{}
	for id, q := range r.byID {
		if q.targetID == targetID && q.database == database && q.pid != 0 {
			out[q.pid] = Attached{ID: id, UserID: q.userID, SQL: q.sql}
		}
	}
	return out
}

// Running returns the number of running queries (tests, metrics).
func (r *Registry) Running() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byID)
}
