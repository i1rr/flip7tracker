> **Archived:** 2026-06-27
> **Summary:** ---

---
plan_executor:
  test_cadence: per_batch
  review_depth: medium
  review_angles: [arch, bugs, security, gaps]
---

# flip7bot — Migration: Rust/teloxide (Telegram) → Go (Mattermost)

Port flip7bot to a Go Mattermost bot at feature parity with the existing Rust/teloxide Telegram bot, copying jobHunter/mmbot's architecture (Client4 + net/http + HMAC nav-state), preserving the existing SQLite data, and adopting a thread-per-game live scoreboard.

## Overview

flip7bot is a score tracker for the physical card game Flip7. One host drives the bot in a single channel: adds players, enters each hand's score, and the bot keeps a **live scoreboard edited in place**, resolving the winner at a manual **End Game** (top score ≥ 200 → winner(s) + Elo update; < 200 → game discarded), and maintaining Elo ratings + a Hall of Fame.

The migration replaces every Telegram mechanism with its Mattermost equivalent while preserving all game logic, statistics, and the existing `flip7.db`:

- Inline keyboards + `callback_query` → interactive **buttons** in post attachments, dispatched to `POST /action`, with **HMAC-signed nav-state** in each button's `context`.
- `edit_message_text` (live scoreboard) → `Client4.UpdatePost` / `UpdateAttachment` on the stored post id.
- Force-reply text entry (name, points, rename) → interactive **dialogs** (`OpenInteractiveDialog` via a short-lived `trigger_id`), carrying signed `state`.
- The teloxide dialogue `State` FSM in `InMemStorage` is **eliminated**: persistent state lives in SQLite (re-read per interaction by `game_id`), transient UI state lives in signed button `context`, text steps live in signed dialog `state`.
- The five Telegram commands (`/start`, `/menu`, `/cancel`, `/help`, `/scoreboard`) collapse to one slash command `/flip7` with optional arg routing (`/flip7` = menu, `/flip7 scoreboard`, `/flip7 help`). `/cancel` has no slash equivalent — it becomes a **Close/Back button** that deletes or re-renders the post (jobHunter's Close pattern), since the stateless design has no conversation to cancel.

### Decisions confirmed for this migration (do not re-litigate)

- **Strict feature parity** with the *current Rust behavior*, which is authoritative wherever the original migration prose differs:
  - **No auto-end at 200.** The winner is resolved only at a manual **End Game**. On end-confirm: if the maximum total < 200 the game is **discarded** (rows deleted, no Elo); if ≥ 200 the top scorer(s) win, `game_winners` + `rating_history` are written, and Elo updates.
  - **Typed-only score entry.** Tap a player → a dialog → type points (0–999). No quick-score buttons.
- **Thread-per-game.** Each new/resumed game `CreatePost`s a root **scoreboard post**; that post id is the game's thread root, stored on the game row. The scoreboard root is `UpdatePost`'d in place on every score/edit; per-entry confirmations post as **replies** in the thread (`RootId` = scoreboard post id).
- **No in-memory game sessions.** Every interaction re-reads SQLite by `game_id` carried in the signed context and re-renders — a game is never held in memory between taps, so no "unload after N minutes" timer is needed. If a future ephemeral cache is ever added it must carry a ≤ 10-minute TTL keyed by owner id, but the design does not require one.
- **Idempotent mutating actions.** Because action `context` has no max-age, every scoreboard/confirm button stays valid for the game's whole life and can be double-tapped or replayed. All mutating confirm actions must therefore be idempotent against current DB state: end-game/discard finalize only `WHERE finished_at IS NULL` (a second tap is a no-op that just re-renders), and edit-last deletes a **specific score-entry id** carried in the confirm button's signed context (`DELETE … WHERE id = ?`), so a replay cannot delete a different (newer) entry.
- **Data preserved.** The existing `flip7.db` carries forward; migrations are additive/transforming, never destructive. Pre-migration unfinished games stay resumable (post id nulled, relisted in the owner channel).
- **Deployment: Docker Compose on the Mattermost bridge** (deliberate deviation from jobHunter's host `systemd --user` unit).
- **Single owner-only channel**, `user_id == owner` guard, per-command bearer token — exactly as jobHunter.

## Tech Stack

All versions verified stable and actively maintained (checked 2026-06-26).

| Component | Choice | Version | Rationale |
|---|---|---|---|
| Language | Go | toolchain 1.26.4; `go.mod` declares `go 1.24` | Latest stable Go; ≥1.24 required by the Mattermost client module |
| Mattermost client | `github.com/mattermost/mattermost/server/public` (`model.Client4`) | v0.4.2 | Same outbound client jobHunter uses; version independent of the running server |
| Inbound HTTP | `net/http` (stdlib) | — | Identical to jobHunter; no framework |
| SQLite driver | `modernc.org/sqlite` (pure Go, cgo-free) via `database/sql` | v1.53.0 | Static musl/Alpine build with no cgo — fixes the Rust build's libsqlite3 pain |
| Migrations | `embed.FS` + a small in-house runner | — | Plain `.sql` files run at startup; no `sqlx::migrate!`, no golang-migrate dependency |
| Config / .env | `github.com/joho/godotenv` + stdlib env parsing | v1.5.1 | Same as jobHunter; missing `.env` is non-fatal (env may be set by compose) |
| Crypto | stdlib `crypto/hmac` + `crypto/sha256` + `crypto/subtle` | — | HMAC-SHA256 nav-state, constant-time compare — copied from jobHunter `mm/sign.go` |
| Build image | `golang:1.26.4-alpine` (builder) → `alpine:3.24` (runtime) | — | Multi-stage static binary; runtime adds `ca-certificates` + `wget` (healthcheck) |
| Deployment | Docker Compose on the existing MM bridge (`172.28.0.0/16`) | — | Container joins the MM external network with a fixed IP, inbound port 8068 not published to host |

> The Mattermost client-lib version is independent of the running MM server (which runs a `latest` image). Re-check client/server compatibility before any MM server upgrade.

## Context

### Reference architecture (copy from jobHunter/mmbot)

The blueprint is `~/jobHunter/mmbot`. Copy these patterns wholesale, adapting the domain:

- **`Poster` / `AdminAPI` / `PostUpdater` interfaces over `Client4`** so handlers depend on small interfaces and can be unit-tested with fakes (`var _ AdminAPI = (*model.Client4)(nil)`).
- **HMAC-SHA256 nav-state** (`mm/sign.go`): `json.Marshal(NavState)` → `base64url(payload) + "." + base64url(hmac)`; the MAC covers the **exact transmitted JSON bytes** (verify recomputes over the same bytes — no re-marshal). Constant-time verify via `hmac.Equal`. **No max-age on action `context`** (a game's scoreboard buttons must verify for the game's whole life); optional max-age on dialog `state`.
- **Fail-fast config** that accumulates *all* problems into one error, and asserts `HMAC_KEY ≥ 32 bytes` at both load and `Signer` construction. `Redacted()` exposes secrets only as boolean `*_set` presence flags for logging.
- **Three-layer guard:** (1) HMAC over nav-state (server-only key, never placed in a post), (2) network scoping (the inbound port is reachable only on the Docker bridge, never published to the host), (3) owner `user_id == owner` check on every `/slash`, `/action`, `/dialog`.
- **`/healthz`** returns a static `200 ok`, echoing nothing. Never log raw bodies, action `context`, dialog `state`, or any secret.
- **`register.sh`** registers the slash command via `mmctl --local` (`docker exec` into the MM container) and captures the slash token into `.env` with `awk` (preserving `chmod 600`).

Key departure: jobHunter keeps durable state in a JSON file and shells out to Python for DB access — it links **no** SQLite in Go. flip7bot instead owns a real `database/sql` + `modernc.org/sqlite` layer (the original Rust schema + queries ported), so the `internal/db` package is net-new versus the reference.

### State strategy

- **Persistent game state → SQLite** (already there): `games`, `game_players`, `score_entries`, `game_winners`, `rating_history`, `players.rating`. A screen re-renders by reading the DB for `game_id`, never from memory.
- **Transient UI state → signed button `context`**: in-progress New-Game player selection (`players` list), pagination pages, and confirm steps all encode their payload into the HMAC-signed `context` of the buttons that render them. No `InMemStorage`.
- **Text entry → dialogs**: typing a player name, entering points, renaming carry the `game_id`/`player_id`/return-screen in the dialog's signed `state`. On submit, the handler writes the DB and `UpdatePost`s the originating post (a `SubmitDialogResponse` cannot update a post — re-render with a separate `UpdateAttachment` using the `post_id` carried in the signed state).

### Nav-state grammar (every former callback)

Port every teloxide callback prefix to a signed `NavState` action discriminator. Source grammar (from `src/keyboards/*` and inline builders):

- `menu:new_game | load_game | stats | players`
- `setup:add_new | known | back | start | disabled`, `player:remove:{player_id}`
- `known:add:{player_id} | page:{page} | back | start | noop`
- `score:player:{game_id}:{player_id}`, `game:edit:{game_id} | end:{game_id} | end_confirm:{game_id} | end_cancel:{game_id}`
- `edit:confirm:{game_id} | cancel:{game_id}`
- win/post-game nav: `game:stats | new | home`
- `game:load:{game_id}`, `load:back`
- `mgmt:rename:{player_id} | delete:{player_id} | page:{page} | noop | back | delete_confirm:{player_id} | delete_cancel`
- `stats:player:{player_id} | page:{page} | back | back_to_list`

These become compact `NavState` fields (action code + a small set of typed payload fields: `game_id`, `player_id`, `entry_id` (the score-entry id for idempotent edit-last), `page`, and a `players []int64` list for new-game setup). Keep payloads compact; there is no 64-byte limit but small contexts keep posts lean.

**Avoiding an import cycle (router composition).** `internal/nav` owns ONLY the grammar — the `NavState` codes, the `mm.Signer`-backed encode/decode, and the `button(label, ns)` helper. It must **not** import the screen packages (`newgame`, `game`, `players`, `stats`), or a cycle forms (screens import `nav` for the grammar; `nav` would import screens to dispatch). Instead, copy jobHunter's composition: each screen package exposes an `Owns(action) bool` predicate plus its handler entry points and imports only `nav` + `mm` + `db`. The central `/action` dispatcher lives in `cmd/mmbot/main.go` (which legitimately imports everything): it decodes the verified `NavState` and routes to the first package whose `Owns` returns true (a `combinedAction` over the screen handlers). Dependency direction stays acyclic: `nav ← screens ← main`.

### Schema migration (data-preserving) — `005_mattermost_ids.sql`

The current schema stores **Telegram integer ids** that become **Mattermost strings**:

- `games.message_id INTEGER` → `games.post_id TEXT` (the live scoreboard post id; nullable).
- `games.chat_id INTEGER` → `games.channel_id TEXT` (the MM channel id).
- `chat_last_players.chat_id INTEGER` → `chat_last_players.channel_id TEXT`.

SQLite cannot change a column type in place, so `005` **rebuilds** the affected tables with `TEXT` columns. Two SQLite behaviors are load-bearing here and were verified against the official docs:

1. **`PRAGMA foreign_keys` is a no-op inside an open transaction.** So the runner cannot disable FK enforcement *after* `BEGIN`. The table rebuild MUST follow SQLite's documented 12-step procedure: on a **dedicated single connection**, `PRAGMA foreign_keys=OFF` (outside any transaction) → `BEGIN` → create the new table → `INSERT … SELECT` (transform) → `DROP TABLE` old → `ALTER TABLE … RENAME` → recreate indexes → `PRAGMA foreign_key_check` (abort if it reports rows) → `COMMIT` → `PRAGMA foreign_keys=ON`. Because `games` is referenced by `game_players`/`score_entries`/`game_winners`/`rating_history`, doing this with FKs left ON would either cascade-delete or abort. The embedded runner (Step 6) therefore runs the **whole migration batch on one connection that has `foreign_keys=OFF` for the duration**, runs `foreign_key_check` at the end, and only then restores `ON` — never relying on a per-transaction pragma.
2. **AUTOINCREMENT must be re-declared.** The rebuilt `games` (and any AUTOINCREMENT table) must re-declare `INTEGER PRIMARY KEY AUTOINCREMENT`; after the `INSERT … SELECT` copies explicit ids, SQLite updates the `sqlite_sequence` row to the max id, so new ids continue above the high-water mark (no id reuse/aliasing of FK history).

Legacy Telegram ids are meaningless under Mattermost, so:

- Set every legacy `post_id` to `NULL` (forces a fresh `CreatePost` on next render/resume).
- Set every legacy `channel_id` to `''` (empty) inside `005`. The real channel id is not known at SQL-file authoring time, so a **startup backfill** (after `mm.Resolve` yields the owner channel id — Step 21) runs the idempotent `UPDATE games SET channel_id = ? WHERE channel_id = ''` (and likewise for `chat_last_players`), claiming legacy rows into the configured owner channel. This keeps `GetUnfinishedGames(channelID)` filtering by `channel_id` intact **and** makes pre-migration unfinished games listable/resumable — closing the contradiction between the blank backfill and the ported channel filter.
- Preserve **all** `players`, `score_entries`, `game_winners`, `rating_history`, and `players.rating` rows untouched — that is the valuable history.

**Reconciling with the existing sqlx-migrated DB (critical).** The live `flip7.db` was migrated by `sqlx`, which records applied migrations in its own `_sqlx_migrations` table and already has the post-004 schema (including `players.rating`). A naive new `schema_migrations` table would be empty on first Go startup and the runner would re-apply 001–004 — and `004`'s `ALTER TABLE players ADD COLUMN rating` would fail with "duplicate column name", crashing on the production DB. The runner therefore must, at startup: if `_sqlx_migrations` exists and is non-empty, **seed `schema_migrations` as having already applied 001–004** (the migrations sqlx ran) so only `005` runs; if neither tracking table exists, run all five on a fresh DB. Add a startup self-check (e.g. confirm `players.rating` exists) before attempting `005`, and document the version-numbering alignment between the sqlx descriptions and the embedded file names.

Migrations 001–004 are copied verbatim so a *fresh* DB builds to the same pre-005 shape; 005 then transforms. A **parity test** runs all migrations against a fixture that mimics the sqlx-migrated production shape (including a populated `_sqlx_migrations`) and asserts row counts for `players`/`score_entries`/`game_winners`/`rating_history` are unchanged, a few known `players.rating` values survive, `post_id IS NULL`/`channel_id=''` post-005, and the new columns are `TEXT`. Run migrations at startup via the embedded runner. Keep the DB on the existing `flip7_data` volume at `/data/flip7.db`.

**Back up before the first migration & rollback (WAL-aware).** The destructive 005 rebuild runs at the **first** container startup against the real `flip7_data` volume, so a backup MUST be taken *before* that first start, not only at cutover. **The Rust bot runs SQLite in WAL mode** (`src/db/mod.rs`: `SqliteJournalMode::Wal`), so the live volume carries `flip7.db-wal`/`flip7.db-shm` sidecars that may hold committed-but-un-checkpointed games — a plain `cp flip7.db` would silently drop them. The backup therefore must be WAL-safe: with the Rust container **stopped**, either (a) checkpoint then copy — `sqlite3 /d/flip7.db "PRAGMA wal_checkpoint(TRUNCATE);"` and copy the resulting single `flip7.db`, or (b) copy **all three** files (`flip7.db`, `flip7.db-wal`, `flip7.db-shm`) together. Because 005 transforms `chat_id`/`message_id` to `TEXT`, the retained Rust binary can no longer read the DB after migration — so **rollback is "restore the pre-migration backup + redeploy the Rust bot", not "just redeploy Rust"**. Critically, the rollback restore must **first delete the post-migration `flip7.db-wal`/`flip7.db-shm` sidecars** on the volume before dropping the backup back in place; otherwise SQLite replays the stale Go-era WAL over the restored Rust file and corrupts it. This procedure is documented in Step 24; the Rust tree is kept (Step 26) only as the code half of that rollback.

### Deployment topology

- Attach the `flip7bot` service to the Mattermost stack's bridge as an **external network** with a **fixed IP** on `172.28.0.0/16` (outside jobHunter's `.1`/`.100`/`.200` reservations) so `INTEGRATION_BASE_URL` is stable.
- **Inbound:** listener binds `0.0.0.0:8068` *inside the container*; the port is published only to the bridge, never to the host. Port 8068 avoids MM's 8065/8066 and jobHunter's 8067. `INTEGRATION_BASE_URL=http://<flip7bot-bridge-ip>:8068`. **Network-scoping caveat vs the reference:** because the bind is `0.0.0.0` on a shared bridge (not a gateway-IP bind + host `ufw` as in jobHunter), *any* container on the MM bridge can reach the listener — so the network-scoping layer is weaker and the effective guards for an on-bridge peer reduce to HMAC + the owner check. This is an accepted residual risk for a single-user home server (documented in `mmbot/CLAUDE.md`), mitigated by: not publishing the port to the host, the owner-only channel, and HMAC over nav-state. If tighter scoping is wanted later, bind the container's fixed bridge IP instead of `0.0.0.0`.
- **Outbound:** `MM_URL` reaches the MM server over the bridge (MM container's bridge IP or service name, port 8065).
- **MM allow-list:** the stack's `MM_SERVICESETTINGS_ALLOWEDUNTRUSTEDINTERNALCONNECTIONS` already covers `172.16.0.0/12` ⊇ `172.28.0.0/16`, so no MM env change is expected.
- **External-network ordering:** the compose file references the MM stack's bridge as an **external** network, so the MM stack (and its named network) must already exist before `docker compose up`; the flip7bot service does not create it.
- Keep `restart: unless-stopped`, the `flip7_data` volume, and a `wget`-based `/healthz` healthcheck. Do not publish the listener port to the host.

### First-deploy provisioning (manual, before the bot can resolve)

`mm.Resolve` only *looks up* (never creates) the team/channel and validates `MM_BOT_TOKEN` via `GetMe` — so these must exist first or startup fails at `GetMe`/`GetChannelByName`. Before the first deploy: create the flip7 **bot account in Mattermost and generate its access token** (`MM_BOT_TOKEN`), create its **team** and **owner-only channel**, add the bot to both; then fill `.env` (`MM_URL`, `MM_BOT_TOKEN`, `MM_TEAM`, `MM_CHANNEL`, owner identity, `HMAC_KEY` from `openssl rand -hex 32`, `LISTEN_ADDR`, `INTEGRATION_BASE_URL`); then run `register.sh` to create `/flip7` and capture `SLASH_TOKEN_FLIP7`; **WAL-safe back up `flip7.db`** (checkpoint then copy, or copy all three `flip7.db*` files — see the rollback note); then `docker compose build && up -d`. This order is documented in `mmbot/CLAUDE.md` (Step 24).

## Success criteria

- `go build ./... && go vet ./... && go test ./...` pass and `gofmt -l .` prints nothing.
- The Elo rating port matches the Rust output within a tight float tolerance (`1e-9`) for known inputs (golden parity test) — not a byte-exact match, since Go `math.Pow` and Rust `f64::powf` may differ in the last ULP — and existing `players.rating` values continue from where they are.
- The schema migration preserves every `players`/`score_entries`/`game_winners`/`rating_history` row (parity test asserts counts + sample ratings), runs cleanly against the existing sqlx-migrated DB shape, and a pre-migration backup exists so the documented rollback (restore backup + redeploy Rust) is possible.
- A host can, from the owner-only channel: open the menu via `/flip7`; start a new game (picking known players and adding a new one via dialog) which posts a scoreboard thread; enter scores (typed via dialog) and watch the scoreboard update in place; edit the last entry; end the game so the ≥200 winner(s) are detected (incl. ties/multi-winner) with Elo + history written, or the <200 game is discarded; resume an unfinished game into a fresh thread; rename/delete players; view the Hall of Fame and per-player detail with preserved historical Elo.
- A tampered/forged `context` fails HMAC and yields a benign "expired" ephemeral; a non-owner `user_id` is rejected; `/healthz` answers `200 ok` from a bridge container but the port is not reachable from the LAN.
- The Rust tree is retired only at cutover, after the Go bot is validated, in a dedicated commit with history preserved.

## Implementation Plan

### Batch 1: Module scaffold & config

- [x] Step 1: Create the `mmbot/` Go module and project skeleton.
  - `cd ~/flip7bot && mkdir mmbot && cd mmbot && go mod init github.com/rivan/flip7bot/mmbot` (mirror jobHunter's module path style); set the toolchain to Go 1.26.4 but declare `go 1.24` in `go.mod` (the floor the Mattermost client requires).
  - Add dependencies pinned to the verified versions: `github.com/mattermost/mattermost/server/public v0.4.2`, `modernc.org/sqlite v1.53.0`, `github.com/joho/godotenv v1.5.1`. Run `go mod tidy`; commit `go.sum`.
  - Create the directory tree under `mmbot/`: `cmd/mmbot/`, `internal/{config,mm,server,db,nav,menu,game,newgame,players,stats,rating,scoreboard}/`, `internal/db/migrations/`, `scripts/`. The `.sql` files live under `internal/db/migrations/` (NOT a module-root `migrations/`) because `//go:embed` only matches files in the embedding `.go` file's own directory subtree — it cannot traverse to a parent directory.
  - Author `mmbot/.dockerignore` excluding `.env`, `*.db`/`*.db-wal`/`*.db-shm`, the compiled binary, `.git`, and any local scratch, so secrets and local DBs are never baked into image layers.
  - Add `Makefile` targets: `build` (`CGO_ENABLED=0 go build ./...`), `test` (`go test ./...`), `vet` (`go vet ./...`), `fmt` (`gofmt -l .`), `check` (build+vet+test+fmt-clean).
  - Update `.gitignore` to exclude `mmbot/.env`, `mmbot/mmbot` (binary), and any local `*.db`/`*.db-wal`/`*.db-shm`. Author `mmbot/.env.example` listing every variable (see Step 2) with placeholder values and comments.
- [x] Step 2: Implement `internal/config` — env loader + fail-fast validation.
  - `Config` fields: `MMURL` (`MM_URL`), `MMBotToken` (`MM_BOT_TOKEN`), `MMTeam` (`MM_TEAM`), `MMChannel` (`MM_CHANNEL`), `OwnerUsername` (`OWNER_USERNAME`), `OwnerUserID` (`OWNER_USER_ID`), `HMACKey` (`HMAC_KEY`), `SlashTokenFlip7` (`SLASH_TOKEN_FLIP7`), `DatabaseURL` (`DATABASE_URL`, default `/data/flip7.db`), `ListenAddr` (`LISTEN_ADDR`, default `0.0.0.0:8068`), `IntegrationBaseURL` (`INTEGRATION_BASE_URL`), `LogLevel` (`LOG_LEVEL`, default `info`). Drop the Rust `BOT_TOKEN`/`ALLOWED_CHAT_IDS`.
  - `Load(envPath)` stats the file, godotenv-loads it if present (missing is non-fatal), then reads env. `validate()` accumulates *all* missing-required problems into one joined error; requires at least one of `OWNER_USER_ID`/`OWNER_USERNAME`; asserts `len([]byte(HMACKey)) >= 32` with the actionable `openssl rand -hex 32` hint. Add `Redacted()` returning a log-safe map (secrets as boolean `*_set` flags).
  - Logging uses stdlib `log/slog` with the level parsed from `LOG_LEVEL` (default `info`); choose a single handler (text) and use it process-wide. Startup config logging goes through `Redacted()` only — never log a secret value.
  - Unit-test: valid config loads; each missing-required case is reported; a 31-byte HMAC key is rejected; `Redacted()` never contains a secret value.

### Batch 2: Mattermost client wrapper, Resolve, Signer, Poster

- [x] Step 3: Implement `internal/mm/client.go` — Client4 wrapper + startup `Resolve`.
  - `NewAPIClient(mmURL, botToken) *model.Client4` (`model.NewAPIv4Client` + `SetToken`).
  - Define `AdminAPI` interface (the `Client4` subset Resolve needs) with `var _ AdminAPI = (*model.Client4)(nil)`.
  - `Resolve(ctx, api, log, team, channel, ownerUsername, ownerUserID) (*Resolved, error)`: `GetMe` (validate token; 401 → typed `ErrBadCredentials`) → `GetTeamByName` → ensure bot team membership (add if missing) → `GetChannelByName` → ensure bot channel membership → resolve owner id (`OWNER_USER_ID` else `GetUserByUsername`) → `verifyOwnerOnly` (page channel members, skip bot/owner/other bots; any remaining human is logged **loudly via Warn, not fatal**). Return `Resolved{BotUserID, TeamID, ChannelID, OwnerUserID}`.
  - Unit-test Resolve against a fake `AdminAPI`: happy path, bad token, missing membership auto-add, non-owner human warning emitted.
- [x] Step 4: Implement `internal/mm/sign.go` — HMAC nav-state Signer.
  - `const MinHMACKeyBytes = 32`; `NewSigner(key)` errors on short key and copies the key.
  - `NavState` struct with compact JSON tags + `omitempty`: action code (`a`), `game_id` (`g`), `player_id` (`pl`), `entry_id` (`e`, the score-entry id for idempotent edit-last), `page` (`pg`), `players` list (`ps`), `post_id` (`post`), `issued_at` (`iat`). Add the action-code constants for every nav grammar entry from Context.
  - HMAC-SHA256 over the **exact transmitted JSON bytes**: `Sign` → `base64url(json) + "." + base64url(mac)`; `Verify` splits, base64-decodes, recomputes the MAC over the decoded payload, compares with `hmac.Equal` (constant-time), then unmarshals. Typed errors `ErrBadToken`/`ErrBadSignature`/`ErrStateExpired`.
  - `SignContext`/`VerifyContext` (no max-age, for `/action`); `SignState`/`VerifyState(token, maxAge)` (enforce `|now-iat| <= maxAge` when `maxAge > 0`, for `/dialog`).
  - Port jobHunter's `sign_test.go` discipline: round-trip, tamper-byte rejection, wrong-key rejection, max-age expiry, constant-time path exercised.
- [x] Step 5: Implement `internal/mm/poster.go` — Poster interface + concrete impl.
  - `Poster` interface: `PostMessage(ctx, channelID, msg) (postID string, err error)`; `PostInThread(ctx, channelID, rootID, msg) (postID, err)` (sets `Post.RootId`); `PostAttachment(ctx, channelID, msg, attachments) (postID, err)`; `PostAttachmentInThread(ctx, channelID, rootID, msg, attachments) (postID, err)`; `UpdatePost(ctx, postID, msg) error`; `UpdateAttachment(ctx, postID, msg, attachments) error`; `DeletePost(ctx, postID) error`; `OpenDialog(ctx, triggerID, url, dialog) error`.
  - Concrete `Client` impl over `model.Client4` (`PostUpdater` subset asserted via `var _`). `UpdatePost`/`DeletePost` treat not-found as benign `nil` (taps on a deleted post must not error). Attachments built via `model.SlackAttachment` with `Actions []*model.PostAction`; `OpenDialog` wraps `OpenInteractiveDialog(model.OpenDialogRequest{...})`.
  - Unit-test the not-found→nil paths and attachment construction with a fake `PostUpdater`.

### Batch 3: SQLite layer — driver, models, migrations, data-preserving rebuild

<!-- test_gate: true -->

- [x] Step 6: Implement `internal/db` open + embedded migration runner + models.
  - `Open(databaseURL) (*sql.DB, error)` using `modernc.org/sqlite` (`sql.Open("sqlite", dsn)`); set `_pragma=journal_mode(WAL)`, `_pragma=foreign_keys(1)`, and **`_pragma=busy_timeout(5000)`** via DSN params (verify modernc's pragma DSN syntax) so concurrent dialog-submit vs action writes wait on the single-writer lock instead of failing fast with `SQLITE_BUSY`; `db.SetMaxOpenConns(5)`, `SetMaxIdleConns(1)`.
  - `internal/db/migrations/` embedded via `//go:embed migrations/*.sql` from `internal/db/db.go` (the pattern is relative to that file's directory — the `.sql` files MUST sit under `internal/db/migrations/`, not the module root); the runner applies pending files in lexical order, tracking applied versions in a `schema_migrations(version TEXT PRIMARY KEY, applied_at TEXT)` table (create-if-not-exists). **sqlx reconciliation:** if a legacy `_sqlx_migrations` table exists and is non-empty, seed `schema_migrations` with 001–004 as already-applied (sqlx already ran them) so only 005 runs; on a DB with neither table, run all five. **FK-safe rebuild:** run the whole migration batch on **one dedicated `*sql.Conn`** that issues `PRAGMA foreign_keys=OFF` *before* any `BEGIN`, wraps each file in its own transaction, runs `PRAGMA foreign_key_check` after the batch (abort on any reported row), then restores `PRAGMA foreign_keys=ON` and closes the conn (other pool conns keep FKs on via the DSN). Before running 005, self-check that the post-004 shape exists (e.g. `players.rating` column present). Idempotent and safe to run at every startup.
  - `models.go`: `Game{ID int64; ChannelID string; PostID sql.NullString; StartedAt, FinishedAt; WinnerPlayerID sql.NullInt64}`, `Player{ID; Name; IsDeleted bool; CreatedAt; Rating float64}`, `ScoreEntry{ID; GameID; PlayerID; Points int64; CreatedAt}`, `PlayerStats{...}` (built manually, mirroring the Rust struct fields). `EloDelta` is defined once in `internal/rating` (its owner); `internal/db` imports `rating` to consume it — do not duplicate the type.
- [x] Step 7: Author migrations 001–005.
  - Copy `001_initial.sql`, `002_last_players.sql`, `003_game_winners.sql`, `004_elo_rating.sql` verbatim from the Rust `migrations/` into `internal/db/migrations/` (so a fresh DB reaches the pre-005 shape), adjusting only what the embedded runner needs (no `sqlx`-specific syntax is used).
  - Author `005_mattermost_ids.sql`: rebuild `games` (`chat_id INTEGER` → `channel_id TEXT`, `message_id INTEGER` → `post_id TEXT` nullable) and `chat_last_players` (`chat_id` → `channel_id TEXT`) via create-new → `INSERT … SELECT` (set `post_id = NULL`, `channel_id = ''`) → `DROP TABLE` old → `RENAME`. **`chat_last_players` PK collision:** its PK becomes `(channel_id, player_id)`; collapsing every legacy `chat_id` to `''` makes any player who appeared under two different Telegram chats collide on the new PK (plausible — `allowed_chat_ids` was never enforced). Use `INSERT OR IGNORE … SELECT DISTINCT player_id …` so the rebuild dedupes instead of aborting on a UNIQUE-PK violation. (`games` is keyed by its unique `id`, so it has no such collision.) recreate `idx_games_chat_id` as an index on `channel_id` plus any other affected indexes. Re-declare `games` as `INTEGER PRIMARY KEY AUTOINCREMENT` so ids continue above the existing max (no id reuse). FK enforcement is handled by the **runner** (Step 6: `foreign_keys=OFF` outside the transaction + `foreign_key_check` after) — the `.sql` file itself must NOT rely on an in-transaction `PRAGMA foreign_keys`, which is a no-op. `channel_id` is left `''`; the real owner channel id is backfilled at startup (Step 21).
  - Preserve `players`, `score_entries`, `game_winners`, `rating_history`, and `players.rating` untouched.
- [x] Step 8: Write the migration parity test.
  - Fixture: a programmatically-seeded DB mirroring the **sqlx-migrated production shape** — the full post-004 schema (with `players.rating`) **and a populated `_sqlx_migrations` table** — seeded with a few players carrying known non-default `rating` values, finished games, score entries, winners, and rating history. Do **not** commit the real personal `flip7.db`. Also seed an unfinished game so the channel-backfill/resume path can be asserted.
  - Open a copy, run all migrations, and assert: 001–004 are NOT re-run (no "duplicate column" error) because `_sqlx_migrations` is present; row counts for `players`/`score_entries`/`game_winners`/`rating_history` are unchanged; the known `players.rating` values survive; `games.post_id` is NULL and `games.channel_id` is `''` for migrated rows; the new column affinities are `TEXT`; AUTOINCREMENT is preserved (a fresh insert gets an id above the prior max).

### Batch 4: Database queries port

- [x] Step 9: Port `db/queries.rs` to `internal/db/queries.go` (all take `ctx` + `*sql.DB`).
  - Functions (channel/post ids now strings): `CreateOrGetPlayer(name)`, `GetAllActivePlayers()`, `CreateGame(channelID)`, `GetUnfinishedGames(channelID)`, `FinishGameWithWinners(gameID, winnerIDs, eloDeltas) (bool, error)` (single TX, **idempotent**: the finalizing `UPDATE games SET finished_at=…, winner_player_id=… WHERE id=? AND finished_at IS NULL` is the guard — if 0 rows affected the game was already finished, so return `false` and write neither `game_winners`/`rating_history` nor any `players.rating` change; otherwise insert winners + per-delta history + rating updates and return `true`), `DiscardGame(gameID) (bool, error)` (TX, guarded: delete score_entries/game_players/game_winners/game row only if the game row still exists and is unfinished; returns `true` iff this call actually deleted it, so a replay/double-tap on an already-discarded game returns `false` and triggers no duplicate side effects), `UpdateGamePostID(gameID, postID)`, `BackfillLegacyChannel(channelID)` (idempotent `UPDATE … WHERE channel_id=''` for games + chat_last_players), `AddPlayerToGame(gameID, playerID)`, `AddScoreEntry(gameID, playerID, points) (ScoreEntry, error)`, `GetLastScoreEntry(gameID) (Option)`, `DeleteScoreEntryByID(gameID, entryID) (bool, error)` (`DELETE … WHERE id=? AND game_id=?`; returns whether a row was deleted — replaces the old "delete max-id" so edit-last is idempotent and replay-safe), `GetGameScores(gameID)` (`map[int64]int64`), `GetGamePlayers(gameID)`, `IsPlayerInUnfinishedGame(playerID) bool` (guards hard-delete against players still in an active game), `GetFinishedGameResult(gameID)` (read-back for re-rendering a finished game's win screen: the `game_winners` rows + the per-player `rating_history` deltas/`rating_after` for that game — so a replayed/stale `end_confirm` rebuilds the win screen from **persisted** values, never by recomputing Elo over already-updated ratings), `GetPlayerStats(playerID)`, `RenamePlayer(playerID, name)`, `NameExists(name, excludePlayerID)` (checks **all** rows incl. soft-deleted, to pre-empt the `UNIQUE` index), `HardDeletePlayer(playerID)` (TX: delete score_entries/game_players/game_winners/rating_history/chat_last_players, NULL `games.winner_player_id`, delete player), `SaveLastPlayers(channelID, playerIDs)`, `GetLastPlayers(channelID)`, `GetAllStats()`.
  - Match the Rust semantics exactly: stats count only `finished_at IS NOT NULL` games; `losses = games - wins`; `win_rate = wins/games`; `avg_score = total_points/games` (0 when `games = 0`); `highest_score = MAX` per-game total; `GetAllStats` sorts by `rating DESC` then case-insensitive `name ASC`; `CreateOrGetPlayer` un-soft-deletes an existing same-name player.
  - Use explicit transactions (`db.BeginTx`) with rollback-on-error for every multi-statement mutation. Build any variable-length clause (`game_winners` multi-insert, `SaveLastPlayers`, any `IN (...)`) from **bound `?` placeholders**, never string-interpolated ids. Add focused query tests against an in-memory migrated DB, including a double-call to `FinishGameWithWinners`/`DeleteScoreEntryByID` asserting the second call is a no-op (idempotency).

### Batch 5: Elo rating port + golden parity

<!-- test_gate: true -->

- [x] Step 10: Port `utils/rating.rs` to `internal/rating/rating.go` exactly.
  - Constants: `KFactor = 24.0`, Elo divisor `400.0`, base rating `1000.0` (schema default). `EloEntry{PlayerID, Score int64, Rating float64}`, `EloDelta{PlayerID, RatingBefore, RatingAfter, Delta float64}`.
  - `expected(rSelf, rOpp) = 1/(1+10^((rOpp-rSelf)/400))`; `ComputeUpdates(entries) []EloDelta`: for `n<2` zero deltas; else `kPerPair = KFactor/(n-1)`, sum over unordered pairs with actual scores {win=1, loss=0, tie=0.5}; preserve input order; `RatingAfter = Rating + Delta`.
  - Port all 12 Rust unit tests (`two_equal_players_winner_gains_half_k`, `equal_scores_means_zero_delta`, `favourite_beats_underdog_small_gain`, `upset_underdog_wins_big_gain`, `three_player_distinct_scores`, `joint_winners_split_against_each_other`, `conservation_holds_for_arbitrary_games`, `single_player_no_change`, `empty_input_returns_empty`, `rating_after_equals_before_plus_delta`, `order_preserved`, `k_factor_scales_with_player_count`).
- [x] Step 11: Add a Go↔Rust golden parity test (mirror jobHunter's shared-fixture discipline).
  - Commit a `testdata/elo_golden.json` of `{entries → expected deltas}` cases generated from the Rust implementation (document the generation command in a comment). The Go test loads the fixture and asserts `ComputeUpdates` matches each expected delta within a tight float tolerance (e.g. `1e-9`), including a multi-winner and an upset case so the conservation and pairwise branches are covered.

### Batch 6: Scoreboard & text rendering

- [x] Step 12: Port `utils/scoreboard.rs` and the win/stats renderers to Mattermost markdown.
  - `RenderScoreboard(scores map[int64]int64, players []Player, gameID int64) string`: sort by score descending, medals 🥇🥈🥉 for ranks 1–3 / `   {n}.` for 4+, dot-pad name to 15 chars (`{name:.<15}`), header `🎮 Flip 7 — Game #{id}`, footer `🏁 First to 200 wins!`. Wrap the body in a fenced code block (```) to preserve monospace alignment (replaces Telegram's implicit `<pre>`). Player names are already validated (Step 16/19) to exclude backticks, newlines, and control chars, so they cannot break out of the fence or inject markdown/mentions; the renderer additionally truncates to the column width.
  - `BuildWinText(...)`: single `🏆 WINNER: {name} with {score} pts!` or joint `🏆 JOINT WINNERS: {names} — tied at {score} pts!`; "Final Scores:" list with medals + Elo delta `   (+{delta} → {rating_after})`; footer `📈 Rating changes shown in parentheses.`
  - `RenderHallOfFame(stats []PlayerStats, page int)` and `RenderPlayerDetail(stat)`: port the Hall-of-Fame table (columns `# Player G W Win% Avg Elo`, `━` separator, medals top-3, name truncated to 12, win% as `{:.0}%`, sorted by rating desc) into a fenced code block; port the detail card layout.
  - Unit-test the rendered strings against golden snapshots (ordering, padding, medals, code-fence wrapping).

### Batch 7: HTTP server — listener, auth middleware, routes

- [x] Step 13: Implement `internal/server` — net/http listener + auth + routes.
  - Listener bound to `ListenAddr` (`0.0.0.0:8068` inside the container is acceptable — the port is bridge-only, not host-published); `http.Server` with `ReadHeaderTimeout = 5s`; `withLimits` wrapper: `http.MaxBytesReader` (1 MiB) + a 15s per-request context. Graceful `Shutdown` (10s) on SIGTERM.
  - Routes: `/healthz` (GET/HEAD only → static `200 "ok"`, echoing nothing); `/slash/flip7` (POST `x-www-form-urlencoded`); `/action` (POST JSON `model.PostActionIntegrationRequest`); `/dialog` (POST JSON `model.SubmitDialogRequest`).
  - Middleware: **slash** → `subtle.ConstantTimeCompare(token, SlashTokenFlip7)` (empty expected never matches) then `ownerOK`; **/action** → `VerifyContext(req.Context["nav"])` (no max-age) then `ownerOK(req.UserId)`; **/dialog** → `VerifyState(req.State, dialogStateMaxAge)` then `ownerOK(req.UserId)`; `ownerOK = OwnerUserID != "" && userID == OwnerUserID`.
  - Errors: `deny` logs a short label at Debug → `401 "unauthorized"`; `fail` logs the error at Error → `500 "internal error"`. Never log bodies, `context`, `state`, or secrets. Business "expired" messages are returned by handlers as ephemerals, not by middleware.
  - Unit-test the middleware: forged/tampered context rejected, wrong bearer token rejected (constant-time path), non-owner rejected, `/healthz` open and echoing nothing.
  - Add an **end-to-end integration test** (`httptest.Server` over the real `Handler()`) for the highest-risk seam: post a properly signed `/action` and a `/dialog` submit with a fake `Poster` and an in-memory migrated DB, asserting the full chain (verify → owner check → DB write → re-render call on the Poster) and that a tampered/non-owner request is rejected before any DB write.

### Batch 8: Nav-state grammar, screen router, menu, slash command

- [x] Step 14: Implement `internal/nav` — grammar, `button()` helper, and the screen router.
  - Encode/decode every nav grammar entry (Context) into `NavState`; `button(label, ns)` signs the state and sets `PostAction.Integration = {URL: ${INTEGRATION_BASE_URL}/action, Context: {"nav": token}}`. A signing failure logs and skips that one button (the screen still renders).
  - `Navigator.Action(ctx, req, ns)` dispatches by action code, each screen returning `PostActionIntegrationResponse{Update: post}` (re-render in place via `model.ParseSlackAttachment`); a `Close` action deletes/re-renders the post; stale/forged data yields `ephemeral("This view has expired — run /flip7 to start again.")`.
  - Wire the dialog submit dispatcher: decode `SubmitDialogRequest`, `VerifyState`, owner-check, switch on the action in the signed `state`, validate `Submission` fields (return `SubmitDialogResponse{Errors}` to keep the dialog open), apply DB writes, then re-render the originating post via `UpdateAttachment(ns.PostID, …)` and post a transient confirmation; `req.Cancelled` → empty success. **Stale-post fallback:** if `UpdateAttachment` reports not-found (the originating post was deleted/replaced — e.g. via `/flip7 scoreboard` or a resume), do not silently swallow it: post a **fresh** scoreboard root post, `UpdateGamePostID` to the new id, and continue — so the visible board never goes silently stale after a successful DB write.
- [x] Step 15: Implement `internal/menu` + `/flip7` slash routing.
  - The menu lives in its own `internal/menu` package (it cannot live in `nav`, which is now grammar-only). `internal/menu` exposes `Owns(action)` for `menu:*` and the post-game nav `game:home|new|stats`, plus the screen builders, and imports only `nav`+`mm`+`db`. In `cmd/mmbot/main.go`'s `combinedAction`, `menu` is also the **explicit default owner**: any verified action no other package claims routes to the menu (renders the main menu / a benign "expired" ephemeral), so the dispatch partition has no undefined fallthrough.
  - `/flip7` (empty arg) → post the main-menu nav post with buttons `New Game`, `Load Game`, `Statistics`, `Players` (actions `menu:new_game|load_game|stats|players`). `/flip7 scoreboard` → re-post the most-recent active game's scoreboard at the channel bottom (delete the old post, `CreatePost` fresh, update `post_id`); if none, ephemeral "No active game." `/flip7 help` → ephemeral onboarding blurb (replaces `/start`/`/help`).
  - `cmd/mmbot/main.go` routes the three subcommands to the menu/game entry points.

### Batch 9: New-game flow

- [x] Step 16: Implement `internal/newgame` — setup, dialogs, known-player picker, start.
  - Entry: build the setup screen; pre-load last-used players via `GetLastPlayers(channelID)` filtered to active, carried as `players` in the signed context. Render added-players list + buttons.
  - `+ New Player` → `OpenDialog` **as the first action** using the trigger_id from the action payload (the ~3s trigger_id window means the dialog must open before any non-essential DB work; the players list needed for prefill is already in the signed context). On submit validate name (1–50 characters matching the Rust check; reject control characters, newlines, and backticks so names are safe inside code-fence renders and cannot inject markdown/mentions), `CreateOrGetPlayer` (un-soft-delete if needed); if already in the list reply "{name} is already added"; else append and re-render.
  - `+ Known Player` → paginated picker (8/page) excluding already-added active players; `known:add:{id}` appends and returns to setup; `known:page:{n}` paginates; `known:back`; `known:start` starts the game directly from the picker (same effect as `setup:start` below — shares the start path, including the ≥2-player and no-unfinished-game guards); `known:noop` is the inert page-indicator.
  - `player:remove:{id}` removes instantly and re-renders.
  - `Start Game` (`setup:start`, and `known:start` from the picker): require ≥ 2 players (otherwise the button renders as a disabled `setup:disabled` no-op); block if `GetUnfinishedGames(channelID)` is non-empty ("You have an unfinished game — load or end it first."); else `CreateGame(channelID)`, `AddPlayerToGame` per player, `CreatePost` the scoreboard **root post** (the game thread), store its id via `UpdateGamePostID`, `SaveLastPlayers`. **Retire the originating setup post:** the `/action` response must re-render the originating setup/menu post into a NON-interactive stub ("🎮 Game started — see the scoreboard thread ⬇️", no buttons), so only the fresh root post carries the live score/end controls. Otherwise two posts would bear no-max-age score/end buttons for the same `game_id` (the same hazard guarded against in `game:load` and `/flip7 scoreboard`).

### Batch 10: Active game lifecycle, score entry, load game

- [x] Step 17: Implement `internal/game` — live scoreboard, score entry, edit-last, end-game.
  - Render the scoreboard as a `SlackAttachment` on the root post: text from `RenderScoreboard`, with one `score:player:{game_id}:{player_id}` button per in-game player plus controls `game:edit:{id}` / `game:end:{id}`. Every score/edit re-renders the **same root post** via `UpdateAttachment`.
  - Score entry: `score:player` → `OpenDialog` for points **as the first action** (trigger_id window) carrying `game_id`+`player_id` in the signed dialog state; on submit guard `finished_at IS NULL` (else "This game is already over."), **verify the player still exists and is a `game_players` member of this game** (a stale button for a since-deleted player must yield a benign "That player is no longer in this game." ephemeral, never an FK-violation 500), validate integer 0–999, `AddScoreEntry`, `UpdateAttachment` the root post, and `PostInThread(channelID, postID, "✏️ {name}: {pts}")` as a thread reply (parity with the Rust confirmation line). Apply the Step-14 stale-post fallback if the root post is gone. **No auto-end.**
  - Edit Last (`game:edit`): read the current last entry via `GetLastScoreEntry`; if none, ephemeral "No scores to edit yet."; else render `edit:confirm` carrying that **specific `entry_id`** in the signed context, plus `edit:cancel`. On confirm call `DeleteScoreEntryByID(gameID, entryID)` (idempotent — a replay/double-tap that finds the row already gone is a harmless no-op, and a stale button can only ever delete the one entry it was minted for, never a newer one) then re-render.
  - End Game (`game:end` → `game:end_confirm`/`game:end_cancel`): the confirm copy differs by `max_score` (≥ 200 → "declare winner"; < 200 → "discard" warning). On confirm: if `max < 200` → call `DiscardGame(gameID)`; **only on a `true` result** (this tap actually discarded it) edit the originating scoreboard root post into a non-interactive stub ("🚩 Game discarded — nobody reached 200.", no buttons) and post the discard reply once; **on `false`** (already discarded by a prior tap — rows are gone) just render the same benign ended stub with no controls and post nothing (no duplicate reply, no live buttons over a deleted game). Symmetric with the finish path's replay handling. Else (max ≥ 200) compute Elo via `rating.ComputeUpdates` over final per-player totals and call `FinishGameWithWinners(gameID, winnerIDs, deltas)`. **On a `true` result** (this tap finalized it): `SaveLastPlayers`, render the win screen (`BuildWinText` from the just-computed deltas) into the root post, and post the winner-announcement **thread reply once**, buttons `game:stats|new|home`. **On a `false` result** (already finalized by a prior tap): do NOT recompute Elo (the players' ratings were already updated, so a recompute would show wrong deltas) and do NOT re-post the announcement reply (it would duplicate) — instead read persisted winners + deltas via `GetFinishedGameResult` and re-render the same win screen from those stored values. Ties → all top scorers are joint winners.
- [x] Step 18: Implement Load Game (resume into a fresh thread).
  - `menu:load_game` → list `GetUnfinishedGames(channelID)` as buttons `game:load:{id}` labeled `Game #{id} · {names} · {Mon D}`; empty → "No unfinished games found." with Back.
  - `game:load:{id}` → if the game still has a non-null `post_id`, **best-effort `DeletePost`** of the old scoreboard first (so two live boards with valid no-max-age buttons can't both drive one game); then fetch `GetGameScores` + `GetGamePlayers`, `CreatePost` a **fresh** scoreboard root post, `UpdateGamePostID`, and render the active-game screen on the new thread.

### Batch 11: Players management & statistics

- [x] Step 19: Implement `internal/players` — manage, rename, delete, pagination.
  - `menu:players` → paginated active-player list (8/page) with per-player `mgmt:rename:{id}` / `mgmt:delete:{id}`, plus `mgmt:page:{n}` / `mgmt:back`.
  - `mgmt:rename:{id}` → open the dialog first (trigger_id window); on submit validate (1–50 chars, no control chars/newlines/backticks) and uniqueness via `NameExists(name, excludeID)` which checks **all** rows including soft-deleted — surfacing a friendly "name already taken" `SubmitDialogResponse{Errors}` instead of letting the `players.name` UNIQUE index throw a 500 — then `RenamePlayer`. `mgmt:delete:{id}` → `mgmt:delete_confirm:{id}` / `mgmt:delete_cancel`; on confirm, first guard `IsPlayerInUnfinishedGame(id)` — if true, refuse with an ephemeral ("Can't delete {name} — they're in an active game. End or discard it first.") so a stale `score:player` button can't later reference a deleted player; else `HardDeletePlayer` and re-render.
- [x] Step 20: Implement `internal/stats` — Hall of Fame + player detail.
  - `menu:stats` → `GetAllStats` → `RenderHallOfFame` (10/page) with per-player drilldown buttons `stats:player:{id}` (3/row), `stats:page:{n}`, `stats:back`.
  - `stats:player:{id}` → `GetPlayerStats` → `RenderPlayerDetail` with `stats:back_to_list`. Stats: `finished_at IS NOT NULL` counts as a game; `winner_player_id`/`game_winners` count wins; the rating shown is the stored `players.rating`.

### Batch 12: Main wiring

- [x] Step 21: Implement `cmd/mmbot/main.go`.
  - `config.Load` → `db.Open` + run embedded migrations → `mm.Resolve` (logging the owner-only warning) → **`BackfillLegacyChannel(resolved.ChannelID)`** (claims pre-migration `channel_id=''` games + last-players into the configured owner channel, idempotent) → construct `Signer` + `Poster` → wire every screen/handler (nil-tolerant so the listener can stand up before business logic) → build the `server`. The `/action` dispatcher is the `combinedAction` composition (Context → router): it routes the verified `NavState` to the first screen package whose `Owns(action)` returns true, keeping `nav` free of screen-package imports. Serve with graceful SIGTERM shutdown draining in-flight requests.
  - Initialize `log/slog` from `LogLevel`; never print secrets (use `config.Redacted()` for any startup config log).
  - Smoke-build: `make check` passes (`go build ./... && go vet ./... && go test ./...` + `gofmt -l .` clean).

### Batch 13: Deployment — Dockerfile, Compose, register.sh

- [x] Step 22: Author `mmbot/Dockerfile` and `mmbot/docker-compose.yml`.
  - Multi-stage `Dockerfile`: `golang:1.26.4-alpine` builder with module-cache layering (`COPY go.mod go.sum` → `go mod download` → `COPY` source), `CGO_ENABLED=0 go build -o /out/mmbot ./cmd/mmbot`; runtime `alpine:3.24` + `ca-certificates` + `wget` (for the healthcheck), a non-root `bot` user, `WORKDIR /app`, copy the static binary, `CMD ["./mmbot"]`. Migrations are embedded in the binary (no separate copy needed).
  - `docker-compose.yml`: `flip7bot` service joins the Mattermost stack's bridge as an **external network** (declared `external: true`, referencing the MM stack's existing named network — which must already exist; compose will not create it) with a **fixed IP** on `172.28.0.0/16` (document the chosen IP); mount the existing `flip7_data` volume at `/data`; `env_file: .env`; `restart: unless-stopped`; healthcheck `wget -qO- http://127.0.0.1:8068/healthz`; do **not** publish port 8068 to the host. Set `INTEGRATION_BASE_URL=http://<fixed-ip>:8068` and `MM_URL` to the MM container's bridge address (port 8065) in `.env.example`.
- [x] Step 23: Author `mmbot/scripts/register.sh`.
  - Source `.env` for `MM_TEAM`/`OWNER_USERNAME`/`INTEGRATION_BASE_URL`; create the `/flip7` slash command via `mmctl --local` (`docker exec -i mattermost mmctl …`) pointing at `${INTEGRATION_BASE_URL%/}/slash/flip7`, with `--autocomplete`; parse the token with `jq -r '.token'`; write/replace `SLASH_TOKEN_FLIP7` in `.env` via `awk` (preserving `chmod 600`). `set -euo pipefail`; `bash -n`-clean; print a reminder that there is no `/start` and to restart the container. Never run in CI.

### Batch 14: Documentation, smoke tests, validation

- [x] Step 24: Write/update documentation and memory.
  - Author `mmbot/CLAUDE.md` describing the Mattermost architecture (gateway/port model, inbound auth + HMAC nav-state, channel/team/owner resolution, thread-per-game scoreboard, register.sh, secrets, smoke tests, source layout). Document the **first-deploy provisioning order** (create MM bot account + token, team, owner-only channel, add bot; fill `.env`; `register.sh`; back up `flip7.db`; `compose up`) and the **rollback procedure** (stop the Go container → **delete the post-migration `flip7.db-wal`/`flip7.db-shm` sidecars** on the volume → restore `flip7.db.bak` → redeploy the Rust bot — required because 005 transforms the DB to TEXT columns the Rust binary cannot read, and because leaving the Go-era WAL behind would replay over and corrupt the restored backup). Update the project `~/flip7bot/README.md` / `CLAUDE.md` to describe the Go/Mattermost bot and the Docker-on-bridge deployment.
  - Update memory at `/home/rivan/.claude/projects/-home-rivan-flip7bot/memory/` with the migration outcome and the new architecture. Keep `plan.md` Active/Completed tracking (move items to Completed with commit hashes as jobHunter does).
- [x] Step 25: Execute the acceptance / smoke-test checklist against the live MM stack.
  - **First, back up the live DB (WAL-safe) before the very first container start**, because the 005 rebuild runs at first startup and is the point of no return. With the Rust container stopped, checkpoint then copy: `docker run --rm -v flip7_data:/d keinos/sqlite3 sqlite3 /d/flip7.db "PRAGMA wal_checkpoint(TRUNCATE);"` followed by `docker run --rm -v flip7_data:/d alpine cp /d/flip7.db /d/flip7.db.bak` — or copy all three of `flip7.db`/`flip7.db-wal`/`flip7.db-shm` together. A plain `cp flip7.db` alone can drop committed-but-un-checkpointed games (Rust runs WAL mode).
  - Health (`/healthz` → `200 ok` from a bridge container, not from LAN); `/flip7` posts the menu, `/flip7 help` → ephemeral; new game (known players + add-via-dialog) posts a scoreboard thread with a stored post id; typed score entry updates the scoreboard in place + posts a thread reply; edit-last deletes the last entry and re-renders (Elo unaffected until end); End Game with max ≥ 200 detects winner(s) incl. ties/multi-winner and writes `game_winners` + `rating_history`, with max < 200 discarding; load-game resumes into a fresh thread; players rename/delete/paginate; stats Hall of Fame + detail render with preserved historical Elo; a tampered `context` → benign "expired" ephemeral and a non-owner `user_id` is rejected; pre-migration history is intact. Confirm `make check` is green.

### Batch 15: Cutover — retire the Rust tree

- [x] Step 26: Retire the Rust implementation in a dedicated commit (only after Step 25 passes).
  - Back up the live DB first (WAL-safe, container stopped): checkpoint via `docker run --rm -v flip7_data:/d keinos/sqlite3 sqlite3 /d/flip7.db "PRAGMA wal_checkpoint(TRUNCATE);"` then `docker run --rm -v flip7_data:/d alpine cp /d/flip7.db /d/flip7.db.bak` (or copy all three `flip7.db*` files).
  - In one dedicated commit, delete the Rust working tree (`src/`, `Cargo.toml`, `Cargo.lock`, the Rust `Dockerfile`, `entrypoint.sh`) and the now-consumed `MIGRATION_PROMPT.md`; promote the `mmbot/` Dockerfile/compose to drive the project. History is preserved in git (nothing is rewritten). Do not retire anything before the Go bot is validated.

## Plantator Review Notes

This plan was generated by the `plantator` skill in **autonomous review mode** at **medium** depth (serious threshold: HIGH and above). It was grounded in a full read of both the current Rust source (`~/flip7bot/src`) and the reference Go implementation (`~/jobHunter/mmbot`), and its dependency versions were verified against authoritative sources (2026-06-26).

**Iterations:** the original cap was 3; the user then requested more passes, so the review was extended to **6 iterations**. Stopping condition: **clean convergence** — iteration 6 returned zero serious findings across all reviewed angles (the first fully-clean pass). Across all six iterations, 0 of 15 serious findings were repeats — the digest-dedup held and every fix-round addressed genuinely new material until the design stabilized.

**Per-iteration outcome:**
- Iteration 1 — 7 serious (2 CRITICAL, 5 HIGH) across all four angles. Fixed: sqlx-migration reconciliation (avoid duplicate-column crash on the real DB), FK-safe table rebuild (PRAGMA foreign_keys is a no-op inside a transaction), channel-id backfill vs the ported channel filter, backup-before-first-migration + rollback path, the nav import cycle, and idempotent/replay-safe mutations. Plus many sub-HIGH fixes applied directly.
- Iteration 2 — 3 serious HIGH (security returned clean). Fixed: `go:embed` parent-directory build bug, `chat_last_players` PK collision on the blank-channel backfill, and the idempotent-replay win screen recomputing Elo over already-updated ratings.
- Iteration 3 — 2 serious HIGH (security AND gaps both clean; Agent 4 confirmed full feature parity — every callback prefix and all 18 queries are mapped). Fixed: new-game start leaving the originating setup post with live controls, and hard-deleting a player who is still in an unfinished game (stale button → FK-violation 500).
- Iteration 4 (extended) — 2 serious HIGH (architecture, bugs, AND security all clean; gaps only). Fixed: WAL-unsafe backup (`cp flip7.db` drops un-checkpointed sidecars) and WAL-unsafe rollback (stale Go-era `-wal`/`-shm` replays over and corrupts the restored backup).
- Iteration 5 (extended) — 1 serious HIGH (architecture and gaps clean; bugs only). Fixed: the discard branch was not symmetrically replay-safe — a replayed `end_confirm` on an already-discarded game would post a duplicate "Game discarded" reply and leave live controls over deleted rows; `DiscardGame` now returns a bool and the branch retires the post + posts the reply only on the true path.
- Iteration 6 (extended) — **0 serious findings (bugs, architecture, security all clean).** Convergence: bugs confirmed the replay/idempotency family (finish/discard/edit-last) is fully closed; architecture and security verified the load-bearing Mattermost v0.4.2 API assumptions (`PostActionIntegrationRequest.TriggerId`, `EphemeralText`, `OpenInteractiveDialog`, `UpdatePost`/`DeletePost`, channel-member APIs) against the actual module source. Gaps was rested (parity confirmed in iter 3, clean in iters 3–5).

**Final-iteration (6) agent reports:**
- Architecture — **No serious findings.** Verified the dialog-from-button, ephemeral, and post-update APIs exist with the expected signatures in `mattermost/server/public@v0.4.2`.
- Bugs — **No serious findings.** Pronounced the replay/idempotency family fully closed and internally consistent with the `GetFinishedGameResult` false-path re-render.
- Security — **No serious findings.** Confirmed the discard-stub change introduced no new exposure; the trigger_id seam is sound.

**Autonomous decisions of note (with rationale):**
- Strict feature parity with *current Rust behavior* where it diverges from the migration prose: manual End Game (no auto-end at 200), typed-only score entry. Rationale: the user explicitly chose parity; the existing code is authoritative.
- Thread-per-game with a fully DB-backed (no in-memory) session model. Rationale: the user asked for per-game threads and "don't keep games in memory"; the stateless re-read design satisfies both with no TTL needed.
- Idempotency enforced at the query layer (guarded `WHERE finished_at IS NULL`, delete-by-entry-id) rather than via button max-age. Rationale: game buttons must stay valid for the game's whole life, so DB-state guards are the correct place to make replays safe.
- Rebuild affected tables to `TEXT` (vs adding parallel `*_pid`/`*_cid` columns). Rationale: legacy Telegram ids are meaningless under Mattermost; a clean TEXT schema is simpler, and the data preserved (players/scores/winners/ratings/history) is untouched.
- Accepted residual risk (documented, not "fixed"): the `0.0.0.0` container bind is weaker network-scoping than jobHunter's gateway-IP+ufw model; mitigated by HMAC + owner check + owner-only channel + port-not-host-published, and a fixed-IP bind is offered as a tightening option. This matches the reference's own accepted-risk posture for a single-user home server.

**Residual note:** the extended 6-iteration review reached a natural clean convergence (iteration 6 found nothing across bugs/architecture/security). The dominant theme across the back half of the review was **replay-safety of no-max-age action buttons** — finish, discard, edit-last, new-game-start, and player-delete all needed DB-state guards because a button stays clickable for the game's whole life. The implementer should treat that family as the highest-risk area and keep the idempotency tests (double-call no-op assertions) front and center. Operationally, the WAL-aware backup/rollback procedure is the other thing to get exactly right before the first real migration.
