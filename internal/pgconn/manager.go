// Package pgconn manages pgx pools per (target, database, credential kind)
// and runs every operation inside a guarded transaction (SPEC §6.2–6.4).
package pgconn

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Warewave-Technology/athens/internal/authz"
	"github.com/Warewave-Technology/athens/internal/config"
	"github.com/Warewave-Technology/athens/internal/credentials"
	"github.com/Warewave-Technology/athens/internal/discovery"
	"github.com/Warewave-Technology/athens/internal/metrics"
	"github.com/Warewave-Technology/athens/internal/version"
)

type CredKind string

const (
	RW CredKind = "rw"
	RO CredKind = "ro"
)

type Key struct {
	TargetID string
	Database string
	Kind     CredKind
}

type Targets interface {
	Get(id string) (discovery.Target, bool)
}

type Resolver interface {
	Resolve(ctx context.Context, namespace string, ref discovery.CredentialRef) (credentials.Credentials, error)
	DefaultDB(ctx context.Context, t discovery.Target) string
	CA(ctx context.Context, namespace string, ref *discovery.KeyRef) ([]byte, error)
}

type Options struct {
	Targets  Targets
	Resolver Resolver
	Limits   config.Limits
	Log      *slog.Logger
	// IdleClose closes pools unused for this long (default 15m).
	IdleClose time.Duration
	// ConnectTimeout per connection attempt (default 5s).
	ConnectTimeout time.Duration
	// DialOverrides redirects "host:port" to another address (local
	// development only, server.devHostOverrides).
	DialOverrides map[string]string
}

type pool struct {
	key      Key
	db       string // database the pool connects to (resolved for Database "")
	p        *pgxpool.Pool
	created  time.Time
	mu       sync.Mutex
	lastUsed time.Time
}

func (p *pool) touch() {
	p.mu.Lock()
	p.lastUsed = time.Now()
	p.mu.Unlock()
}

func (p *pool) idleSince() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastUsed
}

type targetState struct {
	status    string // ok | unreachable | unknown
	checked   time.Time
	lastError string
	roFailed  bool
	// missingDB / fallbackDB: the guessed initial database does not exist and
	// the default-database pool uses fallbackDB instead.
	missingDB, fallbackDB string
	// roleChecks: per credential kind, the read-only role read users switch
	// to and whether the login is privileged (ADR-026, ADR-027).
	roleChecks map[CredKind]roleCheck
	// dbs: databases of the target (cached at dbsAt, ADR-027).
	dbs     map[string]bool
	dbsAt   time.Time
	running map[*runHandle]struct{}
}

type runHandle struct{ cancel context.CancelFunc }

type Manager struct {
	opt     Options
	notices sync.Map // *pgconn.PgConn -> *noticeSink

	mu     sync.Mutex
	pools  map[Key]*pool
	states map[string]*targetState
}

func NewManager(o Options) *Manager {
	if o.IdleClose == 0 {
		o.IdleClose = 15 * time.Minute
	}
	if o.ConnectTimeout == 0 {
		o.ConnectTimeout = 5 * time.Second
	}
	return &Manager{opt: o, pools: map[Key]*pool{}, states: map[string]*targetState{}}
}

func (m *Manager) state(id string) *targetState {
	s, ok := m.states[id]
	if !ok {
		s = &targetState{status: "unknown", running: map[*runHandle]struct{}{}}
		m.states[id] = s
	}
	return s
}

// Request describes one guarded operation.
type Request struct {
	Target   discovery.Target
	Database string
	Level    authz.Level
	// Email is used for application_name ("athens:<email>").
	Email string
	// ReadOnly forces a READ ONLY transaction even for write-level users
	// (catalog, grid reads).
	ReadOnly bool
	// Reset runs DISCARD ALL after the transaction (SQL editor, ADR-011).
	Reset bool
	// UserSQL marks SQL written by the user (SQL editor). Read users may not
	// run it on a privileged login (ADR-027).
	UserSQL bool
	// UserData marks reads of table rows (grid). Read users may not run them
	// on a privileged login without a usable read-only role (ADR-027).
	UserData bool
	// Notices receives NOTICE/WARNING messages raised during fn.
	Notices *[]Notice
}

type Notice struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type noticeSink struct {
	mu  sync.Mutex
	out *[]Notice
}

// Run executes fn in a transaction prepared per SPEC §6.3.
func (m *Manager) Run(ctx context.Context, req Request, fn func(ctx context.Context, tx pgx.Tx) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	h := &runHandle{cancel: cancel}
	m.mu.Lock()
	st := m.state(req.Target.ID)
	st.running[h] = struct{}{}
	useRO := req.Level == authz.Read && req.Target.RO != nil && !st.roFailed
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.state(req.Target.ID).running, h)
		m.mu.Unlock()
	}()

	kind := RW
	if useRO {
		kind = RO
	}
	if req.Database != "" {
		ok, err := m.databaseExists(ctx, req.Target, req.Database)
		if err != nil {
			if ctx.Err() != nil {
				return ErrCancelled
			}
			return err
		}
		if !ok {
			return ErrNoDatabase
		}
	}
	key := Key{TargetID: req.Target.ID, Database: req.Database, Kind: kind}
	conn, err := m.acquire(ctx, req.Target, key)
	if err != nil && kind == RO && isAuthError(err) {
		m.log().Warn("read-only credential rejected, falling back to rw + READ ONLY", "target", req.Target.ID)
		m.mu.Lock()
		m.state(req.Target.ID).roFailed = true
		m.mu.Unlock()
		key.Kind = RW
		conn, err = m.acquire(ctx, req.Target, key)
	}
	if err != nil {
		if ctx.Err() != nil {
			return ErrCancelled
		}
		return err
	}
	// Read users drop to a read-only role and are checked for a privileged
	// login, on either credential (ADR-026, ADR-027).
	dropRole := req.Level == authz.Read
	err = m.runOn(ctx, conn, req, dropRole, key.Kind, fn)
	if errors.Is(err, errStaleConn) {
		// The pooled connections died underneath us (server or network
		// restart). Nothing reached the server, so drop the pool's idle
		// connections and try once more on a fresh one.
		m.log().Info("stale pooled connection, reconnecting", "target", req.Target.ID)
		m.resetPool(key)
		if conn, err = m.acquire(ctx, req.Target, key); err != nil {
			return err
		}
		err = m.runOn(ctx, conn, req, dropRole, key.Kind, fn)
	}
	if errors.Is(err, errStaleConn) {
		return &ErrUnavailable{Cause: err}
	}
	return err
}

// staleBegin reports whether BEGIN failed because the connection itself is
// gone. Retrying is safe: a failed BEGIN leaves nothing behind on the server.
func staleBegin(conn *pgxpool.Conn, err error) bool {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		// admin_shutdown, crash_shutdown, cannot_connect_now
		return pe.Code == "57P01" || pe.Code == "57P02" || pe.Code == "57P03"
	}
	return pgconn.SafeToRetry(err) || conn.Conn().IsClosed()
}

// errStaleConn marks a connection that failed before the transaction began.
var errStaleConn = errors.New("stale pooled connection")

func (m *Manager) resetPool(key Key) {
	m.mu.Lock()
	p, ok := m.pools[key]
	m.mu.Unlock()
	if ok {
		p.p.Reset()
	}
}

func (m *Manager) log() *slog.Logger { return m.opt.Log }

func (m *Manager) runOn(ctx context.Context, conn *pgxpool.Conn, req Request, dropRole bool, kind CredKind, fn func(context.Context, pgx.Tx) error) (err error) {
	defer func() {
		if req.Reset {
			// User SQL may have changed session state; never hand it to the
			// next user (ADR-011). Use a fresh context: ctx may be cancelled.
			rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if _, rerr := conn.Exec(rctx, "DISCARD ALL"); rerr != nil {
				_ = conn.Conn().Close(rctx)
			}
			cancel()
		}
		conn.Release()
	}()
	if req.Notices != nil {
		pc := conn.Conn().PgConn()
		m.notices.Store(pc, &noticeSink{out: req.Notices})
		defer m.notices.Delete(pc)
	}

	opts := pgx.TxOptions{}
	if req.Level != authz.Write || req.ReadOnly {
		opts.AccessMode = pgx.ReadOnly
	}
	tx, err := conn.BeginTx(ctx, opts)
	if err != nil {
		if ctx.Err() == nil && staleBegin(conn, err) {
			_ = conn.Conn().Close(context.WithoutCancel(ctx)) // never return it to the pool
			return errStaleConn
		}
		return classify(ctx, err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	l := m.opt.Limits
	// standard_conforming_strings: the server must tokenize string literals
	// like the classifier's parser does.
	if _, err = tx.Exec(ctx, `SELECT set_config('standard_conforming_strings', 'on', true), set_config('application_name', $1, true), set_config('statement_timeout', $2, true),
		set_config('lock_timeout', $3, true), set_config('idle_in_transaction_session_timeout', $4, true)`,
		AppName(req.Email), ms(l.StatementTimeout.D()), ms(l.LockTimeout.D()), ms(2*l.StatementTimeout.D())); err != nil {
		return classify(ctx, err)
	}
	if dropRole {
		rc, err := m.setReadRole(ctx, tx, req.Target, kind)
		if err != nil {
			return classify(ctx, err)
		}
		// On a privileged login a role switch is no boundary for user SQL,
		// and without one even athens-generated reads would run with the
		// login's privileges (settings, other sessions): refuse both.
		if rc.privileged && (req.UserSQL || (req.UserData && rc.role == "")) {
			return ErrReadLoginRequired
		}
	}
	if err = fn(ctx, tx); err != nil {
		return classify(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return classify(ctx, err)
	}
	return nil
}

// defaultReadRole is the predefined role (PostgreSQL 14+) that may read every
// table, view and sequence but not run superuser-only functions.
const defaultReadRole = "pg_read_all_data"

// roleCheck is the cached result of checking one login of a target.
type roleCheck struct {
	role       string // usable read-only role, "" if none
	privileged bool   // the login has privileges a role switch cannot contain
}

// privilegedLogin: the login is, or is a member of, a role that is superuser,
// REPLICATION or BYPASSRLS, or of a predefined role that reads server files,
// settings or other sessions, or signals backends. Through any function that
// runs SQL a user can SET ROLE back to such a login (ADR-027).
const privilegedLogin = `SELECT EXISTS (SELECT 1 FROM pg_roles r
	WHERE (r.rolsuper OR r.rolreplication OR r.rolbypassrls OR r.rolname IN ('pg_read_server_files', 'pg_write_server_files',
		'pg_execute_server_program', 'pg_signal_backend', 'pg_read_all_settings', 'pg_read_all_stats', 'pg_monitor'))
	AND pg_has_role(session_user, r.oid, 'MEMBER'))`

// setReadRole switches a read user's transaction to the target's read-only
// role with SET LOCAL ROLE (reset at commit; RESET/SET ROLE are rejected by
// sqlclass) and reports what it knows about the login. Whether the login is
// privileged is checked in every transaction (a role can be granted at any
// time); whether it may switch to the role is checked inside a savepoint once
// per target and credential kind and cached until a pool of that kind is
// created again.
func (m *Manager) setReadRole(ctx context.Context, tx pgx.Tx, t discovery.Target, kind CredKind) (roleCheck, error) {
	role := t.ReadOnlyRole
	if role == "" {
		role = defaultReadRole
	}
	set := "SET LOCAL ROLE " + pgx.Identifier{role}.Sanitize()
	var privileged bool
	if err := tx.QueryRow(ctx, privilegedLogin).Scan(&privileged); err != nil {
		return roleCheck{}, err
	}
	m.mu.Lock()
	st := m.state(t.ID)
	rc, checked := st.roleChecks[kind]
	if checked && rc.privileged != privileged {
		rc.privileged = privileged
		st.roleChecks[kind] = rc
	}
	m.mu.Unlock()
	if checked {
		if rc.role == "" {
			return rc, nil
		}
		if _, err := tx.Exec(ctx, set); err != nil {
			m.mu.Lock()
			delete(m.state(t.ID).roleChecks, kind) // role dropped or revoked: check again next time
			m.mu.Unlock()
			return rc, err
		}
		return rc, nil
	}

	ok := false
	if _, err := tx.Exec(ctx, "SAVEPOINT athens_role"); err != nil {
		return rc, err
	}
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_roles r WHERE r.rolname = $1 AND pg_has_role(current_user, r.oid, 'MEMBER')`, role).Scan(&n)
	if err == nil && n == 1 {
		if _, err = tx.Exec(ctx, set); err == nil {
			ok = true
		}
	}
	if !ok {
		if ctx.Err() != nil {
			return rc, ctx.Err()
		}
		if _, rerr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT athens_role"); rerr != nil {
			return rc, rerr
		}
		if kind == RW {
			m.log().Warn("no usable read-only role; read users run on the rw login in READ ONLY transactions",
				"target", t.ID, "role", role, "privileged", privileged)
		}
	} else if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT athens_role"); err != nil {
		return rc, err
	}
	rc = roleCheck{privileged: privileged}
	if ok {
		rc.role = role
	}
	m.mu.Lock()
	st = m.state(t.ID)
	if st.roleChecks == nil {
		st.roleChecks = map[CredKind]roleCheck{}
	}
	st.roleChecks[kind] = rc
	m.mu.Unlock()
	return rc, nil
}

// PrivilegedRW reports, after the first read on the RW login, that the login
// is privileged, so read users cannot use the SQL editor there (ADR-027).
func (m *Manager) PrivilegedRW(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.states[id]
	return ok && s.roleChecks[RW].privileged
}

// ReadRole reports the read-only role of a target after the first read on its
// RW login: checked is false before that; role is "" when none is usable.
func (m *Manager) ReadRole(id string) (role string, checked bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.states[id]; ok {
		rc, checked := s.roleChecks[RW]
		return rc.role, checked
	}
	return "", false
}

func ms(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }

// AppName is "athens:<email>" truncated to 63 bytes (NAMEDATALEN-1).
func AppName(email string) string {
	s := version.Name + ":" + email
	if len(s) > 63 {
		s = s[:63]
	}
	return s
}

// acquire gets a connection, creating the pool lazily. On authentication
// failure the credentials are re-resolved and the pool recreated once.
func (m *Manager) acquire(ctx context.Context, t discovery.Target, key Key) (*pgxpool.Conn, error) {
	var p *pool
	var conn *pgxpool.Conn
	var err error
	for range 2 {
		if p, err = m.pool(ctx, t, key, false); err != nil {
			return nil, err
		}
		conn, err = p.p.Acquire(ctx)
		if err == nil || !m.evicted(key, p) {
			break
		}
		// evicted between lookup and Acquire (makeRoom): take a fresh pool
	}
	if err != nil && isAuthError(err) {
		m.log().Info("authentication failed, re-resolving credentials", "target", t.ID, "kind", key.Kind)
		if p, err = m.pool(ctx, t, key, true); err != nil {
			return nil, err
		}
		conn, err = p.p.Acquire(ctx)
	}
	if err != nil && key.Database == "" && isMissingDatabase(err) {
		conn, p, err = m.acquireFallbackDB(ctx, t, key, p.db, err)
	}
	if err != nil && key.Database != "" {
		// The server refused this one database (it was dropped, or the login
		// lacks CONNECT on it): that is the database's error, not the
		// target's. Drop the pool and leave the target status alone.
		switch {
		case isMissingDatabase(err):
			m.dropPool(key)
			return nil, ErrNoDatabase
		case isNoConnectPrivilege(err):
			m.dropPool(key)
			return nil, ErrDatabaseDenied
		}
	}
	if err != nil {
		m.recordError(t.ID, err)
		return nil, &ErrUnavailable{Cause: err}
	}
	p.touch()
	m.recordOK(t.ID)
	return conn, nil
}

// evicted reports that p is no longer the pool for key.
func (m *Manager) evicted(key Key, p *pool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pools[key] != p
}

// dbListTTL bounds how stale the cached database list of a target may be;
// an unknown name refreshes it at most every dbListRefresh.
const (
	dbListTTL     = time.Minute
	dbListRefresh = 5 * time.Second
)

// databaseExists checks name against the target's databases (cached), so a
// database name from a URL never creates a pool unless it exists (ADR-027).
func (m *Manager) databaseExists(ctx context.Context, t discovery.Target, name string) (bool, error) {
	m.mu.Lock()
	st := m.state(t.ID)
	known, at := st.dbs[name], st.dbsAt
	m.mu.Unlock()
	age := time.Since(at)
	if (known && age < dbListTTL) || (!known && age < dbListRefresh) {
		return known, nil
	}
	conn, err := m.acquire(ctx, t, Key{TargetID: t.ID, Kind: RW})
	if err != nil {
		return false, err
	}
	defer conn.Release()
	rows, err := conn.Query(ctx, "SELECT datname FROM pg_database WHERE datallowconn")
	if err != nil {
		return false, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return false, err
	}
	dbs := make(map[string]bool, len(names))
	for _, n := range names {
		dbs[n] = true
	}
	m.mu.Lock()
	st = m.state(t.ID)
	st.dbs, st.dbsAt = dbs, time.Now()
	m.mu.Unlock()
	return dbs[name], nil
}

// fallbackDBs are tried, in order, when the initial database of a target does
// not exist (DISCOVERY §4.4 guesses it from env or the user name).
var fallbackDBs = []string{"postgres", "template1"}

// acquireFallbackDB recreates the default-database pool of t on the first
// fallback database that exists and remembers it until the target changes.
func (m *Manager) acquireFallbackDB(ctx context.Context, t discovery.Target, key Key, missing string, cause error) (*pgxpool.Conn, *pool, error) {
	for _, db := range fallbackDBs {
		if db == missing {
			continue
		}
		m.mu.Lock()
		st := m.state(t.ID)
		st.missingDB, st.fallbackDB = missing, db
		m.mu.Unlock()
		p, err := m.pool(ctx, t, key, true)
		if err != nil {
			return nil, nil, err
		}
		conn, err := p.p.Acquire(ctx)
		if err == nil {
			m.log().Warn("initial database does not exist, using a fallback", "target", t.ID, "missing", missing, "using", db)
			return conn, p, nil
		}
		if !isMissingDatabase(err) {
			return nil, nil, err
		}
	}
	m.mu.Lock()
	st := m.state(t.ID)
	st.missingDB, st.fallbackDB = "", ""
	m.mu.Unlock()
	return nil, nil, cause
}

func (m *Manager) recordError(id string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.state(id)
	s.status, s.lastError, s.checked = "unreachable", err.Error(), time.Now()
	m.log().Warn("target connect failed", "target", id, "err", err)
}

func (m *Manager) recordOK(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.state(id)
	s.status, s.checked = "ok", time.Now()
}

// maxPoolsPerTarget bounds the pools one target can hold (one per database
// and credential kind); the least recently used idle pool makes room.
const maxPoolsPerTarget = 32

var errTooManyPools = errors.New("too many databases of this server are in use at once")

func (m *Manager) dropPool(key Key) {
	m.mu.Lock()
	p, ok := m.pools[key]
	delete(m.pools, key)
	m.mu.Unlock()
	if ok {
		go p.p.Close()
	}
}

// makeRoom evicts the least recently used idle pool of target id when it
// holds maxPoolsPerTarget pools. Called with m.mu held.
func (m *Manager) makeRoom(id string) error {
	var n int
	var lru *pool
	for k, p := range m.pools {
		if k.TargetID != id {
			continue
		}
		n++
		if k.Database != "" && p.p.Stat().AcquiredConns() == 0 && (lru == nil || p.idleSince().Before(lru.idleSince())) {
			lru = p
		}
	}
	if n < maxPoolsPerTarget {
		return nil
	}
	if lru == nil {
		return errTooManyPools
	}
	delete(m.pools, lru.key)
	go lru.p.Close()
	return nil
}

func (m *Manager) pool(ctx context.Context, t discovery.Target, key Key, recreate bool) (*pool, error) {
	m.mu.Lock()
	p, ok := m.pools[key]
	if ok && recreate {
		delete(m.pools, key)
		go p.p.Close()
		ok = false
	}
	var full error
	if !ok {
		full = m.makeRoom(t.ID)
	}
	m.mu.Unlock()
	if ok {
		return p, nil
	}
	if full != nil {
		return nil, &ErrUnavailable{Cause: full}
	}
	cfg, err := m.poolConfig(ctx, t, key)
	if err != nil {
		metrics.PoolCreate.WithLabelValues(t.ID, "error").Inc()
		m.recordError(t.ID, err)
		return nil, &ErrUnavailable{Cause: err}
	}
	np, err := pgxpool.NewWithConfig(context.WithoutCancel(ctx), cfg)
	if err != nil {
		metrics.PoolCreate.WithLabelValues(t.ID, "error").Inc()
		return nil, &ErrUnavailable{Cause: err}
	}
	metrics.PoolCreate.WithLabelValues(t.ID, "ok").Inc()
	created := &pool{key: key, db: cfg.ConnConfig.Database, p: np, created: time.Now(), lastUsed: time.Now()}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.pools[key]; ok { // lost a creation race
		np.Close()
		return existing, nil
	}
	m.pools[key] = created
	if st, ok := m.states[t.ID]; ok {
		delete(st.roleChecks, key.Kind) // credentials may have changed: check the login again
	}
	return created, nil
}

func (m *Manager) poolConfig(ctx context.Context, t discovery.Target, key Key) (*pgxpool.Config, error) {
	ref := t.RW
	if key.Kind == RO {
		ref = *t.RO
	}
	creds, err := m.opt.Resolver.Resolve(ctx, t.Namespace, ref)
	if err != nil {
		return nil, fmt.Errorf("resolve %s credentials: %w", key.Kind, err)
	}
	db := key.Database
	if db == "" {
		m.mu.Lock()
		db = m.state(t.ID).fallbackDB
		m.mu.Unlock()
		if db == "" {
			db = m.opt.Resolver.DefaultDB(ctx, t)
		}
	}
	mode := t.SSLMode
	if mode == "" {
		mode = "prefer"
	}
	var ca []byte
	if t.CA != nil && (mode == "verify-ca" || mode == "verify-full") {
		if ca, err = m.opt.Resolver.CA(ctx, t.Namespace, t.CA); err != nil {
			return nil, fmt.Errorf("resolve CA: %w", err)
		}
	}
	parseMode := mode
	if len(ca) > 0 {
		parseMode = "require" // TLS config is built below with the custom CA
	}
	dsn := (&url.URL{
		Scheme: "postgres", Host: net.JoinHostPort(t.Host, strconv.Itoa(t.Port)), Path: "/" + db,
		RawQuery: url.Values{"sslmode": {parseMode}, "connect_timeout": {strconv.Itoa(int(m.opt.ConnectTimeout.Seconds()))}}.Encode(),
	}).String()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cc := cfg.ConnConfig
	cc.User, cc.Password = creds.User, creds.Password
	cc.DefaultQueryExecMode = pgx.QueryExecModeExec // ADR-011
	cc.RuntimeParams["application_name"] = version.Name
	if len(ca) > 0 {
		tc, err := tlsWithCA(ca, t.Host, mode == "verify-full")
		if err != nil {
			return nil, err
		}
		cc.TLSConfig, cc.Fallbacks = tc, nil
	}
	if len(m.opt.DialOverrides) > 0 {
		d := &net.Dialer{Timeout: m.opt.ConnectTimeout}
		lookup := cc.LookupFunc
		cc.LookupFunc = func(ctx context.Context, host string) ([]string, error) {
			for k := range m.opt.DialOverrides {
				if h, _, _ := net.SplitHostPort(k); h == host {
					return []string{host}, nil // resolved by DialFunc
				}
			}
			return lookup(ctx, host)
		}
		cc.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if to, ok := m.opt.DialOverrides[addr]; ok {
				addr = to
			}
			return d.DialContext(ctx, network, addr)
		}
	}
	cc.OnNotice = func(pc *pgconn.PgConn, n *pgconn.Notice) {
		if v, ok := m.notices.Load(pc); ok {
			s := v.(*noticeSink)
			s.mu.Lock()
			*s.out = append(*s.out, Notice{Severity: n.Severity, Message: n.Message})
			s.mu.Unlock()
		}
	}
	cfg.MaxConns = int32(min(m.opt.Limits.PoolMaxConns, 1000)) // #nosec G115 -- bounded
	cfg.MinConns = 0
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = time.Minute
	return cfg, nil
}

func tlsWithCA(ca []byte, host string, verifyHost bool) (*tls.Config, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("CA secret contains no PEM certificates")
	}
	tc := &tls.Config{RootCAs: roots, ServerName: host, MinVersion: tls.VersionTLS12}
	if !verifyHost {
		// verify-ca: check the chain but not the host name.
		tc.InsecureSkipVerify = true // #nosec G402 -- chain verified in VerifyConnection
		// VerifyConnection (unlike VerifyPeerCertificate) also runs on
		// resumed sessions.
		tc.VerifyConnection = func(cs tls.ConnectionState) error {
			certs := cs.PeerCertificates
			if len(certs) == 0 {
				return errors.New("no server certificate")
			}
			inter := x509.NewCertPool()
			for _, c := range certs[1:] {
				inter.AddCert(c)
			}
			_, err := certs[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter})
			return err
		}
	}
	return tc, nil
}

// TargetChanged closes pools (and cancels running operations when the
// target is gone). Hook it to discovery.Registry.OnChange.
func (m *Manager) TargetChanged(e discovery.Event) {
	if e.Type == discovery.Added {
		return
	}
	m.mu.Lock()
	var closing []*pool
	for k, p := range m.pools {
		if k.TargetID == e.Target.ID {
			closing = append(closing, p)
			delete(m.pools, k)
		}
	}
	if s, ok := m.states[e.Target.ID]; ok {
		if e.Type == discovery.Removed {
			for h := range s.running {
				h.cancel()
			}
			delete(m.states, e.Target.ID)
		} else {
			s.roFailed = false
			s.missingDB, s.fallbackDB = "", ""
			s.roleChecks = nil
			s.dbs, s.dbsAt = nil, time.Time{}
		}
	}
	m.mu.Unlock()
	for _, p := range closing {
		go p.p.Close() // waits for in-flight connections to be released
	}
}

// Status is "ok", "unreachable" or "unknown" (no open pool yet).
func (m *Manager) Status(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.states[id]; ok {
		return s.status
	}
	return "unknown"
}

// ROFallback reports whether the read-only credential was rejected.
func (m *Manager) ROFallback(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.states[id]
	return ok && s.roFailed
}

// DefaultDBFallback reports the missing initial database of a target and the
// fallback used instead ("" when none).
func (m *Manager) DefaultDBFallback(id string) (missing, using string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.states[id]; ok {
		return s.missingDB, s.fallbackDB
	}
	return "", ""
}

type PoolStat struct {
	TargetID  string    `json:"targetId"`
	Database  string    `json:"database"`
	Kind      CredKind  `json:"kind"`
	Total     int32     `json:"total"`
	Idle      int32     `json:"idle"`
	Acquired  int32     `json:"acquired"`
	Created   time.Time `json:"created"`
	LastUsed  time.Time `json:"lastUsed"`
	Status    string    `json:"status"`
	LastError string    `json:"lastError,omitempty"`
}

// Stats lists every open pool (admin page).
func (m *Manager) Stats() []PoolStat {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PoolStat, 0, len(m.pools))
	for k, p := range m.pools {
		st := p.p.Stat()
		s := m.state(k.TargetID)
		out = append(out, PoolStat{TargetID: k.TargetID, Database: k.Database, Kind: k.Kind, Total: st.TotalConns(),
			Idle: st.IdleConns(), Acquired: st.AcquiredConns(), Created: p.created, LastUsed: p.idleSince(),
			Status: s.status, LastError: s.lastError})
	}
	slices.SortFunc(out, func(a, b PoolStat) int {
		if a.TargetID != b.TargetID {
			return compare(a.TargetID, b.TargetID)
		}
		return compare(a.Database+string(a.Kind), b.Database+string(b.Kind))
	})
	return out
}

func compare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Maintain closes idle pools, updates pool metrics and health-checks
// targets with an open pool every minute, until ctx ends.
func (m *Manager) Maintain(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	lastCheck := time.Now()
	for {
		select {
		case <-ctx.Done():
			m.CloseAll()
			return
		case <-t.C:
		}
		m.closeIdle()
		m.updateMetrics()
		if time.Since(lastCheck) >= time.Minute {
			lastCheck = time.Now()
			m.healthCheck(ctx)
		}
	}
}

func (m *Manager) closeIdle() {
	m.mu.Lock()
	var closing []*pool
	for k, p := range m.pools {
		if time.Since(p.idleSince()) > m.opt.IdleClose && p.p.Stat().AcquiredConns() == 0 {
			closing = append(closing, p)
			delete(m.pools, k)
		}
	}
	m.mu.Unlock()
	for _, p := range closing {
		p.p.Close()
	}
}

func (m *Manager) updateMetrics() {
	type agg struct{ acquired, idle int32 }
	byTarget := map[string]*agg{}
	m.mu.Lock()
	for k, p := range m.pools {
		a := byTarget[k.TargetID]
		if a == nil {
			a = &agg{}
			byTarget[k.TargetID] = a
		}
		st := p.p.Stat()
		a.acquired += st.AcquiredConns()
		a.idle += st.IdleConns()
	}
	m.mu.Unlock()
	metrics.PoolConns.Reset()
	for id, a := range byTarget {
		metrics.PoolConns.WithLabelValues(id, "acquired").Set(float64(a.acquired))
		metrics.PoolConns.WithLabelValues(id, "idle").Set(float64(a.idle))
	}
}

// healthCheck runs SELECT 1 on one pool per target that has an open pool.
func (m *Manager) healthCheck(ctx context.Context) {
	m.mu.Lock()
	byTarget := map[string]*pool{}
	for k, p := range m.pools {
		// prefer the default-database pool: it reflects the target, not
		// whichever database a user opened
		if cur, ok := byTarget[k.TargetID]; !ok || (k.Database == "" && cur.key.Database != "") {
			byTarget[k.TargetID] = p
		}
	}
	m.mu.Unlock()
	for id, p := range byTarget {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := p.p.Exec(cctx, "SELECT 1")
		cancel()
		if err != nil {
			m.recordError(id, err)
		} else {
			m.recordOK(id)
		}
	}
}

// CloseAll closes every pool (shutdown).
func (m *Manager) CloseAll() {
	m.mu.Lock()
	pools := m.pools
	m.pools = map[Key]*pool{}
	for _, s := range m.states {
		for h := range s.running {
			h.cancel()
		}
	}
	m.mu.Unlock()
	for _, p := range pools {
		p.p.Close()
	}
}
