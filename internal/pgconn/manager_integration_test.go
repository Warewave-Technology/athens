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
		m := in.Manager(tg, client, pgtest.Limits())
		defer m.CloseAll()
		create := func(m *athenspg.Manager, req athenspg.Request) error {
			return m.Run(context.Background(), req, func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, "CREATE TABLE should_fail(id int)")
				return err
			})
		}
		var pe *pgconn.PgError
		if err := create(m, pgtest.Req(tg, "app", authz.Read)); !errors.As(err, &pe) || pe.Code != "25006" {
			t.Fatalf("want read_only_sql_transaction, got %v", err)
		}
		// write level with ReadOnly flag (catalog path) is read-only too
		req := pgtest.Req(tg, "app", authz.Write)
		req.ReadOnly = true
		if err := create(m, req); !errors.As(err, &pe) || pe.Code != "25006" {
			t.Fatalf("ReadOnly flag: got %v", err)
		}
		// no read-only login: read users never reach the server (ADR-028)
		noRO := tg
		noRO.RO = nil
		if err := create(m, pgtest.Req(noRO, "app", authz.Read)); !errors.Is(err, athenspg.ErrReadLoginRequired) {
			t.Fatalf("without a read-only login: %v", err)
		}
	})
}

// TestROCredentialNoFallback: read users use the read-only login; a rejected
// one is an error and an admin warning, never a fallback to the RW login, and
// it does not mark the target unreachable (ADR-028).
func TestROCredentialNoFallback(t *testing.T) {
	each(t, func(t *testing.T, in *pgtest.Instance) {
		tg, client := in.Target("rocred")
		user := func(m *athenspg.Manager, tg discovery.Target, level authz.Level) (string, error) {
			var u string
			err := m.Run(context.Background(), pgtest.Req(tg, "app", level), func(ctx context.Context, tx pgx.Tx) error {
				return tx.QueryRow(ctx, "SELECT session_user").Scan(&u)
			})
			return u, err
		}
		m := in.Manager(tg, client, pgtest.Limits())
		defer m.CloseAll()
		if u, err := user(m, tg, authz.Read); err != nil || u != pgtest.ROUser {
			t.Fatalf("read user = %s %v", u, err)
		}
		if u, err := user(m, tg, authz.Write); err != nil || u != "postgres" {
			t.Fatalf("write user = %s %v", u, err)
		}
		// break the RO secret: read users get an error, not the RW login
		bad := tg
		ro := discovery.CredentialRef{User: tg.RO.User, Password: discovery.ValueSource{Secret: &discovery.KeyRef{Name: "rocred-rw", Key: "username"}}}
		bad.RO = &ro
		m2 := in.Manager(bad, client, pgtest.Limits())
		defer m2.CloseAll()
		if u, err := user(m2, bad, authz.Read); !errors.Is(err, athenspg.ErrReadLoginUnavailable) {
			t.Fatalf("rejected read-only login: user=%q err=%v", u, err)
		}
		if !m2.ROFailed(bad.ID) {
			t.Fatal("ROFailed not set")
		}
		if u, err := user(m2, bad, authz.Write); err != nil || u != "postgres" {
			t.Fatalf("writers unaffected: %s %v", u, err)
		}
		if s := m2.Status(bad.ID); s != "ok" {
			t.Fatalf("status %s", s)
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

// TestMissingDefaultDatabase: the guessed initial database (here a user name
// without a database of that name) does not exist; the default-database pool
// falls back to "postgres" and reports it, explicit databases are untouched.
func TestMissingDefaultDatabase(t *testing.T) {
	in := instances[0]
	tg, client := in.Target("nodb")
	tg.DefaultDB = "vmetric"
	m := in.Manager(tg, client, pgtest.Limits())
	defer m.CloseAll()
	var db string
	if err := m.Run(context.Background(), pgtest.Req(tg, "", authz.Read), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT current_database()").Scan(&db)
	}); err != nil {
		t.Fatal(err)
	}
	if missing, using := m.DefaultDBFallback(tg.ID); db != "postgres" || missing != "vmetric" || using != "postgres" {
		t.Fatalf("db=%s missing=%q using=%q", db, missing, using)
	}
	if err := m.Run(context.Background(), pgtest.Req(tg, "app", authz.Read), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT current_database()").Scan(&db)
	}); err != nil || db != "app" {
		t.Fatalf("explicit database: %v %s", err, db)
	}
	// a missing explicit database is still an error, not a silent fallback
	err := m.Run(context.Background(), pgtest.Req(tg, "nope", authz.Read), func(context.Context, pgx.Tx) error { return nil })
	if !errors.Is(err, athenspg.ErrNoDatabase) {
		t.Fatalf("missing explicit database: %v", err)
	}
	// ... and neither keeps a pool nor marks the target unreachable (ADR-027)
	if m.Status(tg.ID) != "ok" {
		t.Fatalf("status after a missing database: %s", m.Status(tg.ID))
	}
	for _, st := range m.Stats() {
		if st.Database == "nope" {
			t.Fatal("pool for a missing database was kept")
		}
	}
	// a target change clears the fallback
	m.TargetChanged(discovery.Event{Type: discovery.Updated, Target: tg})
	if _, using := m.DefaultDBFallback(tg.ID); using != "" {
		t.Fatalf("fallback kept after target change: %q", using)
	}
}

// TestProvisionedLogin (ADR-028): readonly-provision creates an unprivileged
// athens_read login over the RW login, rotates its password when athens
// restarts, and never takes over a role it does not manage.
func TestProvisionedLogin(t *testing.T) {
	ctx := context.Background()
	q := func(m *athenspg.Manager, tg discovery.Target, db string, level authz.Level, sql string) (string, error) {
		var out string
		err := m.Run(ctx, pgtest.Req(tg, db, level), func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, sql).Scan(&out)
		})
		return out, err
	}
	pg16 := instances[0]
	tg, client := pg16.Target("provision")
	tg.RO, tg.ROProvision = nil, true
	m := pg16.Manager(tg, client, pgtest.Limits())
	defer m.CloseAll()

	if u, err := q(m, tg, "app", authz.Read, "SELECT session_user || '/' || current_user"); err != nil || u != "athens_read/athens_read" {
		t.Fatalf("read login = %q %v", u, err)
	}
	attrs, err := q(m, tg, "app", authz.Read, `SELECT concat_ws(',', rolsuper, rolcreatedb, rolcreaterole, rolreplication, rolbypassrls,
		pg_has_role('athens_read', 'pg_read_all_data', 'MEMBER'), shobj_description(oid, 'pg_authid') LIKE 'managed by athens%')
		FROM pg_roles WHERE rolname = 'athens_read'`)
	if err != nil || attrs != "f,f,f,f,f,t,t" {
		t.Fatalf("role attributes = %q %v", attrs, err)
	}
	if err := m.Run(ctx, pgtest.Req(tg, "app", authz.Write), func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "CREATE TABLE prov_t AS SELECT 1 AS id, 'a' AS name")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := q(m, tg, "app", authz.Read, "SELECT count(*)::text FROM prov_t"); err != nil || n != "1" {
		t.Fatalf("data read: %q %v", n, err)
	}
	for _, sql := range []string{"SELECT pg_read_file('PG_VERSION')", "SELECT pg_ls_dir('.')::text"} {
		if _, err := q(m, tg, "app", authz.Read, sql); err == nil {
			t.Errorf("%s must be denied for read users", sql)
		}
	}
	if _, err := q(m, tg, "app", authz.Read, "UPDATE prov_t SET name = name RETURNING 'x'"); err == nil {
		t.Fatal("read users must not write")
	}
	if u, err := q(m, tg, "app", authz.Write, "SELECT session_user"); err != nil || u != "postgres" {
		t.Fatalf("write user = %q %v", u, err)
	}
	if msg := m.ProvisionError(tg.ID); msg != "" {
		t.Fatalf("ProvisionError = %q", msg)
	}

	// a restarted athens sets a new password; the old manager's new
	// connections fail once, re-provision and carry on
	m2 := pg16.Manager(tg, client, pgtest.Limits())
	defer m2.CloseAll()
	if u, err := q(m2, tg, "app", authz.Read, "SELECT session_user"); err != nil || u != "athens_read" {
		t.Fatalf("after restart: %q %v", u, err)
	}
	m.CloseAll()
	if u, err := q(m, tg, "postgres", authz.Read, "SELECT session_user"); err != nil || u != "athens_read" {
		t.Fatalf("stale password not recovered: %q %v", u, err)
	}

	// an RW login that may not create roles: setup fails, writers carry on
	admin, err := pg16.Connect(ctx, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	if _, err := admin.Exec(ctx, "CREATE ROLE zweak LOGIN PASSWORD 'zweak-pw'; GRANT CONNECT ON DATABASE app TO zweak"); err != nil {
		t.Fatal(err)
	}
	weak, weakClient := pg16.Target("provision-weak")
	if _, err := weakClient.CoreV1().Secrets(pgtest.Namespace).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "zweak", Namespace: pgtest.Namespace},
		Data: map[string][]byte{"username": []byte("zweak"), "password": []byte("zweak-pw")}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	weak.RW = discovery.CredentialRef{
		User:     discovery.ValueSource{Secret: &discovery.KeyRef{Name: "zweak", Key: "username"}},
		Password: discovery.ValueSource{Secret: &discovery.KeyRef{Name: "zweak", Key: "password"}},
	}
	weak.RO, weak.ROProvision = nil, true
	m3 := pg16.Manager(weak, weakClient, pgtest.Limits())
	defer m3.CloseAll()
	if _, err := q(m3, weak, "app", authz.Read, "SELECT 1::text"); !errors.Is(err, athenspg.ErrReadLoginUnavailable) {
		t.Fatalf("weak RW login: %v", err)
	}
	if msg := m3.ProvisionError(weak.ID); !strings.Contains(msg, "may not create or update") {
		t.Fatalf("ProvisionError = %q", msg)
	}
	if _, err := q(m3, weak, "app", authz.Write, "SELECT 1::text"); err != nil || m3.Status(weak.ID) != "ok" {
		t.Fatalf("writers on the weak login: %v %s", err, m3.Status(weak.ID))
	}

	// a role named athens_read that athens did not create is left alone
	if _, err := admin.Exec(ctx, "COMMENT ON ROLE athens_read IS 'someone else'"); err != nil {
		t.Fatal(err)
	}
	m4 := pg16.Manager(tg, client, pgtest.Limits())
	defer m4.CloseAll()
	if _, err := q(m4, tg, "app", authz.Read, "SELECT 1::text"); !errors.Is(err, athenspg.ErrReadLoginUnavailable) {
		t.Fatalf("unmanaged role: %v", err)
	}
	if msg := m4.ProvisionError(tg.ID); !strings.Contains(msg, "not managed by athens") {
		t.Fatalf("ProvisionError = %q", msg)
	}
	if s := m4.Status(tg.ID); s == "unreachable" {
		t.Fatalf("status %s", s)
	}
	var comment string
	if err := admin.QueryRow(ctx, "SELECT shobj_description(oid, 'pg_authid') FROM pg_roles WHERE rolname = 'athens_read'").Scan(&comment); err != nil || comment != "someone else" {
		t.Fatalf("unmanaged role touched: %q %v", comment, err)
	}

	// a main login that is not a superuser but may create roles (managed
	// services): the first start creates the role, a restart only resets
	// the password
	for _, sql := range []string{
		"DROP ROLE athens_read",
		"CREATE ROLE zcr LOGIN CREATEROLE PASSWORD 'zcr-pw'",
		"GRANT pg_read_all_data TO zcr WITH ADMIN OPTION",
		"GRANT CONNECT ON DATABASE app TO zcr",
	} {
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	cr, crClient := pg16.Target("provision-cr")
	if _, err := crClient.CoreV1().Secrets(pgtest.Namespace).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "zcr", Namespace: pgtest.Namespace},
		Data: map[string][]byte{"username": []byte("zcr"), "password": []byte("zcr-pw")}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	cr.RW = discovery.CredentialRef{
		User:     discovery.ValueSource{Secret: &discovery.KeyRef{Name: "zcr", Key: "username"}},
		Password: discovery.ValueSource{Secret: &discovery.KeyRef{Name: "zcr", Key: "password"}},
	}
	cr.RO, cr.ROProvision = nil, true
	for i := range 2 {
		mc := pg16.Manager(cr, crClient, pgtest.Limits())
		u, err := q(mc, cr, "app", authz.Read, "SELECT session_user")
		mc.CloseAll()
		if err != nil || u != "athens_read" {
			t.Fatalf("CREATEROLE main login, start %d: %q %v (%s)", i+1, u, err, mc.ProvisionError(cr.ID))
		}
	}

	// extra rights granted to the managed role make athens stop using it
	if _, err := admin.Exec(ctx, "GRANT pg_write_all_data TO athens_read"); err != nil {
		t.Fatal(err)
	}
	m6 := pg16.Manager(tg, client, pgtest.Limits())
	defer m6.CloseAll()
	if _, err := q(m6, tg, "app", authz.Read, "SELECT 1::text"); !errors.Is(err, athenspg.ErrReadLoginUnavailable) {
		t.Fatalf("role with extra membership: %v", err)
	}
	if msg := m6.ProvisionError(tg.ID); !strings.Contains(msg, "beyond LOGIN and pg_read_all_data") {
		t.Fatalf("ProvisionError = %q", msg)
	}

	// PostgreSQL 13 has no pg_read_all_data
	pg13 := instances[1]
	tg13, client13 := pg13.Target("provision13")
	tg13.RO, tg13.ROProvision = nil, true
	m5 := pg13.Manager(tg13, client13, pgtest.Limits())
	defer m5.CloseAll()
	if _, err := q(m5, tg13, "app", authz.Read, "SELECT 1::text"); !errors.Is(err, athenspg.ErrReadLoginUnavailable) {
		t.Fatalf("pg13: %v", err)
	}
	if msg := m5.ProvisionError(tg13.ID); !strings.Contains(msg, "PostgreSQL 14") {
		t.Fatalf("pg13 ProvisionError = %q", msg)
	}
}

// TestDatabaseScopedErrors (ADR-027): unknown database names never create a
// pool, and a database the login may not connect to is that database's error.
func TestDatabaseScopedErrors(t *testing.T) {
	in := instances[0]
	tg, client := in.Target("dbscoped")
	m := in.Manager(tg, client, pgtest.Limits())
	defer m.CloseAll()
	ctx := context.Background()
	noop := func(context.Context, pgx.Tx) error { return nil }

	// the read-only login (not a superuser) may not connect to "locked"
	admin, err := in.Connect(ctx, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	for _, sql := range []string{"DROP DATABASE IF EXISTS locked", "CREATE DATABASE locked", "REVOKE CONNECT ON DATABASE locked FROM PUBLIC"} {
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}

	for i := range 40 {
		if err := m.Run(ctx, pgtest.Req(tg, fmt.Sprintf("random_%d", i), authz.Read), noop); !errors.Is(err, athenspg.ErrNoDatabase) {
			t.Fatalf("random database %d: %v", i, err)
		}
	}
	for _, st := range m.Stats() {
		if strings.HasPrefix(st.Database, "random_") {
			t.Fatalf("pool created for an unknown database: %+v", st)
		}
	}

	err = m.Run(ctx, pgtest.Req(tg, "locked", authz.Read), noop) // read users use the RO login
	if !errors.Is(err, athenspg.ErrDatabaseDenied) {
		t.Fatalf("no CONNECT privilege: %v", err)
	}
	if s := m.Status(tg.ID); s == "unreachable" {
		t.Fatalf("one database's refusal marked the target %s", s)
	}
}

// TestPrivilegedROLogins (ADR-027, ADR-028): a read-only login that is a
// member of a superuser or monitoring role is refused for read users;
// unprivileged logins keep working, and a later GRANT is seen at once.
func TestPrivilegedROLogins(t *testing.T) {
	in := instances[0]
	ctx := context.Background()
	admin, err := in.Connect(ctx, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	for _, sql := range []string{
		"CREATE ROLE zmember LOGIN PASSWORD 'zmember-pw' IN ROLE postgres",
		"CREATE ROLE zmon LOGIN PASSWORD 'zmon-pw' IN ROLE pg_monitor",
		"CREATE ROLE zplain LOGIN PASSWORD 'zplain-pw'",
	} {
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	login := func(user string) (*athenspg.Manager, discovery.Target) {
		tg, client := in.Target("priv-" + user)
		sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: user, Namespace: pgtest.Namespace},
			Data: map[string][]byte{"username": []byte(user), "password": []byte(user + "-pw")}}
		if _, err := client.CoreV1().Secrets(pgtest.Namespace).Create(ctx, sec, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		tg.RO = &discovery.CredentialRef{
			User:     discovery.ValueSource{Secret: &discovery.KeyRef{Name: user, Key: "username"}},
			Password: discovery.ValueSource{Secret: &discovery.KeyRef{Name: user, Key: "password"}},
		}
		return in.Manager(tg, client, pgtest.Limits()), tg
	}
	run := func(m *athenspg.Manager, tg discovery.Target) error {
		return m.Run(ctx, pgtest.Req(tg, "app", authz.Read), func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "SELECT 1")
			return err
		})
	}
	for _, user := range []string{"zmember", "zmon"} {
		m, tg := login(user)
		if err := run(m, tg); !errors.Is(err, athenspg.ErrReadLoginPrivileged) {
			t.Errorf("%s: %v", user, err)
		}
		if !m.PrivilegedRO(tg.ID) {
			t.Errorf("%s not reported as privileged", user)
		}
		m.CloseAll()
	}
	m, tg := login("zplain")
	defer m.CloseAll()
	if err := run(m, tg); err != nil {
		t.Fatalf("unprivileged login: %v", err)
	}
	if m.PrivilegedRO(tg.ID) {
		t.Fatal("zplain reported as privileged")
	}
	// a privilege granted later is seen by the next transaction
	if _, err := admin.Exec(ctx, "GRANT postgres TO zplain"); err != nil {
		t.Fatal(err)
	}
	if err := run(m, tg); !errors.Is(err, athenspg.ErrReadLoginPrivileged) {
		t.Fatalf("after GRANT postgres: %v", err)
	}
}
