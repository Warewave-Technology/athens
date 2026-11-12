package discovery

import (
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Warewave-Technology/athens/internal/metrics"
)

type EventType string

const (
	Added   EventType = "added"
	Updated EventType = "updated"
	Removed EventType = "removed"
)

type Event struct {
	Type   EventType
	Target Target
}

// NamespaceStatus is the last rebuild result of one namespace (admin page).
type NamespaceStatus struct {
	Namespace   string    `json:"namespace"`
	LastRebuild time.Time `json:"lastRebuild"`
	Problems    []Problem `json:"problems"`
	Error       string    `json:"error,omitempty"`
}

// Registry holds the current targets. Safe for concurrent use.
type Registry struct {
	mu        sync.RWMutex
	targets   map[string]Target
	status    map[string]NamespaceStatus
	listeners []func(Event)
}

func NewRegistry() *Registry {
	return &Registry{targets: map[string]Target{}, status: map[string]NamespaceStatus{}}
}

// OnChange registers a listener called synchronously for every change.
func (r *Registry) OnChange(fn func(Event)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listeners = append(r.listeners, fn)
}

func (r *Registry) Get(id string) (Target, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.targets[id]
	return t, ok
}

// List returns all targets sorted by ID.
func (r *Registry) List() []Target {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Target, 0, len(r.targets))
	for _, t := range r.targets {
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b Target) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func (r *Registry) Status() []NamespaceStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]NamespaceStatus, 0, len(r.status))
	for _, s := range r.status {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b NamespaceStatus) int { return strings.Compare(a.Namespace, b.Namespace) })
	return out
}

// SetNamespace replaces all targets of ns and emits the diff.
func (r *Registry) SetNamespace(ns string, targets []Target, problems []Problem, at time.Time) []Event {
	r.mu.Lock()
	var events []Event
	next := map[string]Target{}
	for _, t := range targets {
		next[t.ID] = t
	}
	for id, old := range r.targets {
		if old.Namespace != ns {
			continue
		}
		if t, ok := next[id]; !ok {
			delete(r.targets, id)
			events = append(events, Event{Type: Removed, Target: old})
		} else if !reflect.DeepEqual(old, t) {
			r.targets[id] = t
			events = append(events, Event{Type: Updated, Target: t})
		}
	}
	for id, t := range next {
		if _, ok := r.targets[id]; !ok {
			r.targets[id] = t
			events = append(events, Event{Type: Added, Target: t})
		}
	}
	prev := r.status[ns].Problems
	r.status[ns] = NamespaceStatus{Namespace: ns, LastRebuild: at, Problems: problems}
	listeners := slices.Clone(r.listeners)
	r.mu.Unlock()

	// metrics: gauge per (ns, source); count only problems not seen before
	counts := map[SourceKind]int{SourceAnnotation: 0, SourceCNPG: 0, SourceZalando: 0}
	for _, t := range targets {
		counts[t.Source]++
	}
	for src, n := range counts {
		if n == 0 {
			metrics.DiscoveryTargets.DeleteLabelValues(ns, string(src)) // no series for empty namespaces
			continue
		}
		metrics.DiscoveryTargets.WithLabelValues(ns, string(src)).Set(float64(n))
	}
	for _, p := range problems {
		if !slices.ContainsFunc(prev, func(q Problem) bool { return q.Object == p.Object && q.Reason == p.Reason }) {
			metrics.DiscoveryErrors.WithLabelValues(string(p.Source), p.Reason).Inc()
		}
	}
	for _, e := range events {
		for _, fn := range listeners {
			fn(e)
		}
	}
	return events
}

// Forget drops the status of ns (used for empty namespaces in a
// cluster-wide watch). Targets are not touched.
func (r *Registry) Forget(ns string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.status, ns)
}

// SetNamespaceError records a failure to rebuild ns (targets are kept).
func (r *Registry) SetNamespaceError(ns string, err error, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.status[ns]
	s.Namespace, s.LastRebuild, s.Error = ns, at, err.Error()
	r.status[ns] = s
}
