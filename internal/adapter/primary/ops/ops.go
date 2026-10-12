// Package ops is the primary adapter for the operational endpoints:
// health and Prometheus metrics, on their own listener so the scraper and
// the health check never share the public surface.
package ops

import (
	"net/http"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// New returns the ops handler. reg is the registry metrics were registered
// on; draining reports 503 on /healthz once shutdown begins, so a load
// balancer or orchestrator stops routing before the process exits.
func New(reg *prometheus.Registry, draining *atomic.Bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		if draining != nil && draining.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("draining\n"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	return mux
}
