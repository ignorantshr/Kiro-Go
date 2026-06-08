# CONTEXT.md

This file provides guidance to coding agents when working with code in this repository.

## What this is

Kiro-Go is a reverse proxy that exposes **Kiro accounts** (backed by AWS CodeWhisperer / Amazon Q `generateAssistantResponse`) as **OpenAI- and Anthropic-compatible APIs**. Clients call standard `/v1/messages` or `/v1/chat/completions`; the proxy picks an account from a pool, translates the request into the Kiro/AWS wire format, calls the AWS upstream, and decodes the binary AWS Event Stream back into SSE.

No third-party Go dependencies beyond `github.com/google/uuid` — everything (HTTP, JSON, the event-stream decoder) is hand-rolled on the stdlib.

## Agent skills

Issue-tracker and triage-label workflows are intentionally not configured for this repo.

### Domain docs

This repo uses a single-context domain-doc layout: look for `CONTEXT.md` at the repo root and ADRs under `docs/adr/` when present. See `docs/agents/domain.md`.

## Commands

```bash
go build -o kiro-go .          # build
./kiro-go                      # run (serves :8080, admin at /kiro_admin)
go test ./...                  # all tests
go test ./proxy/ -run TestName # single test (most tests live in ./proxy)
go vet ./...                   # vet
gofmt -l .                     # list unformatted files

docker-compose up -d           # run in Docker (mounts ./data for persistence)

# prebuilt deploy: cross-compile locally, ship only the binary to a remote Docker host
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o kiro-go .  # build for the target host
./deploy/deploy.sh user@host [remote-dir]                    # build + scp binary + remote `compose up -d --build`
```

`deploy/` holds the prebuilt-deploy assets: a thin `Dockerfile` that only `COPY`s the binary (no Go toolchain, no source, no `web/` — all embedded), a `docker-compose.yml`, and `deploy.sh`. The first time, manually upload `deploy/Dockerfile` + `deploy/docker-compose.yml` to the remote dir; thereafter `deploy.sh` only ships the freshly cross-compiled binary and re-runs `compose up -d --build`. `ca-certificates` in the thin image is required for the binary's TLS to AWS upstream.

There is **no Go CI** — only a Docker image build (`.github/workflows/docker.yml`). Run `go build`, `go test ./...`, and `go vet ./...` locally before committing; nothing else will catch breakage.

Note: `go.mod` declares `go 1.21` but the Dockerfile builds with `golang:1.23-alpine`. Stay compatible with 1.21 language features.

## Configuration & runtime data

- Config is a single JSON file, default `data/config.json` (override with `CONFIG_PATH`). It is created with defaults on first run and **rewritten by the app** whenever settings/accounts/stats change, so don't hand-format it.
- It stores account access/refresh tokens in plaintext, plus all server settings, API keys, and persisted stats. `data/` is gitignored.
- `ADMIN_PASSWORD` env var overrides the config password at startup. `LOG_LEVEL` overrides the configured log level.
- `config.Get*/Update*` are the only sanctioned way to touch config — they hold a `sync.RWMutex` and persist to disk. Don't read/write the file directly.

## Architecture

### Request lifecycle (the core path)

`main.go` → `proxy.NewHandler()` (starts background goroutines) → `Handler.ServeHTTP` routes by path.

For an inference request (`handleClaudeMessages` / `handleOpenAIChat` / `handleOpenAIResponses`):

1. **Authenticate** against configured API keys (`auth.go`). Auth is a master switch (`RequireApiKey` / presence of `ApiKeys`); when off, requests pass unauthenticated.
2. **Translate** the client request into a `*KiroPayload` (`translator.go`: `ClaudeToKiro` / `OpenAIToKiro`).
3. **Retry loop** up to `maxAccountRetryAttempts` (=3): pick an account via `pool.GetNextForModelExcluding`, ensure its token is valid (`ensureValidToken`, refreshing if needed), then `CallKiroAPI`.
4. On failure, `handleAccountFailure` (`account_failover.go`) classifies the error *by string matching the message* (quota/overage/suspension/auth) and either cools down or disables the account, then the loop tries the next account.
5. On success: `pool.RecordSuccess` clears cooldown, `pool.UpdateStats` accumulates usage.

### Package responsibilities

- **`config/`** — thread-safe JSON store. Defines `Account`, `ApiKeyEntry`, `Config`. Handles **backward-compat migrations on load** (legacy single `apiKey` → `ApiKeys[]`; `allowOverage` → `OverageStatus`; `sanitizeClaudeCodePrompt` → `FilterClaudeCode`). When adding a config field, follow the existing migrate-on-load + omitempty pattern.
- **`pool/`** — weighted round-robin account pool (singleton via `GetPool`). The pool holds a *weight-expanded* slice (`Reload` duplicates entries by `Weight`). Selection skips accounts that are cooling down, near token expiry (`tokenRefreshSkewSeconds`=120s), quota-blocked, or lacking the requested model. `RecordError` cools down 1h for quota errors, 1min after 3 consecutive other errors.
- **`auth/`** — OAuth login + token refresh. Three login methods: Builder ID (device-code flow), IAM Identity Center / enterprise SSO, and raw SSO token import.
- **`proxy/`** — the bulk of the code (see below).
- **`logger/`** — leveled logger (debug/info/warn/error), level set once at startup.
- **`web/`** — vanilla-JS admin panel (no build step) + Tailwind via CDN vendor copy. Strings live in `web/locales/{en,zh}.json`. `index.html` is the live panel; `index-legacy.html` is a fallback. The whole dir is **embedded into the binary via `//go:embed all:web` in `main.go`** and injected into `NewHandler(fs.FS)`, so the binary is self-contained — editing `web/` files requires a rebuild to take effect. `serveAdminPage` reads `index.html` directly from the embed FS (not via `http.FileServer`, which would 301 `/index.html`→`./`); `serveStaticFile` serves the rest through `http.FileServer`.

### Inside `proxy/`

- **`handler.go`** (~3k lines) — HTTP routing, all endpoint handlers (inference, `/v1/models`, `/v1/stats`, health), the `/kiro_admin/api/*` admin API, and the background goroutines (`backgroundRefresh` every 30min refreshes tokens/usage; `backgroundStatsSaver` every 30s persists stats).
- **`translator.go`** (~2k lines) — request/response format conversion. This is where most domain complexity lives (see invariants below).
- **`kiro.go`** — upstream call (`CallKiroAPI`) and the hand-written **AWS Event Stream decoder** (`parseEventStream`: 12-byte prelude → headers → payload framing). Defines the `KiroPayload` wire types and `KiroStreamCallback`.
- **`kiro_api.go`** — REST calls to CodeWhisperer (`GetUsageLimits`, `GetUserInfo`, `ListAvailableModels`).
- **`kiro_headers.go`** — spoofs the headers a real Kiro IDE client sends (`KiroClientConfig`: Kiro/system/node versions).
- **`account_failover.go`** — error classification + account disable/cooldown decisions.
- **`cache_tracker.go`** — tracks Anthropic-style prompt-cache usage for reporting.
- **`responses_*.go`** — OpenAI Responses API (`/v1/responses`), including `previous_response_id` history stored on disk and purged after `responsesDefaultTTL`.

### Multi-endpoint fallback

`CallKiroAPI` tries up to three upstream endpoints in order (Kiro IDE → CodeWhisperer → Amazon Q), reordered by `config.PreferredEndpoint`. A `429` rotates to the next endpoint; `401/403/402` return immediately (no point retrying). The account-level retry loop in the handler is a separate layer *above* this endpoint loop.

## Non-obvious invariants (read before editing the translator)

- **Single active tool turn.** The Kiro upstream accepts exactly one active tool exchange: the last assistant message's structured `toolUses` must correspond to the current message's `toolResults`. Any other historical tool calls/results must be flattened into plain text (`sanitizeKiroHistory`, `narrateToolResults`). Orphaned tool results (e.g. after client-side compaction) get folded into the current message text instead of attached structurally. Breaking this causes upstream `400`s.
- **Payload size cap.** `maxPayloadBytes` (≈900KB, below the upstream `CONTENT_LENGTH_EXCEEDS_THRESHOLD`). `truncatePayloadToLimit` drops the oldest history turns (keeping system priming, the active tool turn, and the most recent `minRecentHistoryTurns`) and inserts a `truncationPlaceholder`.
- **System prompt becomes history.** System instructions aren't sent as a system field; they're prepended as a `user → "I will follow these instructions."` priming pair in history.
- **Prompt filtering** (`applyPromptFilters`, gated by config flags): detects the Claude Code CLI built-in system prompt and replaces it with a compact one, strips env-metadata noise, removes boundary markers, and runs user-defined regex/line rules — all to cut token usage. The `FilterClaudeCode` heuristic (`isClaudeCodeSystemPrompt`) matches specific marker text; keep it in sync if upstream prompt wording changes.
- **Model name resolution** (`ParseModelAndThinking` / `MapModel`): strips the thinking suffix (default `-thinking`), applies explicit aliases for dated/legacy/non-Anthropic names, then normalizes `claude-{family}-N-M` → `claude-{family}-N.M` via regex so new versions need no code change.
- **Overage routing.** Whether an over-quota account keeps serving is decided by the per-account upstream `OverageStatus=="ENABLED"` switch OR the global `AllowOverUsage`. `isQuotaBlocked` is the single source of truth — used in both `Reload` and live selection.

## Conventions

- Tests sit next to the code as `*_test.go`, concentrated in `proxy/`. Most translator behavior is locked down by table tests — when changing conversion logic, expect to update them and add cases.
- Comments and commit messages mix English and Chinese; both are fine. User-facing docs are bilingual (`README.md` / `README_CN.md`).
- `docs/plans/` holds design docs for unbuilt features (e.g. `API_KEY_ACCOUNT_AFFINITY_PLAN.md` describes API-key→account binding that is **not yet implemented** — `ApiKeyEntry` has no `BoundAccountIDs` field yet).

## Security defaults to be aware of

Inference endpoints default to **no auth** (`RequireApiKey: false`) with `Access-Control-Allow-Origin: *`, and the default admin password is `changeme`. These are intentional for easy local/Docker startup but must be tightened for any public deployment. Don't silently change these defaults without flagging it.
