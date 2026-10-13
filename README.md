# geocoder-proxy

[![Build Status](https://img.shields.io/github/actions/workflow/status/hoshsadiq/geocoder-proxy/test.yml?branch=main)](https://github.com/hoshsadiq/geocoder-proxy/actions)
[![Go Report Card](https://goreportcard.com/badge/github.com/hoshsadiq/geocoder-proxy)](https://goreportcard.com/report/github.com/hoshsadiq/geocoder-proxy)
[![License](https://img.shields.io/github/license/hoshsadiq/geocoder-proxy)](./LICENSE)

A geocoding service that fronts multiple upstream providers behind one
Photon-compatible API. Built for [Dawarich](https://github.com/Freika/dawarich):
point `PHOTON_API_HOST` at it and imports stop paying one API call per
location point.

## Running it

```bash
geocoder-proxy serve --config config.yaml
```

Configuration layers: environment (`GEOCODER_*`) over a YAML file
(`--config` or `GEOCODER_CONFIG`) over defaults. Provider API keys come
from the environment (`GEOCODER_PROVIDER_<NAME>_API_KEY`), never from a
committed file. See [config.example.yaml](config.example.yaml) for every
key.

The Photon API listens on `:8080` by default (`/reverse`, `/api`);
health and Prometheus metrics live on `:9090` (`/healthz`, `/metrics`).

Two timeouts bound a lookup. `request_timeout` (default 4s) is the
per-request budget: a lookup still unanswered when it runs out is answered
503, below Dawarich's own 5s geocoder timeout. `upstream_timeout` (default
2s) caps a single provider attempt and must be at most half the budget, so
the pace wait plus one capped attempt still leaves room to try the next
provider inside the budget.

## What it does

- **Speaks Photon.** `GET /reverse` and `GET /api`, GeoJSON in the shape
  Dawarich already parses. No Dawarich change needed.
- **Caches by location.** Points within ~25 m of a previously answered
  address reuse it, so a night at home costs one lookup instead of hundreds.
- **Respects each provider's limits.** Per-provider rate pacing and daily
  quota counters, with failover to the next provider when one is throttled
  or down.
- **Never silently loses a point.** An upstream outage is answered with an
  error, not an empty result, so Dawarich retries instead of marking the
  point geocoded.
- **Observable.** Prometheus metrics for cache hit rate, per-provider
  latency, errors and remaining quota.

Providers planned for v1: ChibiGeo/Photon/Komoot, Geoapify, LocationIQ,
Nominatim. Adding another will be one package that registers itself.

## Getting started

Requires [mise](https://mise.jdx.dev). Then:

```sh
mise install           # tool versions from mise.toml
mise run build         # binary lands in tmp/
mise run test          # go test -race -shuffle=on ./...
mise run check         # every prek hook across the repo
mise run ci-local      # the full local CI gate
```

## Contributing

See [AGENTS.md](./AGENTS.md) for the architecture rules (hexagonal, imports
point inward, enforced by depguard) and the commit gate. Every change lands
via a pull request from a feature branch; `mise run check` must pass first.

## License

[MIT](./LICENSE)
