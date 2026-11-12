//go:build e2e

// Package e2e runs inside the kind cluster (hack/kind/e2e.sh) as a Job: it
// signs in through the mock IdP, checks discovery of every fixture pattern,
// queries each target through the API and verifies live discovery updates.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var base = os.Getenv("ATHENS_URL")

type session struct {
	t    *testing.T
	http *http.Client
	csrf string
}

// login runs the authorization code flow, picking the mock IdP user with
// login_hint.
func login(t *testing.T, user string) *session {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if strings.Contains(req.URL.Path, "/oidc/authorize") {
			q := req.URL.Query()
			q.Set("login_hint", user)
			req.URL.RawQuery = q.Encode()
		}
		return nil
	}}
	resp, err := c.Get(base + "/auth/login?returnTo=/api/v1/me")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "@e2e.test") {
		t.Fatalf("login %s: %d %s", user, resp.StatusCode, body)
	}
	u, _ := url.Parse(base)
	s := &session{t: t, http: c}
	for _, ck := range jar.Cookies(u) {
		if ck.Name == "athens_csrf" {
			s.csrf = ck.Value
		}
	}
	return s
}

func (s *session) do(method, path string, body any, out any) int {
	s.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, base+"/api/v1"+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Athens-CSRF", s.csrf)
	req.Header.Set("Origin", base)
	resp, err := s.http.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			s.t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
	if resp.StatusCode >= 400 {
		s.t.Logf("%s %s -> %d %s", method, path, resp.StatusCode, b)
	}
	return resp.StatusCode
}

type target struct {
	ID       string   `json:"id"`
	Source   string   `json:"source"`
	Warnings []string `json:"warnings"`
}

func discovered(s *session) map[string]target {
	var d struct{ Targets []target }
	s.do(http.MethodGet, "/admin/discovery", nil, &d)
	m := map[string]target{}
	for _, t := range d.Targets {
		m[t.ID] = t
	}
	return m
}

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) time.Duration {
	t.Helper()
	start := time.Now()
	for time.Since(start) < d {
		if cond() {
			return time.Since(start)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
	return 0
}

func tpath(id string) string { return "/targets/" + id }

func TestEndToEnd(t *testing.T) {
	waitFor(t, 2*time.Minute, "athens ready", func() bool {
		resp, err := http.Get(base + "/readyz")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == 200
	})
	admin := login(t, "admin")
	writer := login(t, "writer")
	reader := login(t, "reader")

	// Discovery is annotation-only (ADR-020): every fixture is opted in with
	// athens.warewave.io/enabled; credentials still come from the workload env
	// or the operator's secrets.
	want := map[string]string{
		"athens-demo/orders-db":     "annotation", // env secretKeyRef
		"athens-demo/scratch-db":    "annotation", // literal env
		"athens-demo/files-db":      "annotation", // _FILE + headless service
		"athens-demo/legacy-crm":    "annotation", // secret annotation + ro credential
		"athens-demo/cnpg-db":       "cnpg",
		"athens-demo/acid-e2e":      "zalando",
		"athens-demo/pg-postgresql": "annotation", // bitnami chart, annotated after install
	}
	var got map[string]target
	waitFor(t, 2*time.Minute, "all fixture targets", func() bool {
		got = discovered(admin)
		for id := range want {
			if _, ok := got[id]; !ok {
				return false
			}
		}
		return true
	})
	for id, src := range want {
		if got[id].Source != src {
			t.Errorf("%s: source %s, want %s", id, got[id].Source, src)
		}
	}
	for id := range got {
		if !strings.HasPrefix(id, "athens-demo/") {
			t.Errorf("unexpected target %s", id)
		}
	}

	t.Run("query every target", func(t *testing.T) {
		for id := range want {
			var dbs struct{ Databases []struct{ Name string } }
			if code := writer.do(http.MethodGet, tpath(id)+"/databases", nil, &dbs); code != 200 || len(dbs.Databases) == 0 {
				t.Errorf("%s databases: %d", id, code)
				continue
			}
			db := dbs.Databases[0].Name
			for _, d := range dbs.Databases {
				if d.Name != "postgres" {
					db = d.Name
				}
			}
			var out struct{ Rows [][]*string }
			if code := writer.do(http.MethodPost, tpath(id)+"/db/"+db+"/query", map[string]any{"sql": "select current_user, version()"}, &out); code != 200 || len(out.Rows) != 1 {
				t.Errorf("%s query: %d", id, code)
			} else {
				t.Logf("%s/%s: user=%s", id, db, *out.Rows[0][0])
			}
		}
	})

	t.Run("read-only credential and enforcement", func(t *testing.T) {
		var out struct{ Rows [][]*string }
		if code := reader.do(http.MethodPost, tpath("athens-demo/legacy-crm")+"/db/crm/query", map[string]any{"sql": "select session_user"}, &out); code != 200 || *out.Rows[0][0] != "crm_ro" {
			t.Fatalf("reader on legacy-crm: %d %v", code, out.Rows)
		}
		if code := reader.do(http.MethodPost, tpath("athens-demo/legacy-crm")+"/db/crm/query", map[string]any{"sql": "update accounts set name = name"}, nil); code != 403 {
			t.Fatalf("reader update: %d", code)
		}
		if code := writer.do(http.MethodPost, tpath("athens-demo/legacy-crm")+"/db/crm/query", map[string]any{"sql": "update accounts set name = name where id = 1"}, nil); code != 409 {
			t.Fatalf("production write without confirm: %d", code)
		}
		var g struct{ Rows [][]*string }
		if code := reader.do(http.MethodPost, tpath("athens-demo/legacy-crm")+"/db/crm/relations/public/accounts/rows/query", map[string]any{"limit": 5}, &g); code != 200 || len(g.Rows) != 5 {
			t.Fatalf("grid: %d", code)
		}
	})

	t.Run("provisioned read-only login", func(t *testing.T) {
		// orders-db: readonly-provision, athens manages athens_read (ADR-028)
		var out struct{ Rows [][]*string }
		if code := reader.do(http.MethodPost, tpath("athens-demo/orders-db")+"/db/orders/query", map[string]any{"sql": "select session_user"}, &out); code != 200 || *out.Rows[0][0] != "athens_read" {
			t.Fatalf("reader on orders-db: %d %v", code, out.Rows)
		}
		if code := reader.do(http.MethodPost, tpath("athens-demo/orders-db")+"/db/orders/query", map[string]any{"sql": "create table x(id int)"}, nil); code != 403 {
			t.Fatalf("reader create: %d", code)
		}
		// no read-only login: read users do not see the target at all
		if code := reader.do(http.MethodGet, tpath("athens-demo/scratch-db")+"/databases", nil, nil); code != 404 {
			t.Fatalf("reader on scratch-db: %d", code)
		}
	})

	t.Run("live discovery update", func(t *testing.T) {
		rc, err := rest.InClusterConfig()
		if err != nil {
			t.Skip("not in cluster:", err)
		}
		k := kubernetes.NewForConfigOrDie(rc)
		ns := os.Getenv("E2E_NAMESPACE")
		ctx := context.Background()
		labels := map[string]string{"app": "live-db"}
		sts := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "live-db", Annotations: map[string]string{
				"athens.warewave.io/enabled": "true", "athens.warewave.io/secret": "live-db",
			}},
			Spec: appsv1.StatefulSetSpec{
				Replicas: new(int32), Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "pg", Image: "postgres:16-alpine"}}}},
			},
		}
		svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "live-db"},
			Spec: corev1.ServiceSpec{Selector: labels, Ports: []corev1.ServicePort{{Port: 5432}}}}
		_ = k.AppsV1().StatefulSets(ns).Delete(ctx, "live-db", metav1.DeleteOptions{})
		_ = k.CoreV1().Services(ns).Delete(ctx, "live-db", metav1.DeleteOptions{})
		if _, err := k.CoreV1().Services(ns).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := k.AppsV1().StatefulSets(ns).Create(ctx, sts, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		added := waitFor(t, 10*time.Second, "live-db added", func() bool { _, ok := discovered(admin)["athens-demo/live-db"]; return ok })
		if err := k.AppsV1().StatefulSets(ns).Delete(ctx, "live-db", metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		removed := waitFor(t, 10*time.Second, "live-db removed", func() bool { _, ok := discovered(admin)["athens-demo/live-db"]; return !ok })
		_ = k.CoreV1().Services(ns).Delete(ctx, "live-db", metav1.DeleteOptions{})
		t.Logf("registry update latency: add=%s remove=%s", added, removed)
		if added > 2*time.Second || removed > 2*time.Second {
			t.Fatalf("registry updates must land within 2s (add=%s remove=%s)", added, removed)
		}
	})

	t.Run("access annotations", func(t *testing.T) {
		rc, err := rest.InClusterConfig()
		if err != nil {
			t.Skip("not in cluster:", err)
		}
		k := kubernetes.NewForConfigOrDie(rc)
		ns := os.Getenv("E2E_NAMESPACE")
		ctx := context.Background()
		labels := map[string]string{"app": "access-db"}
		// readonly + an access list naming only the readers group (ADR-022)
		sts := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "access-db", Annotations: map[string]string{
				"athens.warewave.io/enabled": "true", "athens.warewave.io/secret": "access-db",
				"athens.warewave.io/readonly": "true", "athens.warewave.io/access": "e2e-readers:rw",
				"athens.warewave.io/readonly-provision": "true",
			}},
			Spec: appsv1.StatefulSetSpec{
				Replicas: new(int32), Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "pg", Image: "postgres:16-alpine"}}}},
			},
		}
		svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "access-db"},
			Spec: corev1.ServiceSpec{Selector: labels, Ports: []corev1.ServicePort{{Port: 5432}}}}
		_ = k.AppsV1().StatefulSets(ns).Delete(ctx, "access-db", metav1.DeleteOptions{})
		_ = k.CoreV1().Services(ns).Delete(ctx, "access-db", metav1.DeleteOptions{})
		if _, err := k.CoreV1().Services(ns).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := k.AppsV1().StatefulSets(ns).Create(ctx, sts, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_ = k.AppsV1().StatefulSets(ns).Delete(ctx, "access-db", metav1.DeleteOptions{})
			_ = k.CoreV1().Services(ns).Delete(ctx, "access-db", metav1.DeleteOptions{})
		}()
		waitFor(t, 10*time.Second, "access-db added", func() bool { _, ok := discovered(admin)["athens-demo/access-db"]; return ok })
		level := func(s *session) string {
			var ts struct {
				Targets []struct{ ID, Level string }
			}
			s.do(http.MethodGet, "/targets", nil, &ts)
			for _, x := range ts.Targets {
				if x.ID == "athens-demo/access-db" {
					return x.Level
				}
			}
			return "none"
		}
		if got := level(reader); got != "read" {
			t.Fatalf("reader (rw in annotation, readonly target): %s", got)
		}
		if got := level(writer); got != "none" {
			t.Fatalf("writer (write by rules, not in the access list): %s", got)
		}
		if code := writer.do(http.MethodGet, tpath("athens-demo/access-db")+"/databases", nil, nil); code != 404 {
			t.Fatalf("writer databases: %d", code)
		}
	})

	t.Run("visibility", func(t *testing.T) {
		var ts struct{ Targets []target }
		reader.do(http.MethodGet, "/targets", nil, &ts)
		ids := []string{}
		for _, x := range ts.Targets {
			ids = append(ids, x.ID)
			if len(x.Warnings) > 0 {
				t.Errorf("non-admin sees warnings for %s", x.ID)
			}
		}
		if !slices.Contains(ids, "athens-demo/orders-db") || slices.Contains(ids, "athens-demo/scratch-db") {
			t.Fatalf("reader targets: %v", ids)
		}
		if code := reader.do(http.MethodGet, "/admin/discovery", nil, nil); code != 404 {
			t.Fatalf("reader admin: %d", code)
		}
	})
	fmt.Println("e2e: OK")
}
