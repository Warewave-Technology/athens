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

// Running returns the number of running queries (tests, metrics).
func (r *Registry) Running() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byID)
}
