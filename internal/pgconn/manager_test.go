package pgconn

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Warewave-Technology/athens/internal/authz"
	"github.com/Warewave-Technology/athens/internal/discovery"
)

// TestRunFailsClosed: requests without a level, and read requests on a
// target without a read-only login, never reach a pool (ADR-028).
func TestRunFailsClosed(t *testing.T) {
	m := NewManager(Options{})
	called := false
	fn := func(context.Context, pgx.Tx) error { called = true; return nil }
	tg := discovery.Target{ID: "ns/db"}
	if err := m.Run(context.Background(), Request{Target: tg, Level: authz.None}, fn); !errors.Is(err, ErrNoAccess) {
		t.Fatalf("level none: %v", err)
	}
	if err := m.Run(context.Background(), Request{Target: tg, Level: authz.Read}, fn); !errors.Is(err, ErrReadLoginRequired) {
		t.Fatalf("read without a read-only login: %v", err)
	}
	if called || len(m.pools) != 0 {
		t.Fatal("a refused request reached the database")
	}
}
