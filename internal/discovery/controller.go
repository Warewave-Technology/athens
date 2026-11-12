package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	appslisters "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

var (
	cnpgGVR    = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}
	zalandoGVR = schema.GroupVersionResource{Group: "acid.zalan.do", Version: "v1", Resource: "postgresqls"}
)

const debounce = 500 * time.Millisecond

// APIResources reports the resources of an API group version (the subset of
// discovery.DiscoveryInterface used to detect CRDs).
type APIResources interface {
	ServerResourcesForGroupVersion(groupVersion string) (*metav1.APIResourceList, error)
}

// Controller runs informers over the watched namespaces (or the whole
// cluster, ADR-024) and rebuilds the registry per namespace (§2).
type Controller struct {
	client     kubernetes.Interface
	dyn        dynamic.Interface
	disco      APIResources
	namespaces []string
	resync     time.Duration
	opt        Options
	reg        *Registry
	log        *slog.Logger
	crdPoll    time.Duration

	scopes []*scope // one per namespace, or a single cluster-wide scope
	mu     sync.Mutex
	timers map[string]*time.Timer // debounced rebuilds per namespace
	synced atomic.Bool
}

// scope is one set of informers: a namespace, or all namespaces when ns is "".
type scope struct {
	ns       string
	factory  informers.SharedInformerFactory
	sts      appslisters.StatefulSetLister
	deploy   appslisters.DeploymentLister
	svc      corelisters.ServiceLister
	typedOK  []cache.InformerSynced
	dfactory dynamicinformer.DynamicSharedInformerFactory

	mu      sync.Mutex
	crds    map[schema.GroupVersionResource]cache.GenericLister
	crdSync []cache.InformerSynced
}

type ControllerOptions struct {
	Client    kubernetes.Interface
	Dynamic   dynamic.Interface
	Discovery APIResources
	// Namespaces limits the watch; empty watches the whole cluster.
	Namespaces []string
	Resync     time.Duration
	Options    Options
	Registry   *Registry
	Log        *slog.Logger
	// CRDPoll is how often missing CRDs are re-checked (default 5m).
	CRDPoll time.Duration
}

func NewController(o ControllerOptions) *Controller {
	if o.CRDPoll == 0 {
		o.CRDPoll = 5 * time.Minute
	}
	return &Controller{client: o.Client, dyn: o.Dynamic, disco: o.Discovery, namespaces: o.Namespaces, resync: o.Resync,
		opt: o.Options, reg: o.Registry, log: o.Log, crdPoll: o.CRDPoll, timers: map[string]*time.Timer{}}
}

// Synced reports whether the initial cache sync finished (readiness).
func (c *Controller) Synced() bool { return c.synced.Load() }

func (c *Controller) clusterWide() bool { return len(c.namespaces) == 0 }

// Run starts informers, waits for the initial sync (up to syncTimeout; the
// controller keeps running afterwards either way) and blocks until ctx ends.
func (c *Controller) Run(ctx context.Context, syncTimeout time.Duration) {
	nss := c.namespaces
	if c.clusterWide() {
		nss = []string{metav1.NamespaceAll}
	}
	for _, ns := range nss {
		s := &scope{ns: ns, crds: map[schema.GroupVersionResource]cache.GenericLister{}}
		s.factory = informers.NewSharedInformerFactoryWithOptions(c.client, c.resync, informers.WithNamespace(ns))
		sts := s.factory.Apps().V1().StatefulSets()
		dep := s.factory.Apps().V1().Deployments()
		svc := s.factory.Core().V1().Services()
		s.sts, s.deploy, s.svc = sts.Lister(), dep.Lister(), svc.Lister()
		for _, inf := range []cache.SharedIndexInformer{sts.Informer(), dep.Informer(), svc.Informer()} {
			c.addHandler(inf)
			s.typedOK = append(s.typedOK, inf.HasSynced)
		}
		s.dfactory = dynamicinformer.NewFilteredDynamicSharedInformerFactory(c.dyn, c.resync, ns, nil)
		c.scopes = append(c.scopes, s)
		s.factory.Start(ctx.Done())
	}
	c.checkCRDs(ctx)

	syncCtx, cancel := context.WithTimeout(ctx, syncTimeout)
	ok := cache.WaitForCacheSync(syncCtx.Done(), c.allSynced()...)
	cancel()
	if !ok {
		c.log.Warn("discovery cache sync timed out; continuing, readiness stays false until synced")
		go func() {
			if cache.WaitForCacheSync(ctx.Done(), c.allSynced()...) {
				c.markSynced()
			}
		}()
	} else {
		c.markSynced()
	}

	t := time.NewTicker(c.crdPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.checkCRDs(ctx)
		}
	}
}

func (c *Controller) markSynced() {
	nss := c.namespaces
	if c.clusterWide() {
		nss = c.populatedNamespaces()
	}
	for _, ns := range nss {
		c.rebuild(ns)
	}
	c.synced.Store(true)
	scope := "cluster"
	if !c.clusterWide() {
		scope = strings.Join(c.namespaces, ",")
	}
	c.log.Info("discovery synced", "scope", scope, "targets", len(c.reg.List()))
}

// populatedNamespaces lists the namespaces that hold any watched object.
func (c *Controller) populatedNamespaces() []string {
	seen := map[string]bool{}
	s := c.scopes[0]
	if l, err := s.sts.List(labels.Everything()); err == nil {
		for _, o := range l {
			seen[o.Namespace] = true
		}
	}
	if l, err := s.deploy.List(labels.Everything()); err == nil {
		for _, o := range l {
			seen[o.Namespace] = true
		}
	}
	if l, err := s.svc.List(labels.Everything()); err == nil {
		for _, o := range l {
			seen[o.Namespace] = true
		}
	}
	s.mu.Lock()
	for _, l := range s.crds {
		if items, err := l.List(labels.Everything()); err == nil {
			for _, it := range items {
				if m, ok := it.(metav1.Object); ok {
					seen[m.GetNamespace()] = true
				}
			}
		}
	}
	s.mu.Unlock()
	out := make([]string, 0, len(seen))
	for ns := range seen {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

func (c *Controller) allSynced() []cache.InformerSynced {
	var out []cache.InformerSynced
	for _, s := range c.scopes {
		out = append(out, s.typedOK...)
		s.mu.Lock()
		out = append(out, s.crdSync...)
		s.mu.Unlock()
	}
	return out
}

// namespaceOf returns the namespace of an informer event object.
func namespaceOf(obj any) string {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = d.Obj
	}
	if m, ok := obj.(metav1.Object); ok {
		return m.GetNamespace()
	}
	return ""
}

func (c *Controller) addHandler(inf cache.SharedIndexInformer) {
	trigger := func(obj any) {
		if ns := namespaceOf(obj); ns != "" {
			c.schedule(ns)
		}
	}
	_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    trigger,
		UpdateFunc: func(_, n any) { trigger(n) },
		DeleteFunc: trigger,
	})
}

// schedule debounces rebuilds of one namespace.
func (c *Controller) schedule(ns string) {
	if !c.synced.Load() {
		return // the initial rebuild runs after sync
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := c.timers[ns]; t != nil {
		t.Reset(debounce)
		return
	}
	c.timers[ns] = time.AfterFunc(debounce, func() {
		c.mu.Lock()
		delete(c.timers, ns)
		c.mu.Unlock()
		c.rebuild(ns)
	})
}

// checkCRDs starts CRD informers once their API appears.
func (c *Controller) checkCRDs(ctx context.Context) {
	want := map[schema.GroupVersionResource]bool{}
	if c.opt.CNPG.Enabled {
		want[cnpgGVR] = true
	}
	if c.opt.Zalando.Enabled {
		want[zalandoGVR] = true
	}
	for gvr := range want {
		if !c.crdPresent(gvr) {
			continue
		}
		for _, s := range c.scopes {
			s.mu.Lock()
			_, started := s.crds[gvr]
			if !started {
				gi := s.dfactory.ForResource(gvr)
				s.crds[gvr] = gi.Lister()
				s.crdSync = append(s.crdSync, gi.Informer().HasSynced)
				c.addHandler(gi.Informer())
			}
			s.mu.Unlock()
			if !started {
				s.dfactory.Start(ctx.Done())
				c.log.Info("watching custom resource", "namespace", s.ns, "resource", gvr.String())
			}
		}
	}
}

func (c *Controller) crdPresent(gvr schema.GroupVersionResource) bool {
	list, err := c.disco.ServerResourcesForGroupVersion(gvr.GroupVersion().String())
	if err != nil {
		if !errors.IsNotFound(err) {
			c.log.Warn("checking API group", "group", gvr.GroupVersion().String(), "err", err)
		}
		return false
	}
	for _, r := range list.APIResources {
		if r.Name == gvr.Resource {
			return true
		}
	}
	return false
}

// scopeFor returns the informers covering ns, or nil if ns is not watched.
func (c *Controller) scopeFor(ns string) *scope {
	for _, s := range c.scopes {
		if s.ns == metav1.NamespaceAll || s.ns == ns {
			return s
		}
	}
	return nil
}

func (c *Controller) rebuild(ns string) {
	s := c.scopeFor(ns)
	if s == nil {
		return
	}
	objs, err := s.objects(ns)
	if err != nil {
		c.log.Error("discovery rebuild", "namespace", ns, "err", err)
		c.reg.SetNamespaceError(ns, err, time.Now())
		return
	}
	targets, problems := Build(objs, c.opt)
	for _, p := range problems {
		c.log.Debug("discovery problem", "namespace", p.Namespace, "kind", p.Object.Kind, "name", p.Object.Name, "reason", p.Reason, "message", p.Message)
	}
	for _, e := range c.reg.SetNamespace(ns, targets, problems, time.Now()) {
		c.log.Info("discovery target "+string(e.Type), "target", e.Target.ID, "source", e.Target.Source)
	}
	if c.clusterWide() && len(targets) == 0 && len(problems) == 0 {
		c.reg.Forget(ns) // keep the admin page to namespaces that matter
	}
}

func (s *scope) objects(ns string) (Objects, error) {
	o := Objects{Namespace: ns}
	var err error
	if o.StatefulSets, err = s.sts.StatefulSets(ns).List(labels.Everything()); err != nil {
		return o, err
	}
	if o.Deployments, err = s.deploy.Deployments(ns).List(labels.Everything()); err != nil {
		return o, err
	}
	if o.Services, err = s.svc.Services(ns).List(labels.Everything()); err != nil {
		return o, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for gvr, l := range s.crds {
		items, err := l.ByNamespace(ns).List(labels.Everything())
		if err != nil {
			return o, err
		}
		for _, it := range items {
			u, ok := it.(*unstructured.Unstructured)
			if !ok {
				continue
			}
			if gvr == cnpgGVR {
				o.CNPG = append(o.CNPG, u)
			} else {
				o.Zalando = append(o.Zalando, u)
			}
		}
	}
	return o, nil
}

// PodSpec returns the cached pod template of a workload (literal env
// credentials, credentials.WorkloadLookup).
func (c *Controller) PodSpec(namespace string, ref ObjectRef) (*corev1.PodSpec, error) {
	w := c.scopeFor(namespace)
	if w == nil {
		return nil, fmt.Errorf("namespace %q is not watched", namespace)
	}
	switch ref.Kind {
	case "StatefulSet":
		s, err := w.sts.StatefulSets(namespace).Get(ref.Name)
		if err != nil {
			return nil, err
		}
		return &s.Spec.Template.Spec, nil
	case "Deployment":
		d, err := w.deploy.Deployments(namespace).Get(ref.Name)
		if err != nil {
			return nil, err
		}
		return &d.Spec.Template.Spec, nil
	}
	return nil, fmt.Errorf("unsupported workload kind %q", ref.Kind)
}
