package discovery

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func pgSts(name string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "demo"},
		Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "pg", Image: "postgres:16", Env: []corev1.EnvVar{{
				Name: "POSTGRES_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: "password"}},
			}}}}},
		}},
	}
}

func pgSvc(name string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "demo"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": name}, Ports: []corev1.ServicePort{{Port: 5432}}},
	}
}

// lockedDiscovery makes FakeDiscovery safe to mutate while the controller polls.
type lockedDiscovery struct {
	mu sync.Mutex
	fd *fakediscovery.FakeDiscovery
}

func (l *lockedDiscovery) ServerResourcesForGroupVersion(gv string) (*metav1.APIResourceList, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fd.ServerResourcesForGroupVersion(gv)
}

func (l *lockedDiscovery) add(r *metav1.APIResourceList) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fd.Resources = append(l.fd.Resources, r)
}

func TestControllerLiveUpdates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := fake.NewClientset(pgSts("one"), pgSvc("one"))
	fd := &lockedDiscovery{fd: client.Discovery().(*fakediscovery.FakeDiscovery)}
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		cnpgGVR: "ClusterList", zalandoGVR: "postgresqlList",
	})
	reg := NewRegistry()
	events := make(chan Event, 16)
	reg.OnChange(func(e Event) { events <- e })
	opt := testOptions(t)
	c := NewController(ControllerOptions{
		Client: client, Dynamic: dyn, Discovery: fd, Namespaces: []string{"demo"}, Resync: time.Minute,
		Options: opt, Registry: reg, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), CRDPoll: 100 * time.Millisecond,
	})
	go c.Run(ctx, 5*time.Second)
	waitFor(t, 5*time.Second, c.Synced)
	if _, ok := reg.Get("demo/one"); !ok {
		t.Fatalf("initial target missing: %+v", reg.List())
	}

	// add
	start := time.Now()
	if _, err := client.AppsV1().StatefulSets("demo").Create(ctx, pgSts("two"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Services("demo").Create(ctx, pgSvc("two"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { _, ok := reg.Get("demo/two"); return ok })
	t.Logf("add propagated in %s", time.Since(start))

	// remove
	if err := client.AppsV1().StatefulSets("demo").Delete(ctx, "one", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { _, ok := reg.Get("demo/one"); return !ok })

	// CRD appears later: the CNPG informer starts and picks up the cluster
	fd.add(&metav1.APIResourceList{
		GroupVersion: "postgresql.cnpg.io/v1", APIResources: []metav1.APIResource{{Name: "clusters", Namespaced: true, Kind: "Cluster"}},
	})
	cl := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster",
		"metadata": map[string]any{"name": "pay", "namespace": "demo"}, "spec": map[string]any{"instances": int64(1)},
	}}
	if _, err := dyn.Resource(cnpgGVR).Namespace("demo").Create(ctx, cl, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { t2, ok := reg.Get("demo/pay"); return ok && t2.Source == SourceCNPG })

	if ps, err := c.PodSpec("demo", ObjectRef{Kind: "StatefulSet", Name: "two"}); err != nil || ps.Containers[0].Name != "pg" {
		t.Fatalf("PodSpec: %v", err)
	}
	if len(reg.Status()) != 1 {
		t.Fatalf("status: %+v", reg.Status())
	}
}

func TestRegistryDiff(t *testing.T) {
	r := NewRegistry()
	a := Target{ID: "ns/a", Namespace: "ns", Name: "a", Port: 1}
	b := Target{ID: "ns/b", Namespace: "ns", Name: "b"}
	other := Target{ID: "x/c", Namespace: "x", Name: "c"}
	r.SetNamespace("x", []Target{other}, nil, time.Now())
	ev := r.SetNamespace("ns", []Target{a, b}, nil, time.Now())
	if len(ev) != 2 {
		t.Fatalf("events %+v", ev)
	}
	a.Port = 2
	ev = r.SetNamespace("ns", []Target{a}, nil, time.Now())
	if len(ev) != 2 || len(r.List()) != 2 {
		t.Fatalf("events %+v list %+v", ev, r.List())
	}
	if _, ok := r.Get("x/c"); !ok {
		t.Fatal("other namespace must be untouched")
	}
}
