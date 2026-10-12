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

	providers := make([]service.Provider, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		factory, ok := registry.Lookup(p.Name)
		if !ok {
			return fmt.Errorf("unknown provider %q (registered: %v)", p.Name, registry.Names())
		}
		geocoder, err := factory(registry.Config{
			BaseURL:   p.BaseURL,
			APIKey:    p.APIKey,
			UserAgent: p.UserAgent,
		})
		if err != nil {
			return fmt.Errorf("provider %s: %w", p.Name, err)
		}
		providers = append(providers, service.Provider{
			Geocoder:   geocoder,
			Limiter:    tokenbucket.New(p.RatePerSecond),
			DailyQuota: p.DailyQuota,
		})
	}

	router := service.New(cache, metrics, providers, service.WithMaxReuseDistance(cfg.Cache.MaxReuseDistance))
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
	app := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
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
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("app shutdown: %w", err)
	}
	if err := opsSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("ops shutdown: %w", err)
	}
	return nil
}
