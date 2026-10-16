# AGENTS.md

## Go skills: load them, every time

Before any Go coding, review, debugging, troubleshooting, or setup task, load `samber/cc-skills-golang@golang-how-to` first (it routes to the other Go skills the task needs) **and** load the full `## Required Go skills` list below. These rules are not optional:

- **The primary agent loads them.** Loading `golang-how-to` alone is not enough. Load the whole required list at the start of the task, before reading code, reviewing a diff, or writing a line of Go.
- **Every subagent loads them too.** When delegating any Go task, pass the full list in the delegation's `load_skills`. A delegation that omits them is not ready to send.
- **Never drop skills to save context.** Skills are load-bearing; prompt size is not.
- **A Go task is not started until the skills are loaded.** Working without them is a process failure, not a shortcut.

## Required Go skills

The following Go skills from `samber/cc-skills-golang` MUST always be applied when working on this project. Load them at the start of every Go-related task, and pass the same list to every subagent you delegate Go work to.

- `samber/cc-skills-golang@golang-code-style`
- `samber/cc-skills-golang@golang-concurrency`
- `samber/cc-skills-golang@golang-context`
- `samber/cc-skills-golang@golang-continuous-integration`
- `samber/cc-skills-golang@golang-data-structures`
- `samber/cc-skills-golang@golang-design-patterns`
- `samber/cc-skills-golang@golang-documentation`
- `samber/cc-skills-golang@golang-error-handling`
- `samber/cc-skills-golang@golang-hexagonal-arch`
- `samber/cc-skills-golang@golang-lint`
- `samber/cc-skills-golang@golang-modernize`
- `samber/cc-skills-golang@golang-naming`
- `samber/cc-skills-golang@golang-observability`
- `samber/cc-skills-golang@golang-safety`
- `samber/cc-skills-golang@golang-security`
- `samber/cc-skills-golang@golang-structs-interfaces`
- `samber/cc-skills-golang@golang-testing`
- `samber/cc-skills-golang@golang-troubleshooting`

## Architecture: hexagonal, imports point inward

The core (`internal/core/`) is technology-agnostic geocoding: domain types, ports, and the router service. It MUST NOT import any adapter package, `net/http`, or a framework. `depguard` enforces this in golangci-lint; a violation fails `mise run check`.

- **Ports live in the core** (`internal/core/port/`) because the core is the consumer. Incoming port: what primary adapters call. Outgoing ports: what the core calls out to (upstream geocoders, cache, metrics).
- **Primary adapters** (`internal/adapter/primary/`) translate HTTP into core calls: `photon` speaks the Photon API shape Dawarich expects; `ops` serves health and metrics.
- **Secondary adapters** (`internal/adapter/secondary/`) implement outgoing ports: one package per upstream provider, the in-memory cache, the Prometheus metrics.
- **Composition root**: `cmd/geocoder-proxy/` wires concrete adapters into ports with manual constructors. No package globals.

### Adding a provider

One new package under `internal/adapter/secondary/provider/<name>/` that implements `port.Geocoder`, translates the provider's response into `domain` types, owns its configuration type and validation, and self-registers:

```go
func init() {
 registry.Register("<name>", registry.Registration{
  NewConfig: func() registry.Config { return &Config{} },
  Build: func(c registry.Config) (port.Geocoder, error) {
   cfg, ok := c.(*Config)
   if !ok || cfg == nil {
    return nil, fmt.Errorf("<name>: unexpected config type %T", c)
   }
   return New(cfg)
  },
 })
}
```

A provider is fully self-contained: its `Config` struct (koanf tags, `Validate()`, `Rate()`/`Quota()`), and its construction live in its own package. No other package names a provider's fields; the composition root decodes operator config into the registered type, so a new field on an existing provider changes nothing outside its package. Shared rules stay shared: every `Validate()` composes `registry.ValidateLimits` and `registry.ValidateBaseURL` with `errors.Join`, so one startup error lists every problem. The `all_test` conformance suite enforces this for every registered provider.

Then add one blank import to `internal/adapter/secondary/provider/all/` and one row to `providerKeys` in `all/all_test.go`: the only two edits outside the new package.

### Several instances and tiers

A provider type can appear more than once in `providers`. Each entry carries a unique `name`; `type` names the registered provider and defaults to `name`, so the registry stays keyed by type and the composition root resolves a (name, type) pair into `service.Provider{Name, Tier, ...}`. The router labels metrics and logs with the instance name, so two instances of one type stay apart.

Lookups rotate only within a tier: `Router.orderedProviders` ring-rotates the providers of each tier, tiers in order, so successive lookups start at a different provider instead of queueing behind the first. A later tier is reached only after every provider before it failed or ran out of quota, which keeps a small-quota public rescue service from taking a steady share of a bulk import. Rotation is opt-in: the config loader gives an entry with no `tier` a tier of its own, in list order, so a config that names several providers without tiers is tried strictly in order exactly as before, and only providers that share an explicit `tier` rotate. `Provider.Tier` is 1-based; the router treats 0 as 1. Quota and pacing stay per instance.

An empty answer ends the walk, within a tier as across tiers: the first provider to answer "nothing here" settles the lookup and its answer is cached for the cell. A tier is therefore a set of providers of comparable coverage, since any member may decide a cell.

### Conventions

- **Compile-time interface guards.** Every adapter carries `var _ port.Geocoder = (*Client)(nil)` next to the type. Silent interface drift must be a build failure.
- **Never answer empty on failure.** An empty result is only correct when a provider genuinely answered "nothing here". Upstream failure propagates as an error. The Photon adapter maps it to **503, not 502**: the geocoder gem Dawarich uses raises only on 400/401/402/429/503, and a JSON-bodied 502 parses into the empty result that Dawarich then marks as final. Dawarich treats a blank answer as final and never retries the point.
- **Every lookup has a budget.** `GEOCODER_REQUEST_TIMEOUT` (default 4s, below Dawarich's 5s) is enforced by the HTTP layer: a lookup that runs out of time is answered 503, and a caller who hung up gets nothing and is not logged as an error. `GEOCODER_UPSTREAM_TIMEOUT` (default 2s) caps one provider attempt and may not exceed half the budget, so there is time left to try the next provider. An attempt the cap or the budget cut counts as a provider `error`. Deadline versus cancellation lives in `internal/adapter/primary/photon/photon.go`; the per-attempt cap is `service.WithAttemptTimeout`.
- **Terminal upstream errors are not retried.** `httpfetch.Do` retries transient failures (transport errors, 5xx, a 429 meaning "slow down") at most `maxRetries` times with bounded backoff, honouring `Retry-After` up to 5 seconds. A provider package may pass a `httpfetch.TerminalCheck` so a 429 carrying a cap code returns at once: the provider is asked once and the router fails over, instead of retries burning its remaining allowance. The check lives in the provider package, which owns its upstream's error vocabulary; `httpfetch` stays free of it. A terminal cap is still an error, so the never-blank rule holds and the outcome label stays `error`. Today only the Photon adapter (ChibiGeo) passes one, for `limit_exceeded`, `quota_exceeded` and `subscription_past_due`.
- **Request-level result counter.** `geocoder_lookups_total{result="ok|empty|failed"}` (`service.Router.recordLookup`) says what the caller got regardless of why, so an outage that records no provider `error` (every provider pacing-limited) still alerts on `failed`. Keep the five provider outcome values and their labels as they are.
- **No coordinates in logs or metric labels.** Location history is PII; cardinality rules aside, raw coordinates never appear in telemetry. Provider adapters must also return coordinate-free errors, because the router wraps provider errors verbatim.
- **Provider adapter contract** (full text in `internal/core/port/geocoder.go`): an empty slice with a nil error is a genuine "nothing here"; any failure, including "operation unsupported", must be an error. Populate `Address.CountryCode` whenever the provider returns one: providers localise country names, and the Photon adapter canonicalises from the code so failover cannot fragment country attribution downstream.
- **Config layering**: flag > env (`GEOCODER_*`) > optional YAML > defaults, via koanf (all config structs carry `koanf:` tags). CLI via zulu (`github.com/zulucmd/zulu/v2`, a cobra fork: only the error-returning `RunE` hooks exist). Each provider owns the user-facing validation of its own config; panics in adapter constructors (`tokenbucket.New`, `service.New`) are the last-line backstop for programming errors, not the validation surface.

### The budget under load

The failover above assumes lookups arrive one at a time. Each provider has its own pace limit (`rate_per_second`, 1/s in the homelab deployment), so lookups arriving together queue for a token: within a single-provider tier, with ten in flight the last one waits around nine seconds, spends the whole budget before its attempt starts, and the caller gets a 503. Rotation spreads that queue across a tier's members instead, so the wait shrinks as the tier grows. Dawarich retries those, which beats both waiting past its own timeout and asking a provider for more than it allows.

A 503 can also come back early, well inside the budget. When a provider would have to wait past the deadline for its token it declines at once, and if every provider does that the lookup fails without any attempt being made.

## Commit gate: prek

Run `mise run hooks-install` once per checkout so `.git/hooks/pre-commit` exists, then `mise run check` before every commit. A clean `mise run check` is the commit gate. Never bypass the hooks (`git commit --no-verify`).

## Git workflow

- All work on a feature branch (`feat/...`, `fix/...`), never on `main`.
- Behaviour changes happen in a worktree (`.worktrees/<slug>`, gitignored), not in this checkout.
- Commits are always signed: `git commit -S`.
- Push the branch and open a PR. **Never merge**; the merge is the human's call.

## gh commands

Always pass `--repo hoshsadiq/geocoder-proxy` to `gh` when the checkout's default repo resolution is ambiguous.
