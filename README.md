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

## Retries

A provider call that fails in a way that might pass on its own (a dropped
connection, a 5xx, a 429 that means "slow down") is retried twice with a short
backoff: half a second, then a second, with jitter. A `Retry-After` header is
honoured instead, capped at 5 seconds. `upstream_timeout` (2s by default) bounds
the whole attempt, wait included, so a `Retry-After` much above a second and a
half leaves no room to retry and the lookup fails over instead.

A cap that waiting cannot clear is the exception. ChibiGeo documents exactly one
such error on its geocoding endpoint: `limit_exceeded`, a plan cap on a 429
whose own docs say retrying after a second does not reset the quota. The
Photon-shaped provider (`chibigeo`) does not retry it: the provider is asked
once and the lookup moves to the next one, if there is one. That spares the
lookup two retries and roughly a second and a half of backoff that cannot help
it. Every other documented error is retried by the rule above, including
`rate_limited` (429), `service_unavailable` (503) and `gateway_timeout` (504).
The two 401 auth errors are not retried and not transient: the retry rule covers
429 and 5xx, so a 401 fails once and moves to the next provider. A 429 whose
body carries no recognised cap is transient too, and a body that stalls is
bounded by the attempt cap like any other slow response. The check is attached
to the provider type, so every `chibigeo`-typed instance has it, whatever
upstream it points at.

A 200 whose body carries an `error` is a failure, not an empty answer: an empty
answer is cached and treated as final downstream, so a swallowed error would
lose the point. Photon itself never does this: its handler always answers 200
with a FeatureCollection, empty when nothing matches (`GenericSearchHandler` and
`GeoJsonFormatter` in its source), and its errors are `{"message":...}` on a
non-200. The guard is for ChibiGeo's wrapper, whose docs put errors on
401/429/503/504 and never describe a 200 with an error. The Photon-shaped
provider is features-aware, so features are served when at least one of them is
usable. AGENTS.md has the per-adapter detail.

Provider errors are built from the status, the Go type and an offset, never from
the response text, so a credential or a coordinate a body smuggles into a decode
failure cannot reach a log.

One cost is easy to miss. A capped provider is still paced and still admits
against its local daily quota before the request is made, so a capped instance
keeps taking its turn and spending its local allowance on 429s until it is
removed from the config.

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
- Exposes Prometheus metrics for cache hits, per-provider latency, errors,
  remaining quota, and terminal caps (`geocoder_provider_terminal_total`).

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
    user_agent: "geocoder-proxy/0.3 (https://github.com/hoshsadiq/geocoder-proxy)"
```

A tier's number decides the order, not the position in the file: a provider in
tier 2 is tried before one in tier 10 even when the tier 10 provider is written
first. A provider with no `tier` still takes a number, its place in the list,
and startup fails if that number is one another provider sets explicitly. That
keeps the two meanings apart: either you set a tier on every provider that
should share one, or you leave the whole file ordered. A tier is a set of providers you are happy to have answer the same
lookups. Rotation goes by turn, not by speed, so a tier carries its number of
providers times its slowest member's rate. Four instances paced at one call a
second carry four lookups a second between them. Add a fifth that is faster,
say 5/s, and it only adds one: five members means each gets one turn in five,
so the group moves at five a second. Put providers in a tier only when they are
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

The `provider` label on the metrics is the instance `name`, so two instances of
one type show up separately. Startup logs the instance names too; per-request
log lines carry no provider.

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
