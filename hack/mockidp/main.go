// Command mockidp is an Entra-like OIDC provider for local development and
// e2e tests. It signs every login in as one configured user whose ID token
// carries oid, tid, email, name and groups claims. Never deploy it to a real
// environment.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/golang-jwt/jwt/v5"
	"github.com/oauth2-proxy/mockoidc"
)

type user struct {
	oid, tid, email, name string
	groups                []string
}

func (u *user) ID() string { return u.oid }

func (u *user) Userinfo([]string) ([]byte, error) {
	return json.Marshal(map[string]any{"sub": u.oid, "email": u.email, "name": u.name})
}

type claims struct {
	*mockoidc.IDTokenClaims
	OID    string   `json:"oid"`
	TID    string   `json:"tid"`
	Email  string   `json:"email"`
	Name   string   `json:"name"`
	Groups []string `json:"groups"`
}

func (u *user) Claims(_ []string, c *mockoidc.IDTokenClaims) (jwt.Claims, error) {
	return &claims{IDTokenClaims: c, OID: u.oid, TID: u.tid, Email: u.email, Name: u.name, Groups: u.groups}, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	listen := flag.String("listen", env("MOCKIDP_LISTEN", "127.0.0.1:7082"), "listen address")
	public := flag.String("public-addr", env("MOCKIDP_PUBLIC_ADDR", ""), "host:port used in the issuer URL (default: listen)")
	clientID := flag.String("client-id", env("MOCKIDP_CLIENT_ID", "athens-dev"), "client id")
	clientSecret := flag.String("client-secret", env("MOCKIDP_CLIENT_SECRET", "athens-dev-secret"), "client secret")
	flag.Parse()

	// Users are selected with the login_hint parameter (e.g. ?login_hint=reader),
	// defaulting to MOCKIDP_DEFAULT_USER. Each user is configured by
	// MOCKIDP_USER_<NAME>=email|group1,group2.
	users := map[string]*user{}
	tid := env("MOCKIDP_TENANT", "00000000-0000-0000-0000-000000000000")
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		name, ok := strings.CutPrefix(k, "MOCKIDP_USER_")
		if !ok {
			continue
		}
		email, groups, _ := strings.Cut(v, "|")
		users[strings.ToLower(name)] = &user{oid: "oid-" + strings.ToLower(name), tid: tid, email: email, name: name, groups: strings.Split(groups, ",")}
	}
	if len(users) == 0 {
		users["dev"] = &user{oid: "oid-dev", tid: tid, email: "dev@example.com", name: "Dev User", groups: []string{"dev-admins", "dev-writers"}}
	}
	def := env("MOCKIDP_DEFAULT_USER", "dev")

	m, err := mockoidc.NewServer(nil)
	if err != nil {
		log.Fatal(err)
	}
	m.ClientID, m.ClientSecret = *clientID, *clientSecret
	_ = m.AddMiddleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, mockoidc.AuthorizationEndpoint) {
				name := strings.ToLower(r.URL.Query().Get("login_hint"))
				if name == "" {
					name = def
				}
				if u, ok := users[name]; ok {
					m.QueueUser(u)
				}
			}
			next.ServeHTTP(w, r)
		})
	})
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	if err := m.Start(ln, nil); err != nil {
		log.Fatal(err)
	}
	if *public != "" {
		m.Server.Addr = *public // only used to build the issuer URL
	}
	log.Printf("mock IdP issuer=%s client_id=%s users=%d", m.Issuer(), m.ClientID, len(users))
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	_ = m.Shutdown()
}
