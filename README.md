# geocoder-proxy

[![Build Status](https://img.shields.io/github/actions/workflow/status/hoshsadiq/geocoder-proxy/test.yml?branch=main)](https://github.com/hoshsadiq/geocoder-proxy/actions)
[![Go Report Card](https://goreportcard.com/badge/github.com/hoshsadiq/geocoder-proxy)](https://goreportcard.com/report/github.com/hoshsadiq/geocoder-proxy)
[![License](https://img.shields.io/github/license/hoshsadiq/geocoder-proxy)](./LICENSE)

One geocoding service in front of several upstream geocoders, answering in the
Photon API shape. Dawarich can point `PHOTON_API_HOST` here instead of at a
provider, and its imports stop costing one billed lookup per location point.

## Running it

```bash
geocoder-proxy serve --config config.yaml
```

Configuration layers environment (`GEOCODER_*`) over a YAML file (`--config` or
`GEOCODER_CONFIG`) over defaults. Provider API keys come from the environment
(`GEOCODER_PROVIDER_<instance>_API_KEY`), never from a committed file.
[config.example.yaml](config.example.yaml) lists every key.

The Photon API listens on `:8080` (`/reverse`, `/api`); health and Prometheus
metrics listen on `:9090` (`/healthz`, `/metrics`).

## Timeouts

`request_timeout` (default 4s) is the budget for one lookup, set below
Dawarich's own 5s geocoder timeout. A lookup still unanswered at the budget is
answered 503, which Dawarich retries. `upstream_timeout` (default 2s) caps a
single provider attempt and must be at most half the budget, so a lookup can
give up on a slow provider and still try the next one in time.

That holds for one lookup at a time. Providers have their own pace limit, so a
batch of lookups queues up and the last in the batch can spend the whole budget
waiting for its turn. Those answer 503 and get retried, which is better than
pushing a provider past its limit. AGENTS.md has the detail.

## What it does

- Answers `GET /reverse` and `GET /api` as GeoJSON in the shape Dawarich
  already parses, so nothing on the Dawarich side changes.
- Caches by location. Points within about 25 m of an already-answered address
  reuse that answer, so sitting at home overnight costs one lookup rather than
  hundreds.
- Paces each provider per second and counts daily calls against its quota,
  moving to the next provider when one is throttled, out of quota, or down.
- Tries providers in order, or rotates within a shared `tier`, so several
  instances share the load instead of one doing all the work until its quota
  runs out.
- Never returns an empty result to hide a failure. An empty answer means a
  provider genuinely found nothing; an outage is an error, so Dawarich retries
  the point instead of marking it geocoded.
- Exposes Prometheus metrics for cache hits, per-provider latency, errors and
  remaining quota.

Providers are registered by type: `chibigeo` (which is Photon-shaped, so it
fronts any Photon-compatible upstream), `geoapify`, `locationiq` and
`nominatim`. A new one is a package that registers itself, plus one blank
import.

## Several providers and rotation

You can list several providers, and more than one instance of the same type.
Every entry has a `name`; `type` names the provider to use and defaults to the
name, so you only set it when the same type appears more than once.

Without a `tier`, each provider stands on its own and is tried in the order you
wrote it: the first answers until it fails or runs out of quota, then the next.
That is what a config did before tiers existed, so leaving `tier` out changes
nothing.

Providers that share a `tier` rotate among themselves. Each lookup starts at
the next provider in the group, so the group shares the work instead of the
first one taking it all:

```yaml
providers:
  - name: chibi-eu
    type: chibigeo
    rate_per_second: 1
    daily_quota: 2500
    tier: 1
  - name: chibi-us
    type: chibigeo
    rate_per_second: 1
    daily_quota: 2500
    tier: 1
  - name: nominatim
    rate_per_second: 1
    daily_quota: 500
    tier: 2
    user_agent: "geocoder-proxy/0.1 (https://github.com/hoshsadiq/geocoder-proxy)"
```

A tier is a set of providers you are happy to have answer the same lookups.
Rotation goes by turn, not by speed, so a tier carries its number of providers
times its slowest member's rate. Four instances paced at one call a second
carry four lookups a second between them. Add a fifth that is faster, say 5/s,
and it only adds one: five members means each gets one turn in five, so the
group moves at five a second. Put providers in a tier only when they are
comparable, and keep a provider with thinner coverage out of a better
provider's tier.

A later `tier` is reached only once every provider before it has failed or run
out of quota. That is where a public service such as Nominatim or komoot's demo
Photon belongs, with a small daily quota: an occasional rescue, never the thing
carrying a bulk import.

An empty answer is final. When a provider answers "nothing here", the lookup
ends and that answer is cached for the whole cell, so the next provider in the
tier is not tried. A sparse provider can settle a cell that a more thorough
one would have filled, which is the other reason to keep a tier to providers
of similar coverage.

The `provider` label on the metrics and in the logs is the instance `name`, so
two instances of one type show up separately.

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

[AGENTS.md](./AGENTS.md) carries the architecture rules (hexagonal, imports
point inward, enforced by depguard) and the commit gate. Every change lands via
a pull request from a feature branch, with `mise run check` passing first.

## License

[MIT](./LICENSE)
