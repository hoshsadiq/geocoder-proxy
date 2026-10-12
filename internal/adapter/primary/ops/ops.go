// Package ops is the primary adapter for the operational endpoints:
// liveness and Prometheus metrics, on their own listener so the scraper
// and the health check never share the public surface.
package ops

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// New returns the ops handler. reg is the registry metrics were registered
// on; /healthz answers 200 unconditionally (the process being up is the
// whole check).
func New(reg *prometheus.Registry) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	return mux
}
