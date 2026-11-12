// Package pgtest starts throwaway PostgreSQL containers (testcontainers) and
// builds discovery targets pointing at them. Used by integration tests only.
package pgtest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/Warewave-Technology/athens/internal/authz"
	"github.com/Warewave-Technology/athens/internal/config"
	"github.com/Warewave-Technology/athens/internal/credentials"
	"github.com/Warewave-Technology/athens/internal/discovery"
	"github.com/Warewave-Technology/athens/internal/pgconn"
)

// Throwaway credentials of disposable test containers.
// #nosec G101
const (
	Namespace = "itest"
	Password  = "itest-superuser-pw"
	ROUser    = "athens_ro"
	ROPass    = "itest-ro-pw"
)

// Versions are the server images every integration test runs against.
var Versions = []string{"postgres:16-alpine", "postgres:13-alpine"}

type Instance struct {
	Image     string
	Host      string
	Port      int
	container testcontainers.Container
}

// Start runs one container with a superuser "postgres", database "app" and a
// read-only role (SELECT on all tables of public).
func Start(ctx context.Context, image string) (*Instance, error) {
	c, err := postgres.Run(ctx, image,
		postgres.WithDatabase("app"), postgres.WithUsername("postgres"), postgres.WithPassword(Password),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90*time.Second)),
	)
	if err != nil {
		return nil, err
	}
	host, err := c.Host(ctx)
	if err != nil {
		return nil, err
	}
	p, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return nil, err
	}
	in := &Instance{Image: image, Host: host, Port: int(p.Num()), container: c}
	conn, err := in.Connect(ctx, "app")
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s';
		GRANT CONNECT ON DATABASE app TO %[1]s; GRANT USAGE ON SCHEMA public TO %[1]s;
		ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO %[1]s`, ROUser, ROPass))
	return in, err
}

func (in *Instance) Stop(ctx context.Context) { _ = in.container.Terminate(ctx) }

// Connect opens a superuser connection (fixtures, assertions).
func (in *Instance) Connect(ctx context.Context, db string) (*pgx.Conn, error) {
	return pgx.Connect(ctx, fmt.Sprintf("postgres://postgres:%s@%s/%s?sslmode=disable", Password,
		net.JoinHostPort(in.Host, strconv.Itoa(in.Port)), db))
}

// Target returns a discovery target with rw (superuser) and ro credentials
// stored in fake Secrets; the returned clientset holds them.
func (in *Instance) Target(name string) (discovery.Target, *fake.Clientset) {
	client := fake.NewClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-rw", Namespace: Namespace},
			Data: map[string][]byte{"username": []byte("postgres"), "password": []byte(Password)}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-ro", Namespace: Namespace},
			Data: map[string][]byte{"username": []byte(ROUser), "password": []byte(ROPass)}},
	)
	ref := func(secret string) discovery.CredentialRef {
		return discovery.CredentialRef{
			User:     discovery.ValueSource{Secret: &discovery.KeyRef{Name: secret, Key: "username"}},
			Password: discovery.ValueSource{Secret: &discovery.KeyRef{Name: secret, Key: "password"}},
		}
	}
	ro := ref(name + "-ro")
	return discovery.Target{
		ID: Namespace + "/" + name, Namespace: Namespace, Name: name, DisplayName: name, Source: discovery.SourceAnnotation,
		Host: in.Host, Port: in.Port, DefaultDB: "app", SSLMode: "disable", RW: ref(name + "-rw"), RO: &ro,
	}, client
}

// Registry is a minimal pgconn.Targets backed by a map.
type Registry map[string]discovery.Target

func (r Registry) Get(id string) (discovery.Target, bool) { t, ok := r[id]; return t, ok }

// Manager builds a pool manager for one target.
func (in *Instance) Manager(t discovery.Target, client *fake.Clientset, limits config.Limits) *pgconn.Manager {
	return pgconn.NewManager(pgconn.Options{
		Targets: Registry{t.ID: t}, Resolver: credentials.NewResolver(client, nil), Limits: limits,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

// Limits returns default limits.
func Limits() config.Limits { return config.Default().Limits }

// Req is a convenience request for user "it@example.com".
func Req(t discovery.Target, db string, level authz.Level) pgconn.Request {
	return pgconn.Request{Target: t, Database: db, Level: level, Email: "it@example.com"}
}

// Skip reports whether integration tests should be skipped (no Docker).
func Skip() bool { return os.Getenv("ATHENS_SKIP_INTEGRATION") != "" }
