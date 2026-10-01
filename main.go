// pipeline-core-service is the regional routing gateway in front of the
// per-region Kubeflow Pipelines apiservers. It exposes the same API
// surface, requires a region on every request (X-Region header,
// ?region= query parameter, or a top-level "region" JSON body field),
// strips the region markers, and forwards byte-identical requests to the
// selected apiserver of that region.
//
// Additional responsibilities (see docs/pipeline-gateway-requirements.md):
// - DB-backed project_regions mapping + namespace resolution/injection
// - Flavor/image catalog (admin CRUD + FE GET)
// - Provisioning API + background reconciler
// - KFP IR spec rewrite for multipart uploads
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/golang/glog"
	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/catalog"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/config"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/handlers"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/middleware"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/namespace"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/provision"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/proxy"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/spec"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/store"
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
	regionStore := config.NewStore(regions)

	dispatcher := proxy.NewDispatcher()
	if err := dispatcher.Reload(regions); err != nil {
		glog.Fatalf("regions: %v", err)
	}
	glog.Infof("loaded %d region(s): %v", len(regions), regionStore.Names())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// — DB pool + migrations ————————————————————————————————————————————
	dbPool, err := store.New(ctx, cfg.DSN)
	if err != nil {
		glog.Fatalf("store: connect: %v", err)
	}
	defer dbPool.Close()
	if err := store.Migrate(ctx, dbPool); err != nil {
		glog.Fatalf("store: migrate: %v", err)
	}
	glog.Info("database migrations applied")

	// — Component wiring ————————————————————————————————————————————————
	nsResolver := namespace.NewResolver(dbPool, cfg.NamespaceCacheTTL)

	catStore := catalog.NewPGStore(dbPool)
	specResolver := &catalog.SpecResolver{Store: catStore}

	provStore := provision.NewPGStore(dbPool)
	kfpClient := &provision.KFPClient{HTTP: &http.Client{Timeout: 30 * time.Second}}
	provisioner := provision.New(provStore, regionStore, kfpClient)

	// — Background goroutines ———————————————————————————————————————————
	health := proxy.NewHealthChecker(dispatcher, cfg.HealthCheckInterval)
	go health.Run(ctx)
	specProvider := swagger.NewSpecProvider(dispatcher, cfg.SwaggerSourceRegion, cfg.SwaggerFetchTimeout, cfg.SwaggerRefreshInterval)
	go specProvider.Run(ctx)
	go watchRegions(ctx, cfg, regionStore, dispatcher)
	if cfg.ProvisionReconcileInterval > 0 {
		go provisioner.Run(ctx, cfg.ProvisionReconcileInterval)
	}

	router := buildRouter(cfg, regionStore, dispatcher, health, specProvider, nsResolver, catStore, specResolver, provisioner)

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
func watchRegions(ctx context.Context, cfg *config.GatewayConfig, regionStore *config.Store, dispatcher *proxy.Dispatcher) {
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
			regionStore.Swap(regions)
			glog.Infof("regions reloaded: %v", regionStore.Names())
		}
	}
}

func buildRouter(
	cfg *config.GatewayConfig,
	regionStore *config.Store,
	dispatcher *proxy.Dispatcher,
	health *proxy.HealthChecker,
	specProvider *swagger.SpecProvider,
	nsResolver namespace.Store,
	catStore catalog.Store,
	specResolver spec.ResourceLookup,
	provisioner *provision.Provisioner,
) *mux.Router {
	router := mux.NewRouter()

	// ── Infrastructure (no region, no auth) ──────────────────────────────
	router.Handle("/healthz", middleware.Logging(handlers.Healthz(version, func() int {
		return len(regionStore.Snapshot())
	}))).Methods(http.MethodGet)
	router.Handle("/regions", middleware.Logging(
		handlers.Regions(dispatcher, health, cfg.RegionsExposeEndpoints),
	)).Methods(http.MethodGet)
	router.Handle("/metrics", promhttp.Handler())
	router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/swagger", http.StatusFound)
	})
	router.PathPrefix("/swagger").Handler(middleware.Logging(swagger.Handler(specProvider)))

	// ── Middleware factories ──────────────────────────────────────────────
	withRegion := middleware.Region(regionStore, cfg.MaxJSONBodyBytes)
	withEnforcer := middleware.Enforcer(cfg.EnforcerRequired)
	withNamespace := middleware.Namespace(nsResolver, cfg.MaxJSONBodyBytes)
	withServiceKey := middleware.ServiceKey(cfg.ServiceKey)

	// P-CHAIN: provisioning — service-key guarded, no region required
	pChain := func(h http.Handler) http.Handler {
		return middleware.Logging(middleware.Recovery(withServiceKey(h)))
	}
	router.Handle("/apis/v2beta1/projects",
		pChain(provision.PutProject(provisioner))).Methods(http.MethodPut)
	router.Handle("/apis/v2beta1/projects/{project_id}",
		pChain(provision.DeleteProject(provisioner))).Methods(http.MethodDelete)

	// S-CHAIN: catalog admin — service-key guarded, no region required
	sChain := func(h http.Handler) http.Handler {
		return middleware.Logging(middleware.Recovery(withServiceKey(h)))
	}
	router.Handle("/apis/v2beta1/admin/flavors",
		sChain(catalog.AdminListFlavors(catStore))).Methods(http.MethodGet)
	router.Handle("/apis/v2beta1/admin/flavors",
		sChain(catalog.AdminCreateFlavor(catStore))).Methods(http.MethodPost)
	router.Handle("/apis/v2beta1/admin/flavors/{id}",
		sChain(catalog.AdminUpdateFlavor(catStore))).Methods(http.MethodPut)
	router.Handle("/apis/v2beta1/admin/flavors/{id}",
		sChain(catalog.AdminDeleteFlavor(catStore))).Methods(http.MethodDelete)
	router.Handle("/apis/v2beta1/admin/images",
		sChain(catalog.AdminListImages(catStore))).Methods(http.MethodGet)
	router.Handle("/apis/v2beta1/admin/images",
		sChain(catalog.AdminCreateImage(catStore))).Methods(http.MethodPost)
	router.Handle("/apis/v2beta1/admin/images/{id}",
		sChain(catalog.AdminUpdateImage(catStore))).Methods(http.MethodPut)
	router.Handle("/apis/v2beta1/admin/images/{id}",
		sChain(catalog.AdminDeleteImage(catStore))).Methods(http.MethodDelete)

	// C-CHAIN: catalog FE — region-aware, enforcer-guarded (no namespace)
	cChain := func(h http.Handler) http.Handler {
		return middleware.Logging(middleware.Recovery(withRegion(withEnforcer(h))))
	}
	router.Handle("/apis/v2beta1/flavors",
		cChain(catalog.FEListFlavors(catStore))).Methods(http.MethodGet)
	router.Handle("/apis/v2beta1/images",
		cChain(catalog.FEListImages(catStore))).Methods(http.MethodGet)

	// K-CHAIN base: page_size guard → fanout-intercept → region → enforcer
	// → namespace → dispatcher. The fanout middleware short-circuits
	// X-Region: all / ?region=all before the region validator sees it; all
	// other requests fall through to the normal single-region path.
	kBase := middleware.Logging(middleware.Recovery(
		middleware.PageSize()(
			proxy.FanoutMiddleware(dispatcher, nsResolver, cfg.EnforcerRequired)(
				withRegion(withEnforcer(withNamespace(dispatcher))),
			),
		),
	))

	// QM-CHAIN: quota pass-through — region + service-key only. CMP calls
	// these service-to-service with the region identified via X-Region;
	// the enforcer assertion and namespace resolution do not apply
	// (quota is addressed by project_id in the body).
	qChain := middleware.Logging(middleware.Recovery(
		withRegion(withServiceKey(dispatcher)),
	))
	router.PathPrefix("/quota").Handler(qChain)

	// K-CHAIN upload: spec-rewriting handler for multipart pipeline upload.
	// Namespace middleware runs first (injects ?namespace= query param), then
	// the Uploader rewrites the YAML spec and calls Forward (dispatcher).
	uploader := &spec.Uploader{
		Lookup:  specResolver,
		Forward: dispatcher.ServeHTTP,
		Lister: &spec.KFPLister{Target: func(regionName string) (*url.URL, *http.Client, bool) {
			t, ok := dispatcher.Target(regionName)
			if !ok {
				return nil, nil, false
			}
			return t.URL, t.Client, true
		}},
	}
	kUpload := middleware.Logging(middleware.Recovery(
		withRegion(withEnforcer(withNamespace(http.HandlerFunc(uploader.HandleUpload)))),
	))
	router.Handle("/apis/v2beta1/pipelines/upload", kUpload).Methods(http.MethodPost)
	router.Handle("/apis/v2beta1/pipelines/upload_version", kUpload).Methods(http.MethodPost)

	// Catch-all KFP proxy (must be last — mux matches most-specific first).
	router.PathPrefix("/").Handler(kBase)

	return router
}

// setupLogging forces glog to stderr and maps LOG_LEVEL to verbosity.
func setupLogging(level string) {
	_ = flag.Set("logtostderr", "true")
	if level == "debug" {
		_ = flag.Set("v", "2")
	}
}
