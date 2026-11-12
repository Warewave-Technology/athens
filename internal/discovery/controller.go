package discovery

import (
	"context"
	"fmt"
	"log/slog"
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

// Controller runs namespaced informers and rebuilds the registry (§2).
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

	nss    map[string]*nsWatch
	synced atomic.Bool
}

type nsWatch struct {
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
	timer   *time.Timer
}

type ControllerOptions struct {
	Client     kubernetes.Interface
	Dynamic    dynamic.Interface
	Discovery  APIResources
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
		opt: o.Options, reg: o.Registry, log: o.Log, crdPoll: o.CRDPoll, nss: map[string]*nsWatch{}}
}

// Synced reports whether the initial cache sync finished (readiness).
func (c *Controller) Synced() bool { return c.synced.Load() }

// Run starts informers, waits for the initial sync (up to syncTimeout; the
// controller keeps running afterwards either way) and blocks until ctx ends.
func (c *Controller) Run(ctx context.Context, syncTimeout time.Duration) {
	for _, ns := range c.namespaces {
		w := &nsWatch{ns: ns, crds: map[schema.GroupVersionResource]cache.GenericLister{}}
		w.factory = informers.NewSharedInformerFactoryWithOptions(c.client, c.resync, informers.WithNamespace(ns))
		sts := w.factory.Apps().V1().StatefulSets()
		dep := w.factory.Apps().V1().Deployments()
		svc := w.factory.Core().V1().Services()
		w.sts, w.deploy, w.svc = sts.Lister(), dep.Lister(), svc.Lister()
		for _, inf := range []cache.SharedIndexInformer{sts.Informer(), dep.Informer(), svc.Informer()} {
			c.addHandler(inf, w)
			w.typedOK = append(w.typedOK, inf.HasSynced)
		}
		w.dfactory = dynamicinformer.NewFilteredDynamicSharedInformerFactory(c.dyn, c.resync, ns, nil)
		c.nss[ns] = w
		w.factory.Start(ctx.Done())
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
	for _, w := range c.nss {
		c.rebuild(w)
	}
	c.synced.Store(true)
	c.log.Info("discovery synced", "namespaces", len(c.nss), "targets", len(c.reg.List()))
}

func (c *Controller) allSynced() []cache.InformerSynced {
	var out []cache.InformerSynced
	for _, w := range c.nss {
		out = append(out, w.typedOK...)
		w.mu.Lock()
		out = append(out, w.crdSync...)
		w.mu.Unlock()
	}
	return out
}

func (c *Controller) addHandler(inf cache.SharedIndexInformer, w *nsWatch) {
	trigger := func(any) { c.schedule(w) }
	_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    trigger,
		UpdateFunc: func(_, n any) { trigger(n) },
		DeleteFunc: trigger,
	})
}

// schedule debounces rebuilds of one namespace.
func (c *Controller) schedule(w *nsWatch) {
	if !c.synced.Load() {
		return // the initial rebuild runs after sync
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Reset(debounce)
		return
	}
	w.timer = time.AfterFunc(debounce, func() {
		w.mu.Lock()
		w.timer = nil
		w.mu.Unlock()
		c.rebuild(w)
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
		for _, w := range c.nss {
			w.mu.Lock()
			_, started := w.crds[gvr]
			if !started {
				gi := w.dfactory.ForResource(gvr)
				w.crds[gvr] = gi.Lister()
				w.crdSync = append(w.crdSync, gi.Informer().HasSynced)
				c.addHandler(gi.Informer(), w)
			}
			w.mu.Unlock()
			if !started {
				w.dfactory.Start(ctx.Done())
				c.log.Info("watching custom resource", "namespace", w.ns, "resource", gvr.String())
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

func (c *Controller) rebuild(w *nsWatch) {
	objs, err := w.objects()
	if err != nil {
		c.log.Error("discovery rebuild", "namespace", w.ns, "err", err)
		c.reg.SetNamespaceError(w.ns, err, time.Now())
		return
	}
	targets, problems := Build(objs, c.opt)
	for _, p := range problems {
		c.log.Debug("discovery problem", "namespace", p.Namespace, "kind", p.Object.Kind, "name", p.Object.Name, "reason", p.Reason, "message", p.Message)
	}
	for _, e := range c.reg.SetNamespace(w.ns, targets, problems, time.Now()) {
		c.log.Info("discovery target "+string(e.Type), "target", e.Target.ID, "source", e.Target.Source)
	}
}

func (w *nsWatch) objects() (Objects, error) {
	o := Objects{Namespace: w.ns}
	var err error
	if o.StatefulSets, err = w.sts.StatefulSets(w.ns).List(labels.Everything()); err != nil {
		return o, err
	}
	if o.Deployments, err = w.deploy.Deployments(w.ns).List(labels.Everything()); err != nil {
		return o, err
	}
	if o.Services, err = w.svc.Services(w.ns).List(labels.Everything()); err != nil {
		return o, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for gvr, l := range w.crds {
		items, err := l.ByNamespace(w.ns).List(labels.Everything())
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
	w, ok := c.nss[namespace]
	if !ok {
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
