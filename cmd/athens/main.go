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

	"github.com/Warewave-Technology/athens/internal/audit"
	"github.com/Warewave-Technology/athens/internal/auth"
	"github.com/Warewave-Technology/athens/internal/authz"
	"github.com/Warewave-Technology/athens/internal/config"
	"github.com/Warewave-Technology/athens/internal/httpapi"
	"github.com/Warewave-Technology/athens/internal/metrics"
	"github.com/Warewave-Technology/athens/internal/store"
	"github.com/Warewave-Technology/athens/internal/version"
	"github.com/Warewave-Technology/athens/web"
)

func main() {
	configPath := flag.String("config", "/etc/athens/config.yaml", "path to config.yaml")
	dev := flag.Bool("dev", false, "proxy the SPA to the Vite dev server")
	devURL := flag.String("dev-url", "http://localhost:5173", "Vite dev server URL (with --dev)")
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
	if err := run(cfg, *dev, *devURL, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(strings.ToUpper(level)))
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})).With("app", version.Name)
}

func run(cfg *config.Config, dev bool, devURL string, log *slog.Logger) error {
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

	spa := httpapi.SPAHandler(web.Dist())
	if dev {
		if spa, err = httpapi.DevProxy(devURL); err != nil {
			return err
		}
	}
	srv := httpapi.New(httpapi.Deps{
		Config: cfg, Log: log, Store: st, Auth: authn, Authz: authz.New(cfg.Authz), Audit: aw, SPA: spa, Dev: dev,
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
