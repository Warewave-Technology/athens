package pgconn

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Warewave-Technology/athens/internal/credentials"
	"github.com/Warewave-Technology/athens/internal/discovery"
)

// ProvisionedRole is the login athens creates for read users on targets
// annotated <prefix>/readonly-provision: "true" (ADR-028). It is one role per
// PostgreSQL server: roles are cluster-wide.
const ProvisionedRole = "athens_read"

// provisionMarker is the role comment that marks ProvisionedRole as managed
// by athens; an existing role without it is never taken over.
const provisionMarker = "managed by athens (athens.warewave.io/readonly-provision): read-only login for read users; " +
	"its password changes whenever athens restarts"

// errProvision wraps a failure to set up the provisioned login; the message
// never contains the password.
type errProvision struct{ msg string }

func (e *errProvision) Error() string { return "read-only login setup failed: " + e.msg }

func (e *errProvision) Is(target error) bool { return target == ErrReadLoginUnavailable }

// provCred is a provisioned password and its generation (1, 2, ...).
type provCred struct {
	password string
	gen      uint64
}

// provLock returns the lock that serializes provisioning for one target.
func (m *Manager) provLock(id string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.provLocks[id]
	if !ok {
		l = &sync.Mutex{}
		m.provLocks[id] = l
	}
	return l
}

// provisionedCreds returns the provisioned login of t and the password's
// generation. The first call creates or updates the role over the RW login
// with a fresh random password, sent only as a SCRAM verifier; the password
// lives in memory only. reprovision (after an authentication failure) sets
// a new password only if the failing pool used the current one (staleGen);
// a pool built on an older password just takes the current one, so pools of
// the same target never knock each other's password out.
func (m *Manager) provisionedCreds(ctx context.Context, t discovery.Target, staleGen uint64, reprovision bool) (credentials.Credentials, uint64, error) {
	l := m.provLock(t.ID)
	l.Lock()
	defer l.Unlock()
	m.mu.Lock()
	c, ok := m.provisioned[t.ID]
	m.mu.Unlock()
	if ok && (!reprovision || c.gen != staleGen) {
		return credentials.Credentials{User: ProvisionedRole, Password: c.password}, c.gen, nil
	}
	pw, err := m.provision(ctx, t)
	m.mu.Lock()
	st := m.state(t.ID)
	st.provisionErr = ""
	if err != nil {
		st.provisionErr = err.Error()
	} else {
		c = provCred{password: pw, gen: c.gen + 1}
		m.provisioned[t.ID] = c
	}
	m.mu.Unlock()
	if err != nil {
		m.log().Warn("read-only login setup failed", "target", t.ID, "err", err)
		return credentials.Credentials{}, 0, err
	}
	m.log().Info("read-only login ready", "target", t.ID, "role", ProvisionedRole)
	return credentials.Credentials{User: ProvisionedRole, Password: pw}, c.gen, nil
}

// provisionTimeout bounds one setup: connecting, and the transaction.
const provisionTimeout = 30 * time.Second

// tooPowerful: the role has an attribute or a membership beyond LOGIN and
// pg_read_all_data (set by someone else, or a foreign role carrying the
// marker). Checked after every change, inside the transaction.
const tooPowerful = `SELECT r.rolsuper OR r.rolcreaterole OR r.rolcreatedb OR r.rolreplication OR r.rolbypassrls OR NOT r.rolcanlogin
	OR EXISTS (SELECT 1 FROM pg_auth_members a WHERE a.member = r.oid AND a.roleid <> 'pg_read_all_data'::regrole)
	FROM pg_roles r WHERE r.rolname = $1`

func (m *Manager) provision(ctx context.Context, t discovery.Target) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, provisionTimeout)
	defer cancel()
	conn, err := m.acquire(ctx, t, Key{TargetID: t.ID, Kind: RW})
	if err != nil {
		return "", &errProvision{msg: "the main login cannot connect"}
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return "", &errProvision{msg: "cannot start a transaction"}
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	l := m.opt.Limits
	if _, err := tx.Exec(ctx, `SELECT set_config('application_name', $1, true), set_config('statement_timeout', $2, true),
		set_config('lock_timeout', $3, true), set_config('idle_in_transaction_session_timeout', $4, true)`,
		AppName("readonly-provision"), ms(l.StatementTimeout.D()), ms(l.LockTimeout.D()), ms(2*l.StatementTimeout.D())); err != nil {
		return "", &errProvision{msg: "cannot prepare the transaction"}
	}

	var version int
	if err := tx.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil {
		return "", &errProvision{msg: "cannot read the server version"}
	}
	if version < 140000 {
		return "", &errProvision{msg: "readonly-provision needs PostgreSQL 14 or later (pg_read_all_data)"}
	}
	var exists bool
	var comment *string
	err = tx.QueryRow(ctx, `SELECT true, shobj_description(oid, 'pg_authid') FROM pg_roles WHERE rolname = $1`, ProvisionedRole).
		Scan(&exists, &comment)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", &errProvision{msg: "cannot look up the role"}
	}
	if exists && (comment == nil || *comment != provisionMarker) {
		return "", &errProvision{msg: fmt.Sprintf("role %s exists and is not managed by athens; it is left alone", ProvisionedRole)}
	}

	pw, verifier, err := newSCRAMPassword()
	if err != nil {
		return "", &errProvision{msg: "cannot generate a password"}
	}
	role := pgx.Identifier{ProvisionedRole}.Sanitize()
	// The verifier is base64 and '$', ':' only, so it is safe inside a SQL
	// string literal (utility statements take no bind parameters). The
	// server stores it as is; the password itself is never sent.
	password := " PASSWORD '" + verifier + "'"
	// ALTER only sets LOGIN and the password: naming SUPERUSER, REPLICATION
	// or BYPASSRLS at all, even as NO..., needs a superuser, and main logins
	// on managed services are not. tooPowerful catches anything else.
	stmts := []string{"ALTER ROLE " + role + " WITH LOGIN" + password}
	if !exists {
		stmts = []string{
			"CREATE ROLE " + role + " WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT" + password,
			"COMMENT ON ROLE " + role + " IS '" + provisionMarker + "'",
		}
	}
	stmts = append(stmts, "GRANT pg_read_all_data TO "+role)
	for _, s := range stmts {
		if _, err := tx.Exec(ctx, s); err != nil {
			return "", &errProvision{msg: "the main login may not create or update the role " + ProvisionedRole +
				" (it needs to be a superuser, or have CREATEROLE and the right to grant pg_read_all_data)"}
		}
	}
	var powerful bool
	if err := tx.QueryRow(ctx, tooPowerful, ProvisionedRole).Scan(&powerful); err != nil {
		return "", &errProvision{msg: "cannot check the role"}
	}
	if powerful {
		return "", &errProvision{msg: fmt.Sprintf("role %s has attributes or role memberships beyond LOGIN and pg_read_all_data; "+
			"athens does not use it (fix or drop the role)", ProvisionedRole)}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", &errProvision{msg: "cannot commit the role change"}
	}
	return pw, nil
}

// newSCRAMPassword returns a random password and its SCRAM-SHA-256 verifier
// in PostgreSQL's format: SCRAM-SHA-256$<iterations>:<salt>$<StoredKey>:<ServerKey>.
func newSCRAMPassword() (password, verifier string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	password = hex.EncodeToString(raw)
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", "", err
	}
	const iterations = 4096
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return "", "", err
	}
	mac := func(key []byte, msg string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(msg))
		return h.Sum(nil)
	}
	stored := sha256.Sum256(mac(salted, "Client Key"))
	server := mac(salted, "Server Key")
	b64 := base64.StdEncoding.EncodeToString
	verifier = fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", iterations, b64(salt), b64(stored[:]), b64(server))
	return password, verifier, nil
}
