// Command athens is the in-cluster PostgreSQL web client.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Warewave-Technology/athens/internal/httpapi"
	"github.com/Warewave-Technology/athens/internal/version"
	"github.com/Warewave-Technology/athens/web"
)

func main() {
	listen := flag.String("listen", ":8080", "HTTP listen address")
	dev := flag.Bool("dev", false, "proxy the SPA to the Vite dev server")
	devURL := flag.String("dev-url", "http://localhost:5173", "Vite dev server URL (with --dev)")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(*listen, *dev, *devURL, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(listen string, dev bool, devURL string, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	spa := httpapi.SPAHandler(web.Dist())
	if dev {
		p, err := httpapi.DevProxy(devURL)
		if err != nil {
			return err
		}
		spa = p
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.Handle("/", spa)

	srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("started", "listen", listen, "version", version.Get())

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
