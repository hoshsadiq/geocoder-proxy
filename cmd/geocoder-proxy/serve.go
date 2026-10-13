package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/zulucmd/zulu/v2"
	"golang.org/x/text/language/display"

	"github.com/hoshsadiq/geocoder-proxy/internal/adapter/primary/ops"
	photonprimary "github.com/hoshsadiq/geocoder-proxy/internal/adapter/primary/photon"
	"github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/cache/memory"
	metricsadp "github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/metrics/prometheus"
	"github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/pacing/tokenbucket"
	"github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/provider/registry"
	"github.com/hoshsadiq/geocoder-proxy/internal/core/service"
	"github.com/hoshsadiq/geocoder-proxy/internal/platform/config"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	// Registers every provider's factory.
	_ "github.com/hoshsadiq/geocoder-proxy/internal/adapter/secondary/provider/all"
)

func newServeCmd() *zulu.Command {
	var configPath string
	cmd := &zulu.Command{
		Use:   "serve",
		Short: "Run the geocoding service",
		Args:  zulu.NoArgs,
		RunE: func(_ *zulu.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return serve(ctx, configPath)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "path to the YAML config file (also GEOCODER_CONFIG)")
	return cmd
}

// withRequestDeadline gives each lookup the request budget: the handler's
// context carries it, so the provider chain cannot outlive it, and the
// handler turns the expiry into the 503 the caller retries.
func withRequestDeadline(next http.Handler, budget time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), budget)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// buildProviders resolves each configured provider through the registry:
// the provider allocates its own config, this function decodes the settings
// into it, the provider validates, and this function assembles the router's
// view (client, pacing, quota). No provider field name appears here.
// Every provider's problems are reported together, so one startup lists
// everything wrong with the configuration.
func buildProviders(entries []config.ProviderEntry) ([]service.Provider, error) {
	providers := make([]service.Provider, 0, len(entries))
	var problems []string

	for _, entry := range entries {
		reg, ok := registry.Lookup(entry.Name)
		if !ok {
			problems = append(problems, fmt.Sprintf("provider %q is not known (registered: %v)", entry.Name, registry.Names()))
			continue
		}
		cfg := reg.NewConfig()
		if err := registry.Decode(cfg, entry.Settings); err != nil {
			problems = append(problems, providerProblem(entry.Name, err))
			continue
		}
		if err := cfg.Validate(); err != nil {
			problems = append(problems, providerProblem(entry.Name, err))
			continue
		}
		geocoder, err := reg.Build(cfg)
		if err != nil {
			problems = append(problems, providerProblem(entry.Name, err))
			continue
		}
		providers = append(providers, service.Provider{
			Geocoder:   geocoder,
			Limiter:    tokenbucket.New(cfg.Rate()),
			DailyQuota: cfg.Quota(),
		})
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("invalid provider configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return providers, nil
}

// providerProblem formats one provider's failure as a single line, so the
// aggregated startup error stays aligned.
func providerProblem(name string, err error) string {
	return fmt.Sprintf("provider %s: %s", name, strings.ReplaceAll(err.Error(), "\n", "; "))
}

// serve is the composition root: it builds every concrete adapter and wires
// them into the core ports. Nothing else in the binary knows which cache,
// metrics backend or providers exist. It runs until ctx ends.
func serve(ctx context.Context, configPath string) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := config.Load(configPath, nil)
	if err != nil {
		return err
	}

	reg := prom.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	metrics := metricsadp.New(reg)
	var cacheOpts []memory.Option
	if cfg.Cache.MaxEntries > 0 {
		cacheOpts = append(cacheOpts, memory.WithMaxEntries(cfg.Cache.MaxEntries))
	}
	cache := memory.New(cfg.Cache.TTL, cacheOpts...)

	providers, err := buildProviders(cfg.Providers)
	if err != nil {
		return err
	}

	router := service.New(cache, metrics, providers,
		service.WithMaxReuseDistance(cfg.Cache.MaxReuseDistance),
		service.WithAttemptTimeout(cfg.UpstreamTimeout))
	handler := photonprimary.New(router, display.English.Regions(), logger)

	// Bind before announcing anything: a listener that fails to bind fails
	// the startup with no "listening" lie and no half-open sibling.
	var lc net.ListenConfig
	appLn, err := lc.Listen(ctx, "tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("binding %s: %w", cfg.ListenAddr, err)
	}
	opsLn, err := lc.Listen(ctx, "tcp", cfg.OpsAddr)
	if err != nil {
		_ = appLn.Close()
		return fmt.Errorf("binding %s: %w", cfg.OpsAddr, err)
	}

	var draining atomic.Bool
	// WriteTimeout is a backstop, not the lookup budget: the request budget
	// ends a slow lookup long before it, and this only stops a runaway from
	// holding a connection for minutes.
	app := &http.Server{
		Handler:           withRequestDeadline(handler, cfg.RequestTimeout),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      3 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}
	opsSrv := &http.Server{
		Handler:           ops.New(reg, &draining),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() { errCh <- app.Serve(appLn) }()
	go func() { errCh <- opsSrv.Serve(opsLn) }()

	names := make([]string, 0, len(providers))
	for _, p := range providers {
		names = append(names, p.Geocoder.Name())
	}
	logger.Info("listening", "addr", appLn.Addr().String(), "ops_addr", opsLn.Addr().String(), "providers", names)

	select {
	case <-ctx.Done():
		logger.Info("shutting down")
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server failed: %w", err)
		}
	}

	draining.Store(true)
	// The ops listener drains first: it is idle by design, and shutting the
	// app down first could return on its error and skip this one.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := opsSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("ops shutdown: %w", err)
	}
	if err := app.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("app shutdown: %w", err)
	}
	return nil
}
