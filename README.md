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
one pace wait plus one capped attempt leaves room to try the next provider
inside the budget.

That failover guarantee is per **uncontended** lookup. Each provider is
paced (`rate_per_second`, 1/s in the homelab deployment) and holds its own
bucket (`tokenbucket.New` per provider), so a lookup queues once per
provider it tries: the fallback provider is a second queue to wait behind,
not a free one. The Nth simultaneous lookup waits about N-1 seconds for
its token before its attempt even starts, so under load the pace queue
spends the budget before any attempt runs. Measured against the deployed
shape — both providers at `rate_per_second` 1, provider 1 hanging, provider
2 healthy, `request_timeout` 4s, `upstream_timeout` 2s, three runs per row:

| concurrent lookups | answered | 503s |
| ------------------ | -------- | ---- |
| 1 | 1, at 2.0s | 0 |
| 3 | 2-3, at 2.0-4.0s | 0-1 |
| 6 | 4, at 2.0-4.0s | 2 |
| 10 | 5, at 0.0-4.0s | 5 |

The counts move by one between runs, because two different things end a
lookup. Most 503s land on the 4s budget, but one can arrive much earlier:
when a provider refuses to queue, `x/time/rate` returns "would exceed
context deadline" as soon as the reservation falls past the deadline, so
the router fails over at once, finds every provider refusing, and answers
503 long before the budget (measured at 2.0s and 3.0s at 10 concurrent).
Dawarich retries a 503, so a contended lookup degrades to a retry rather
than to a wrong answer.

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
