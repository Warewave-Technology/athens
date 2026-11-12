// Command athens is the in-cluster PostgreSQL web client.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/Warewave-Technology/athens/internal/audit"
	"github.com/Warewave-Technology/athens/internal/auth"
	"github.com/Warewave-Technology/athens/internal/authz"
	"github.com/Warewave-Technology/athens/internal/config"
	"github.com/Warewave-Technology/athens/internal/credentials"
	"github.com/Warewave-Technology/athens/internal/discovery"
	"github.com/Warewave-Technology/athens/internal/httpapi"
	"github.com/Warewave-Technology/athens/internal/metrics"
	"github.com/Warewave-Technology/athens/internal/pgconn"
	"github.com/Warewave-Technology/athens/internal/store"
	"github.com/Warewave-Technology/athens/internal/version"
	"github.com/Warewave-Technology/athens/web"
)

func main() {
	configPath := flag.String("config", "/etc/athens/config.yaml", "path to config.yaml")
	dev := flag.Bool("dev", false, "proxy the SPA to the Vite dev server")
	devURL := flag.String("dev-url", "http://localhost:5173", "Vite dev server URL (with --dev)")
	kubeconfig := flag.String("kubeconfig", "", "kubeconfig path (default: in-cluster, then $KUBECONFIG / ~/.kube/config)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		v := version.Get()
		fmt.Printf("%s %s (%s, %s, %s)\n", v.Name, v.Version, v.Commit, v.BuildDate, v.GoVersion)
		return
	}

	cfg, err := config.Load(*configPath, os.Environ())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	log := newLogger(cfg.Log.Level)
	if err := run(cfg, *dev, *devURL, *kubeconfig, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(strings.ToUpper(level)))
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})).With("app", version.Name)
}

func run(cfg *config.Config, dev bool, devURL, kubeconfig string, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("starting", "version", version.Get())

	st, err := store.OpenSQLite(ctx, cfg.Store.Path)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	aw := audit.NewWriter(st, log, cfg.Audit.LogSQL)
	authn, err := auth.New(auth.Options{Config: cfg, Store: st, Audit: aw, Log: log})
	if err != nil {
		return err
	}
	authn.InitWithRetry(ctx, 60*time.Second)

	var bg sync.WaitGroup
	bgCtx, cancelBG := context.WithCancel(ctx)
	defer cancelBG()
	bg.Go(func() { store.RunJanitor(bgCtx, st, cfg.Auth.IdleTTL.D(), cfg.Audit.RetentionDays, log) })

	kc, err := kubeClients(kubeconfig)
	if err != nil {
		return err
	}
	dopt, err := discovery.OptionsFromConfig(cfg.Discovery)
	if err != nil {
		return err
	}
	registry := discovery.NewRegistry()
	disc := discovery.NewController(discovery.ControllerOptions{
		Client: kc.client, Dynamic: kc.dynamic, Discovery: kc.client.Discovery(), Namespaces: cfg.Discovery.Namespaces,
		Resync: cfg.Discovery.Resync.D(), Options: dopt, Registry: registry, Log: log,
	})
	bg.Go(func() { disc.Run(bgCtx, 60*time.Second) })

	pools := pgconn.NewManager(pgconn.Options{
		Targets: registry, Resolver: credentials.NewResolver(kc.client, disc), Limits: cfg.Limits, Log: log,
		DialOverrides: cfg.Server.DevHostOverrides,
	})
	registry.OnChange(pools.TargetChanged)
	bg.Go(func() { pools.Maintain(bgCtx) })

	spa := httpapi.SPAHandler(web.Dist())
	if dev {
		if spa, err = httpapi.DevProxy(devURL); err != nil {
			return err
		}
	}
	srv := httpapi.New(httpapi.Deps{
		Config: cfg, Log: log, Store: st, Auth: authn, Authz: authz.New(cfg.Authz), Audit: aw, Registry: registry, Pools: pools,
		SPA: spa, Dev: dev, Ready: disc.Synced,
	})

	servers := []*http.Server{
		{Addr: cfg.Server.Listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second},
		{Addr: cfg.Server.MetricsListen, Handler: metricsMux(), ReadHeaderTimeout: 10 * time.Second},
	}
	errc := make(chan error, len(servers))
	for _, s := range servers {
		go func() {
			log.Info("listening", "addr", s.Addr)
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("listen %s: %w", s.Addr, err)
			}
		}()
	}

	select {
	case err = <-errc:
	case <-ctx.Done():
		log.Info("shutting down")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, s := range servers {
		if serr := s.Shutdown(shutdownCtx); serr != nil {
			log.Warn("shutdown", "addr", s.Addr, "err", serr)
		}
	}
	cancelBG()
	bg.Wait()
	return err
}

func metricsMux() http.Handler {
	m := http.NewServeMux()
	m.Handle("GET /metrics", metrics.Handler())
	return m
}

type kube struct {
	client  kubernetes.Interface
	dynamic dynamic.Interface
}

func kubeClients(kubeconfig string) (*kube, error) {
	var rc *rest.Config
	var err error
	if kubeconfig == "" {
		rc, err = rest.InClusterConfig()
	}
	if kubeconfig != "" || errors.Is(err, rest.ErrNotInCluster) {
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		rules.ExplicitPath = kubeconfig
		rc, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("kubernetes config: %w", err)
	}
	rc.UserAgent = version.Name + "/" + version.Version
	c, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, err
	}
	d, err := dynamic.NewForConfig(rc)
	if err != nil {
		return nil, err
	}
	return &kube{client: c, dynamic: d}, nil
}
