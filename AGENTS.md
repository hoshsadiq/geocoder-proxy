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

One new package under `internal/adapter/secondary/provider/<name>/` that implements `port.Geocoder`, translates the provider's response into `domain` types, and self-registers:

```go
func init() { registry.Register("<name>", New) }
```

Then add one blank import to `internal/adapter/secondary/provider/all/`. This is the only place `init()` is acceptable: provider registration follows the `database/sql` driver idiom, and the `all` package makes the side effects visible in a single import block. Everywhere else, use explicit constructors.

### Conventions

- **Compile-time interface guards.** Every adapter carries `var _ port.Geocoder = (*Client)(nil)` next to the type. Silent interface drift must be a build failure.
- **Never answer empty on failure.** An empty result is only correct when a provider genuinely answered "nothing here". Upstream failure propagates as an error. The Photon adapter maps it to **503, not 502**: the geocoder gem Dawarich uses raises only on 400/401/402/429/503, and a JSON-bodied 502 parses into the empty result that Dawarich then marks as final. Dawarich treats a blank answer as final and never retries the point.
- **No coordinates in logs or metric labels.** Location history is PII; cardinality rules aside, raw coordinates never appear in telemetry. Provider adapters must also return coordinate-free errors, because the router wraps provider errors verbatim.
- **Provider adapter contract** (full text in `internal/core/port/geocoder.go`): an empty slice with a nil error is a genuine "nothing here"; any failure, including "operation unsupported", must be an error. Populate `Address.CountryCode` whenever the provider returns one: providers localise country names, and the Photon adapter canonicalises from the code so failover cannot fragment country attribution downstream.
- **Config layering**: flag > env (`GEOCODER_*`) > optional YAML > defaults, via koanf. CLI via zulu (`github.com/zulucmd/zulu/v2`, a cobra fork: only the error-returning `RunE` hooks exist).

## Commit gate: prek

Run `mise run hooks-install` once per checkout so `.git/hooks/pre-commit` exists, then `mise run check` before every commit. A clean `mise run check` is the commit gate. Never bypass the hooks (`git commit --no-verify`).

## Git workflow

- All work on a feature branch (`feat/...`, `fix/...`), never on `main`.
- Behaviour changes happen in a worktree (`.worktrees/<slug>`, gitignored), not in this checkout.
- Commits are always signed: `git commit -S`.
- Push the branch and open a PR. **Never merge**; the merge is the human's call.

## gh commands

Always pass `--repo hoshsadiq/geocoder-proxy` to `gh` when the checkout's default repo resolution is ambiguous.
