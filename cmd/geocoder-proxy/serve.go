package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
	cache := memory.New(cfg.Cache.TTL, memory.WithMaxEntries(cfg.Cache.MaxEntries))

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

	app := &http.Server{Addr: cfg.ListenAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	opsSrv := &http.Server{Addr: cfg.OpsAddr, Handler: ops.New(reg), ReadHeaderTimeout: 10 * time.Second}

	errCh := make(chan error, 2)
	go func() { errCh <- app.ListenAndServe() }()
	go func() { errCh <- opsSrv.ListenAndServe() }()

	names := make([]string, 0, len(providers))
	for _, p := range providers {
		names = append(names, p.Geocoder.Name())
	}
	logger.Info("listening", "addr", cfg.ListenAddr, "ops_addr", cfg.OpsAddr, "providers", names)

	select {
	case <-ctx.Done():
		logger.Info("shutting down")
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server failed: %w", err)
		}
	}

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
