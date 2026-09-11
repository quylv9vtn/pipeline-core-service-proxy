// pipeline-core-service is the regional routing gateway in front of the
// per-region Kubeflow Pipelines apiservers. It exposes the same API
// surface, requires a region on every request (X-Region header,
// ?region= query parameter, or a top-level "region" JSON body field),
// strips the region markers, and forwards byte-identical requests to the
// selected apiserver of that region.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/golang/glog"
	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/config"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/handlers"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/middleware"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/proxy"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/swagger"
)

// version is reported on /healthz; override at build time with
// -ldflags "-X main.version=<tag>".
var version = "dev"

// reloadInterval is the regions-file hot-reload poll period. Polling
// (mtime+size) is deliberate: kubelet swaps ConfigMap volume symlinks,
// which file-watching events frequently miss in containers.
const reloadInterval = 30 * time.Second

func main() {
	cfg, err := config.Load()
	if err != nil {
		glog.Fatalf("config: %v", err)
	}
	setupLogging(cfg.LogLevel)

	regions, err := config.LoadRegionsFile(cfg.RegionsConfigPath)
	if err != nil {
		glog.Fatalf("regions: %v", err)
	}
	store := config.NewStore(regions)

	dispatcher := proxy.NewDispatcher()
	if err := dispatcher.Reload(regions); err != nil {
		glog.Fatalf("regions: %v", err)
	}
	glog.Infof("loaded %d region(s): %v", len(regions), store.Names())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	health := proxy.NewHealthChecker(dispatcher, cfg.HealthCheckInterval)
	go health.Run(ctx)
	specProvider := swagger.NewSpecProvider(dispatcher, cfg.SwaggerSourceRegion, cfg.SwaggerFetchTimeout, cfg.SwaggerRefreshInterval)
	go specProvider.Run(ctx)
	go watchRegions(ctx, cfg, store, dispatcher)

	router := buildRouter(cfg, store, dispatcher, health, specProvider)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           router,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No ReadTimeout/WriteTimeout: run-log streams are unbounded.
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	glog.Infof("pipeline-core-service %s listening on %s", version, srv.Addr)

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			glog.Fatalf("server: %v", err)
		}
	case <-ctx.Done():
	}

	glog.Info("shutting down (30s grace for in-flight streams)")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		glog.Errorf("shutdown: %v", err)
	}
	glog.Flush()
}

// watchRegions hot-reloads the regions file: on mtime/size change it
// re-validates, swaps the store, and rebuilds the proxies. On invalid
// content the previous snapshot keeps serving and the error is logged.
func watchRegions(ctx context.Context, cfg *config.GatewayConfig, store *config.Store, dispatcher *proxy.Dispatcher) {
	path := cfg.RegionsConfigPath
	type sig struct {
		modTime, size int64
	}
	stat := func() (sig, error) {
		fi, err := os.Stat(path)
		if err != nil {
			return sig{}, err
		}
		return sig{fi.ModTime().UnixNano(), fi.Size()}, nil
	}
	last, err := stat()
	if err != nil {
		glog.Warningf("regions watch: %v", err)
	}
	t := time.NewTicker(reloadInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cur, err := stat()
			if err != nil {
				glog.Errorf("regions watch: %v", err)
				continue
			}
			if cur == last {
				continue
			}
			last = cur
			regions, err := config.LoadRegionsFile(path)
			if err != nil {
				glog.Errorf("regions reload skipped (keeping previous): %v", err)
				continue
			}
			if err := dispatcher.Reload(regions); err != nil {
				glog.Errorf("regions reload skipped (keeping previous): %v", err)
				continue
			}
			store.Swap(regions)
			glog.Infof("regions reloaded: %v", store.Names())
		}
	}
}

func buildRouter(cfg *config.GatewayConfig, store *config.Store, dispatcher *proxy.Dispatcher, health *proxy.HealthChecker, spec *swagger.SpecProvider) *mux.Router {
	router := mux.NewRouter()

	// Gateway-owned endpoints (no region required).
	router.Handle("/healthz", middleware.Logging(handlers.Healthz(version, func() int {
		return len(store.Snapshot())
	}))).Methods(http.MethodGet)
	router.Handle("/regions", middleware.Logging(handlers.Regions(dispatcher, health))).Methods(http.MethodGet)
	router.Handle("/metrics", promhttp.Handler())
	router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/swagger", http.StatusFound)
	})
	router.PathPrefix("/swagger").Handler(middleware.Logging(swagger.Handler(spec)))

	// Everything else (/apis/..., /quota/...) is proxied with a strict
	// region requirement.
	dispatch := middleware.Logging(middleware.Recovery(middleware.Region(store, cfg.MaxJSONBodyBytes)(dispatcher)))
	router.PathPrefix("/").Handler(dispatch)

	return router
}

// setupLogging forces glog to stderr and maps LOG_LEVEL to verbosity.
func setupLogging(level string) {
	// glog flags are not parsed here; force stderr output and map the
	// LOG_LEVEL to verbosity.
	_ = flag.Set("logtostderr", "true")
	if level == "debug" {
		_ = flag.Set("v", "2")
	}
}
