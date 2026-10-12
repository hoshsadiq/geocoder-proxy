package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	mapstructure "github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/v2"

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
		if err := decodeProviderSettings(cfg, entry.Settings); err != nil {
			problems = append(problems, fmt.Sprintf("provider %s: %v", entry.Name, err))
			continue
		}
		if err := cfg.Validate(); err != nil {
			problems = append(problems, fmt.Sprintf("provider %s: %v", entry.Name, err))
			continue
		}
		geocoder, err := reg.Build(cfg)
		if err != nil {
			problems = append(problems, fmt.Sprintf("provider %s: %v", entry.Name, err))
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

// decodeProviderSettings unmarshals a provider's opaque settings into its
// own config type. Unknown keys are rejected, and environment values arrive
// as strings, so weak typing is on. A non-integral number is refused rather
// than truncated: a fractional daily_quota would otherwise silently become a
// different budget than the operator wrote.
func decodeProviderSettings(cfg registry.Config, settings map[string]any) error {
	k := koanf.New(".")
	// A static confmap cannot fail to load.
	_ = k.Load(confmap.Provider(settings, "."), nil)
	return k.UnmarshalWithConf("", cfg, koanf.UnmarshalConf{
		DecoderConfig: &mapstructure.DecoderConfig{
			ErrorUnused:      true,
			Result:           cfg,
			WeaklyTypedInput: true,
			DecodeHook:       rejectFractionalInt(),
		},
	})
}

// rejectFractionalInt refuses a fractional number being decoded into an int
// field, for values that arrive as numbers (YAML) or strings (env).
func rejectFractionalInt() mapstructure.DecodeHookFunc {
	return func(f, t reflect.Value) (any, error) {
		if t.Kind() != reflect.Int && t.Kind() != reflect.Int64 {
			return f.Interface(), nil
		}
		switch f.Kind() {
		case reflect.Float64:
			if v := f.Float(); v != math.Trunc(v) {
				return nil, fmt.Errorf("value %v is not a whole number", v)
			}
		case reflect.String:
			v, err := strconv.ParseFloat(f.String(), 64)
			if err == nil && v != math.Trunc(v) {
				return nil, fmt.Errorf("value %q is not a whole number", f.String())
			}
		}
		return f.Interface(), nil
	}
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
	// WriteTimeout must exceed the worst-case failover chain (providers x
	// (client timeout x retries + backoff)); 30s was measured below a single
	// hanging provider's 31.6s.
	app := &http.Server{
		Handler:           handler,
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
