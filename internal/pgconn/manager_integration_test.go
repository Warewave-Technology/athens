//go:build integration

package pgconn_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Warewave-Technology/athens/internal/authz"
	"github.com/Warewave-Technology/athens/internal/config"
	"github.com/Warewave-Technology/athens/internal/discovery"
	athenspg "github.com/Warewave-Technology/athens/internal/pgconn"
	"github.com/Warewave-Technology/athens/internal/testutil/pgtest"
)

var instances []*pgtest.Instance

func TestMain(m *testing.M) {
	ctx := context.Background()
	for _, img := range pgtest.Versions {
		in, err := pgtest.Start(ctx, img)
		if err != nil {
			fmt.Fprintln(os.Stderr, "start", img, err)
			os.Exit(1)
		}
		instances = append(instances, in)
	}
	code := m.Run()
	for _, in := range instances {
		in.Stop(ctx)
	}
	os.Exit(code)
}

func each(t *testing.T, fn func(t *testing.T, in *pgtest.Instance)) {
	for _, in := range instances {
		t.Run(in.Image, func(t *testing.T) { fn(t, in) })
	}
}

func TestApplicationNameAndGuards(t *testing.T) {
	each(t, func(t *testing.T, in *pgtest.Instance) {
		tg, client := in.Target("guards")
		m := in.Manager(tg, client, pgtest.Limits())
		defer m.CloseAll()
		err := m.Run(context.Background(), pgtest.Req(tg, "app", authz.Write), func(ctx context.Context, tx pgx.Tx) error {
			var app, st, lt string
			if err := tx.QueryRow(ctx, `SELECT application_name, current_setting('statement_timeout'), current_setting('lock_timeout')
				FROM pg_stat_activity WHERE pid = pg_backend_pid()`).Scan(&app, &st, &lt); err != nil {
				return err
			}
			if app != "athens:it@example.com" || st != "30s" || lt != "5s" {
				return fmt.Errorf("app=%q statement_timeout=%q lock_timeout=%q", app, st, lt)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if m.Status(tg.ID) != "ok" {
			t.Fatalf("status %s", m.Status(tg.ID))
		}
	})
}

func TestReadOnlyEnforced(t *testing.T) {
	each(t, func(t *testing.T, in *pgtest.Instance) {
		tg, client := in.Target("ro")
		noRO := tg
		noRO.RO = nil // force rw credential + READ ONLY tx
		m := in.Manager(noRO, client, pgtest.Limits())
		defer m.CloseAll()
		err := m.Run(context.Background(), pgtest.Req(noRO, "app", authz.Read), func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "CREATE TABLE should_fail(id int)")
			return err
		})
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || pe.Code != "25006" {
			t.Fatalf("want read_only_sql_transaction, got %v", err)
		}
		// write level with ReadOnly flag (catalog path) is read-only too
		req := pgtest.Req(noRO, "app", authz.Write)
		req.ReadOnly = true
		err = m.Run(context.Background(), req, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "CREATE TABLE should_fail(id int)")
			return err
		})
		if !errors.As(err, &pe) || pe.Code != "25006" {
			t.Fatalf("ReadOnly flag: got %v", err)
		}
	})
}

func TestROCredentialUsedAndFallback(t *testing.T) {
	each(t, func(t *testing.T, in *pgtest.Instance) {
		tg, client := in.Target("rocred")
		m := in.Manager(tg, client, pgtest.Limits())
		defer m.CloseAll()
		user := func(level authz.Level) string {
			var u string
			if err := m.Run(context.Background(), pgtest.Req(tg, "app", level), func(ctx context.Context, tx pgx.Tx) error {
				return tx.QueryRow(ctx, "SELECT current_user").Scan(&u)
			}); err != nil {
				t.Fatal(err)
			}
			return u
		}
		if u := user(authz.Read); u != pgtest.ROUser {
			t.Fatalf("read user = %s", u)
		}
		if u := user(authz.Write); u != "postgres" {
			t.Fatalf("write user = %s", u)
		}
		// break the RO secret: read falls back to rw + READ ONLY
		bad := tg
		ro := discovery.CredentialRef{User: tg.RO.User, Password: discovery.ValueSource{Secret: &discovery.KeyRef{Name: "rocred-rw", Key: "username"}}}
		bad.RO = &ro
		m2 := in.Manager(bad, client, pgtest.Limits())
		defer m2.CloseAll()
		var u string
		if err := m2.Run(context.Background(), pgtest.Req(bad, "app", authz.Read), func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, "SELECT current_user").Scan(&u)
		}); err != nil {
			t.Fatal(err)
		}
		if u != "postgres" || !m2.ROFallback(bad.ID) {
			t.Fatalf("fallback user=%s flag=%v", u, m2.ROFallback(bad.ID))
		}
	})
}

func TestRotatedPassword(t *testing.T) {
	each(t, func(t *testing.T, in *pgtest.Instance) {
		ctx := context.Background()
		admin, err := in.Connect(ctx, "app")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = admin.Close(ctx) }()
		if _, err := admin.Exec(ctx, "CREATE ROLE rot LOGIN SUPERUSER PASSWORD 'one'"); err != nil {
			t.Fatal(err)
		}
		tg, client := in.Target("rot")
		sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "rot-rw", Namespace: pgtest.Namespace},
			Data: map[string][]byte{"username": []byte("rot"), "password": []byte("one")}}
		if _, err := client.CoreV1().Secrets(pgtest.Namespace).Update(ctx, sec, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		limits := pgtest.Limits()
		m := in.Manager(tg, client, limits)
		defer m.CloseAll()
		ping := func() error {
			return m.Run(ctx, pgtest.Req(tg, "app", authz.Write), func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, "SELECT 1")
				return err
			})
		}
		if err := ping(); err != nil {
			t.Fatal(err)
		}
		// rotate: change password in DB and Secret, kill idle connections
		if _, err := admin.Exec(ctx, "ALTER ROLE rot PASSWORD 'two'"); err != nil {
			t.Fatal(err)
		}
		sec.Data["password"] = []byte("two")
		if _, err := client.CoreV1().Secrets(pgtest.Namespace).Update(ctx, sec, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = 'rot'"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
		var lastErr error
		for range 3 { // the pool may first hand out a connection that was just terminated
			if lastErr = ping(); lastErr == nil {
				break
			}
		}
		if lastErr != nil {
			t.Fatalf("after rotation: %v", lastErr)
		}
	})
}

func TestUnavailableIsGeneric(t *testing.T) {
	tg, client := instances[0].Target("down")
	tg.Port = 1
	m := instances[0].Manager(tg, client, pgtest.Limits())
	defer m.CloseAll()
	err := m.Run(context.Background(), pgtest.Req(tg, "app", authz.Write), func(context.Context, pgx.Tx) error { return nil })
	var ue *athenspg.ErrUnavailable
	if !errors.As(err, &ue) || err.Error() != "target unavailable" || strings.Contains(err.Error(), "password") {
		t.Fatalf("err = %v", err)
	}
	if m.Status(tg.ID) != "unreachable" {
		t.Fatalf("status %s", m.Status(tg.ID))
	}
}

func TestTimeoutCancelAndReset(t *testing.T) {
	each(t, func(t *testing.T, in *pgtest.Instance) {
		tg, client := in.Target("timeout")
		limits := pgtest.Limits()
		limits.StatementTimeout = config.Duration(300 * time.Millisecond)
		m := in.Manager(tg, client, limits)
		defer m.CloseAll()
		err := m.Run(context.Background(), pgtest.Req(tg, "app", authz.Write), func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "SELECT pg_sleep(5)")
			return err
		})
		if !errors.Is(err, athenspg.ErrTimeout) {
			t.Fatalf("want timeout, got %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		limits.StatementTimeout = config.Duration(time.Minute)
		m2 := in.Manager(tg, client, limits)
		defer m2.CloseAll()
		start := time.Now()
		err = m2.Run(ctx, pgtest.Req(tg, "app", authz.Write), func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "SELECT pg_sleep(60)")
			return err
		})
		if !errors.Is(err, athenspg.ErrCancelled) || time.Since(start) > 2*time.Second {
			t.Fatalf("cancel: %v after %s", err, time.Since(start))
		}

		// session state from user SQL must not survive (ADR-011)
		req := pgtest.Req(tg, "app", authz.Read)
		req.Reset = true
		limits.PoolMaxConns = 1
		m3 := in.Manager(tg, client, limits)
		defer m3.CloseAll()
		if err := m3.Run(context.Background(), req, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "SELECT set_config('search_path', 'leaked', false), pg_advisory_lock(42)")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		var sp string
		var locks int
		if err := m3.Run(context.Background(), pgtest.Req(tg, "app", authz.Read), func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, "SELECT current_setting('search_path'), (SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND pid = pg_backend_pid())").Scan(&sp, &locks)
		}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(sp, "leaked") || locks != 0 {
			t.Fatalf("session state leaked: search_path=%q advisory locks=%d", sp, locks)
		}
	})
}

func TestTextResults(t *testing.T) {
	each(t, func(t *testing.T, in *pgtest.Instance) {
		tg, client := in.Target("text")
		m := in.Manager(tg, client, pgtest.Limits())
		defer m.CloseAll()
		var res *athenspg.TextResult
		err := m.Run(context.Background(), pgtest.Req(tg, "app", authz.Read), func(ctx context.Context, tx pgx.Tx) error {
			var err error
			res, err = athenspg.QueryText(ctx, tx, `SELECT 12345678901234567890.123456789::numeric AS n, 9007199254740993::int8 AS big,
				'{"a": [1, 2]}'::jsonb AS j, ARRAY[1,NULL,3] AS arr, '\xdeadbeef'::bytea AS b,
				'2024-01-02 03:04:05.678+00'::timestamptz AS ts, '1 day 02:03:04'::interval AS iv,
				'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11'::uuid AS u, ROW(1,'x') AS comp, NULL::text AS nul,
				repeat('é', 20) AS long, $1::text AS param`, []*string{ptr("bound")}, athenspg.TextOptions{MaxRows: 10, MaxCellBytes: 41})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		row := res.Rows[0]
		want := []string{"12345678901234567890.123456789", "9007199254740993", `{"a": [1, 2]}`, "{1,NULL,3}", `\xdeadbeef`,
			"", "1 day 02:03:04", "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11", "(1,x)"}
		for i, w := range want {
			if w == "" {
				continue
			}
			if got := *row[i]; got != w {
				t.Errorf("col %s = %q want %q", res.Columns[i].Name, got, w)
			}
		}
		if !strings.HasPrefix(*row[5], "2024-01-02") || row[9] != nil || *row[11] != "bound" {
			t.Errorf("ts=%v nul=%v param=%v", *row[5], row[9], row[11])
		}
		if *row[10] != strings.Repeat("é", 20) {
			t.Errorf("long = %q", *row[10])
		}
		if res.Columns[0].Type != "numeric" || res.Columns[3].Type != "integer[]" {
			t.Errorf("types %+v", res.Columns)
		}
		if len(res.TruncatedCells) != 0 {
			t.Errorf("truncated cells %v", res.TruncatedCells)
		}
		err = m.Run(context.Background(), pgtest.Req(tg, "app", authz.Read), func(ctx context.Context, tx pgx.Tx) error {
			var err error
			res, err = athenspg.QueryText(ctx, tx, "SELECT repeat('é', 10), 'short'", nil, athenspg.TextOptions{MaxRows: 1, MaxCellBytes: 7})
			return err
		})
		if err != nil || *res.Rows[0][0] != "ééé" || *res.Rows[0][1] != "short" || len(res.TruncatedCells) != 1 {
			t.Errorf("truncated cells %v", res.TruncatedCells)
		}

		err = m.Run(context.Background(), pgtest.Req(tg, "app", authz.Read), func(ctx context.Context, tx pgx.Tx) error {
			var err error
			res, err = athenspg.QueryText(ctx, tx, "SELECT g FROM generate_series(1, 1000000) g", nil, athenspg.TextOptions{MaxRows: 5, Cursor: true})
			return err
		})
		if err != nil || len(res.Rows) != 5 || !res.Truncated || res.Columns[0].Type != "integer" {
			t.Fatalf("cursor cap: %v rows=%d truncated=%v", err, len(res.Rows), res != nil && res.Truncated)
		}
	})
}

func ptr(s string) *string { return &s }

// Pooled connections that die (server restart, network reset) must not fail
// the next request: athens reconnects once before giving up.
func TestStaleConnectionsRecovered(t *testing.T) {
	each(t, func(t *testing.T, in *pgtest.Instance) {
		ctx := context.Background()
		tg, client := in.Target("stale")
		limits := pgtest.Limits()
		limits.PoolMaxConns = 2
		m := in.Manager(tg, client, limits)
		defer m.CloseAll()
		ping := func() error {
			return m.Run(ctx, pgtest.Req(tg, "app", authz.Write), func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, "SELECT 1")
				return err
			})
		}
		if err := ping(); err != nil {
			t.Fatal(err)
		}
		admin, err := in.Connect(ctx, "app")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = admin.Close(ctx) }()
		if _, err := admin.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name LIKE 'athens%' AND pid <> pg_backend_pid()"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
		if err := ping(); err != nil {
			t.Fatalf("first request after the connections died: %v", err)
		}
	})
}
