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
}

type pool struct {
	key      Key
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
	running   map[*runHandle]struct{}
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
	return m.runOn(ctx, conn, req, fn)
}

func (m *Manager) log() *slog.Logger { return m.opt.Log }

func (m *Manager) runOn(ctx context.Context, conn *pgxpool.Conn, req Request, fn func(context.Context, pgx.Tx) error) (err error) {
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
		return classify(ctx, err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	l := m.opt.Limits
	if _, err = tx.Exec(ctx, `SELECT set_config('application_name', $1, true), set_config('statement_timeout', $2, true),
		set_config('lock_timeout', $3, true), set_config('idle_in_transaction_session_timeout', $4, true)`,
		AppName(req.Email), ms(l.StatementTimeout.D()), ms(l.LockTimeout.D()), ms(2*l.StatementTimeout.D())); err != nil {
		return classify(ctx, err)
	}
	if err = fn(ctx, tx); err != nil {
		return classify(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return classify(ctx, err)
	}
	return nil
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
	p, err := m.pool(ctx, t, key, false)
	if err != nil {
		return nil, err
	}
	conn, err := p.p.Acquire(ctx)
	if err != nil && isAuthError(err) {
		m.log().Info("authentication failed, re-resolving credentials", "target", t.ID, "kind", key.Kind)
		if p, err = m.pool(ctx, t, key, true); err != nil {
			return nil, err
		}
		conn, err = p.p.Acquire(ctx)
	}
	if err != nil {
		m.recordError(t.ID, err)
		return nil, &ErrUnavailable{Cause: err}
	}
	p.touch()
	m.recordOK(t.ID)
	return conn, nil
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

func (m *Manager) pool(ctx context.Context, t discovery.Target, key Key, recreate bool) (*pool, error) {
	m.mu.Lock()
	p, ok := m.pools[key]
	if ok && recreate {
		delete(m.pools, key)
		go p.p.Close()
		ok = false
	}
	m.mu.Unlock()
	if ok {
		return p, nil
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
	created := &pool{key: key, p: np, created: time.Now(), lastUsed: time.Now()}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.pools[key]; ok { // lost a creation race
		np.Close()
		return existing, nil
	}
	m.pools[key] = created
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
		db = m.opt.Resolver.DefaultDB(ctx, t)
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
		if _, ok := byTarget[k.TargetID]; !ok {
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
