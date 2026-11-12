// Command mockidp is an Entra-like OIDC provider for local development and
// e2e tests. It signs every login in as one configured user whose ID token
// carries oid, tid, email, name and groups claims. Never deploy it to a real
// environment.
package main

import (
	"encoding/json"
	"flag"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
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
		// Local development: one user per access level (hack/dev/config.yaml rules).
		for _, u := range []*user{
			{oid: "oid-dev", email: "dev@example.com", name: "Dev Admin", groups: []string{"dev-admins", "dev-writers"}},
			{oid: "oid-writer", email: "writer@example.com", name: "Wanda Writer", groups: []string{"dev-writers"}},
			{oid: "oid-reader", email: "reader@example.com", name: "Rita Reader", groups: []string{"dev-readers"}},
			{oid: "oid-outsider", email: "outsider@example.com", name: "Otto Outsider", groups: []string{"some-other-group"}},
		} {
			u.tid = tid
			users[strings.TrimPrefix(u.oid, "oid-")] = u
		}
	}
	def := env("MOCKIDP_DEFAULT_USER", "")

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
				if name == "" && len(users) > 1 {
					pickUser(w, r, users) // sign-in screen: choose who to be
					return
				}
				if name == "" {
					for k := range users {
						name = k
					}
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

var pickTmpl = template.Must(template.New("pick").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<title>Mock IdP · sign in</title><style>body{font-family:system-ui,sans-serif;background:#282828;color:#ebdbb2;display:grid;
place-items:center;min-height:100vh;margin:0}main{width:28rem;padding:1.5rem 2rem;border:1px solid #504945;border-radius:8px;
background:#32302f}h1{font-size:1.1rem}a{display:block;padding:.7rem .9rem;margin:.5rem 0;border:1px solid #504945;border-radius:6px;
color:#ebdbb2;text-decoration:none}a:hover{background:#3c3836;border-color:#83a598}small{color:#a89984}</style></head><body><main>
<h1>Mock IdP — choose a user</h1><p><small>Local development only. Pick who to sign in as.</small></p>
{{range .}}<a href="{{.URL}}"><b>{{.Name}}</b> · {{.Email}}<br><small>groups: {{.Groups}}</small></a>{{end}}
</main></body></html>`))

func pickUser(w http.ResponseWriter, r *http.Request, users map[string]*user) {
	type choice struct{ URL, Name, Email, Groups string }
	names := make([]string, 0, len(users))
	for k := range users {
		names = append(names, k)
	}
	sort.Strings(names)
	var cs []choice
	for _, k := range names {
		u := users[k]
		q := r.URL.Query()
		q.Set("login_hint", k)
		cs = append(cs, choice{URL: r.URL.Path + "?" + q.Encode(), Name: u.name, Email: u.email, Groups: strings.Join(u.groups, ", ")})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pickTmpl.Execute(w, cs); err != nil {
		log.Print(err)
	}
}
