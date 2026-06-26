# mmbot — flip7bot (Go / Mattermost)

Guidance for Claude Code and humans working in `~/flip7bot/mmbot/`.

This is the Go/Mattermost port of flip7bot (originally Rust/teloxide on Telegram).
It is a single-owner score tracker for the physical card game **Flip7**: the host
opens a menu via `/flip7`, adds players, types each hand's score, and the bot keeps
a **live scoreboard post edited in place**, resolving the winner at a manual
**End Game** and maintaining Elo ratings + a Hall of Fame. The existing `flip7.db`
(SQLite) is carried forward unchanged except for an additive, data-preserving
schema migration.

## Build & test

Go toolchain 1.26.4 (pinned via `go.mod` / `GOTOOLCHAIN=auto`). Go ≥1.24 is
required by the Mattermost client module. The binary is pure-Go (cgo-free) thanks
to `modernc.org/sqlite`, so it builds and runs static on Alpine.

```bash
cd ~/flip7bot/mmbot
make check     # build ./... + vet ./... + test ./... + gofmt -l . clean (the full gate)
make build     # CGO_ENABLED=0 go build ./...
make test      # go test ./...
make run       # go run ./cmd/mmbot (needs a populated .env in cwd / environment)
go build -o mmbot ./cmd/mmbot   # produce the binary directly
```

`make check` must be green before any commit.

## Architecture

### Gateway / port model

- Inbound: `net/http` listener binds `0.0.0.0:8068` **inside the container only**.
  The port is published only to the Mattermost bridge network, never to the host.
  (8068 avoids MM's 8065/8066 and jobHunter's 8067.)
- Outbound: `model.Client4` calls the MM server at `MM_URL` (the MM container's
  bridge IP / service name, port 8065).
- Routes (see `internal/server/server.go`):
  - `GET /healthz` → static `200 ok`, echoes nothing.
  - `POST /slash/flip7` → the `/flip7` slash command (bearer-token guarded).
  - `POST /action` → interactive button presses (HMAC nav-state in `context`).
  - `POST /dialog` → interactive dialog submissions (HMAC `state`).

### Three-layer security guard

1. **HMAC-SHA256 nav-state** (`internal/mm/sign.go`): every button `context` and
   dialog `state` is `base64url(json) + "." + base64url(hmac)`. The MAC covers the
   *exact transmitted JSON bytes*; verify recomputes over the same bytes (no
   re-marshal) and compares constant-time via `hmac.Equal`. The signing key
   (`HMAC_KEY`, ≥32 bytes) is server-only and never placed in a post. There is
   **no max-age on action `context`** (a game's scoreboard buttons must stay valid
   for the game's whole life); dialog `state` may carry an optional max-age.
2. **Network scoping** — the inbound port is reachable only on the Docker bridge,
   never on the host. **Caveat:** because the bind is `0.0.0.0` on a *shared*
   bridge (not a gateway-IP bind + host firewall as in jobHunter), any container
   on the MM bridge can reach the listener, so this layer is weaker than the
   reference. Accepted residual risk for a single-user home server; mitigated by
   not publishing the port, the owner-only channel, and HMAC. To tighten later,
   bind the container's fixed bridge IP (`172.28.0.5:8068`) instead of `0.0.0.0`.
3. **Owner check** — `user_id == owner` is enforced on every `/slash`, `/action`,
   and `/dialog`. A non-owner is rejected; a tampered/forged nav-state fails HMAC
   and yields a benign "expired" ephemeral.

Never log raw request bodies, action `context`, dialog `state`, the bot token, the
HMAC key, or the slash token. Startup config logging goes through
`config.Redacted()` (secrets exposed only as boolean `*_set` flags).

### Channel / team / owner resolution

At startup `internal/mm.Resolve` (called from `cmd/mmbot/main.go`) only *looks up*
(never creates): validates `MM_BOT_TOKEN` via `GetMe`, resolves `MM_TEAM` and
`MM_CHANNEL` to ids, and resolves the owner (`OWNER_USER_ID`, or
`OWNER_USERNAME` → id via `GetUserByUsername`). It logs an owner-only warning. If
the team/channel/bot don't exist yet, startup fails — see provisioning order
below.

After resolution, `BackfillLegacyChannel(resolvedChannelID)` runs an idempotent
`UPDATE games SET channel_id = ? WHERE channel_id = ''` (and likewise for
`chat_last_players`), claiming pre-migration rows into the configured owner
channel so legacy unfinished games remain listable/resumable.

### Thread-per-game live scoreboard

Each new/resumed game `CreatePost`s a root **scoreboard post**; its post id is
stored on the game row (`games.post_id`) and is the game's thread root. The
scoreboard root is `UpdatePost`'d in place on every score/edit; per-entry
confirmations post as **replies** in the thread (`RootId` = scoreboard post id).

### No in-memory sessions (stateless)

The teloxide dialogue FSM is eliminated. Every interaction re-reads SQLite by
`game_id` (carried in the signed context) and re-renders — a game is never held
in memory. Mutating confirm actions are **idempotent**: end-game/discard finalize
only `WHERE finished_at IS NULL`; edit-last deletes a specific score-entry id
(`DELETE … WHERE id = ?`) carried in the signed context, so a replay cannot delete
a newer entry.

### Source layout (`internal/`)

| Package | Responsibility |
|---|---|
| `config` | env loader + fail-fast validation; `Redacted()`; `SlogLevel()` |
| `mm` | `Client4` wrapper, startup `Resolve`, HMAC `Signer`, `Poster` interface |
| `db` | `database/sql` + `modernc.org/sqlite`; embedded migrations 001–005; models; ported queries |
| `rating` | Elo port (`rating.go`) — golden-parity tested against the Rust output |
| `scoreboard` | live scoreboard + win/stats markdown renderers |
| `server` | `net/http` listener, auth middleware, route handlers |
| `nav` | nav-state grammar, `button()` helper, screen router (`Owns`) — imports no screen packages |
| `menu` | `/flip7` slash routing + main menu |
| `newgame` | setup, dialogs, known-player picker, start |
| `game` | live scoreboard, typed score entry, edit-last, end-game; load/resume |
| `players` | manage, rename, delete, pagination |
| `stats` | Hall of Fame + player detail |

`cmd/mmbot/main.go` is the only package that imports everything: it wires the
screens and owns the `/action` dispatcher (`combinedAction`), routing the verified
`NavState` to the first screen package whose `Owns(action)` returns true. This
keeps `nav ← screens ← main` acyclic (`nav` never imports screens).

## Configuration (env vars)

Copy `.env.example` to `.env`, `chmod 600 .env`, fill in the blanks. `.env` is
gitignored and holds secrets — never commit it.

| Var | Required | Default | Notes |
|---|---|---|---|
| `MM_URL` | yes | — | MM server over the bridge, e.g. `http://172.28.0.1:8065` |
| `MM_BOT_TOKEN` | yes | — | **secret** — bot account access token |
| `MM_TEAM` | yes | — | team name/slug owning the channel + slash command |
| `MM_CHANNEL` | yes | — | owner-only channel name/slug |
| `OWNER_USER_ID` | one of | — | owner id; or set `OWNER_USERNAME` |
| `OWNER_USERNAME` | one of | — | resolved to an id at startup if id absent |
| `HMAC_KEY` | yes | — | **secret**, ≥32 bytes; `openssl rand -hex 32` |
| `SLASH_TOKEN_FLIP7` | yes | — | **secret**; captured by `register.sh` |
| `DATABASE_URL` | no | `/data/flip7.db` | path inside the container (on `flip7_data`) |
| `LISTEN_ADDR` | no | `0.0.0.0:8068` | bridge-only bind |
| `INTEGRATION_BASE_URL` | yes | — | base URL MM uses to reach this bot, e.g. `http://172.28.0.5:8068` |
| `LOG_LEVEL` | no | `info` | debug / info / warn / error |

## Deployment (Docker Compose on the MM bridge)

Deliberate deviation from jobHunter's host `systemd --user` unit.

- `docker-compose.yml` declares the `flip7bot` service, joins the MM stack's
  **existing** bridge as an `external` network (compose will NOT create it — bring
  the MM stack up first), and assigns the **fixed IP `172.28.0.5`** on
  `172.28.0.0/16` so `INTEGRATION_BASE_URL` is stable.
- **Edit `docker-compose.yml`:** replace the network name `mattermost_default`
  with your MM stack's actual network (`docker network ls`).
- The existing `flip7_data` volume (carried over from the Rust deployment, holding
  `flip7.db`) is mounted at `/data` and declared `external` so compose never wipes
  it.
- `restart: unless-stopped`; healthcheck `wget -qO- http://127.0.0.1:8068/healthz`.
  The listener port is **not** published to the host.
- MM allow-list: the stack's `MM_SERVICESETTINGS_ALLOWEDUNTRUSTEDINTERNALCONNECTIONS`
  already covers `172.16.0.0/12` ⊇ `172.28.0.0/16`, so no MM env change is
  expected.

### First-deploy provisioning order (do this in order)

`mm.Resolve` only looks up, never creates, so these must exist first:

1. In Mattermost: create the flip7 **bot account** and generate its access token
   (`MM_BOT_TOKEN`); create the **team** and an **owner-only channel**; add the
   bot to both.
2. Fill `.env` (`MM_URL`, `MM_BOT_TOKEN`, `MM_TEAM`, `MM_CHANNEL`, owner identity,
   `HMAC_KEY` from `openssl rand -hex 32`, `LISTEN_ADDR`, `INTEGRATION_BASE_URL`);
   `chmod 600 .env`.
3. Run `scripts/register.sh` to create the `/flip7` slash command via
   `mmctl --local` and capture `SLASH_TOKEN_FLIP7` into `.env`. There is **no
   `/start`** — the menu is served by `/flip7`. Restart the container afterward.
4. **WAL-safe back up `flip7.db` BEFORE the first container start** — the
   destructive `005` rebuild runs at first startup and is the point of no return.
   With the Rust container stopped:
   ```bash
   docker run --rm -v flip7_data:/d keinos/sqlite3 \
     sqlite3 /d/flip7.db "PRAGMA wal_checkpoint(TRUNCATE);"
   docker run --rm -v flip7_data:/d alpine cp /d/flip7.db /d/flip7.db.bak
   ```
   (Or copy all three of `flip7.db` / `flip7.db-wal` / `flip7.db-shm` together.)
   The Rust bot runs SQLite in **WAL mode**, so a plain `cp flip7.db` alone can
   drop committed-but-un-checkpointed games.
5. `docker compose build && docker compose up -d`; watch `docker compose logs -f`.

### Slash-command registration

`scripts/register.sh` (`bash -n`-clean, `set -euo pipefail`, never run in CI):
sources `.env` for `MM_TEAM` / `INTEGRATION_BASE_URL`, runs
`docker exec -i mattermost mmctl --local command create …` pointing at
`${INTEGRATION_BASE_URL%/}/slash/flip7` with `--autocomplete`, parses the token
with `jq`, and writes/replaces `SLASH_TOKEN_FLIP7` in `.env` (preserving
`chmod 600`). Requires the MM container running with local mode enabled. Restart
the flip7bot container after running it so it picks up the new token.

## Migration / data notes

The current schema stored Telegram integer ids; `005_mattermost_ids.sql` rebuilds
the affected tables with TEXT columns:

- `games.message_id INTEGER` → `games.post_id TEXT` (nullable; set to NULL for all
  legacy rows so the next render does a fresh `CreatePost`).
- `games.chat_id INTEGER` → `games.channel_id TEXT` (legacy rows set to `''`, then
  backfilled to the owner channel id at startup).
- `chat_last_players.chat_id INTEGER` → `chat_last_players.channel_id TEXT`.

Load-bearing SQLite behaviors handled by the embedded runner (`internal/db`):

- The whole migration batch runs on a **single dedicated connection with
  `foreign_keys=OFF`** for the duration (the 12-step table-rebuild procedure),
  running `PRAGMA foreign_key_check` before commit and only then restoring
  `foreign_keys=ON`. `PRAGMA foreign_keys` is a no-op inside a transaction, so it
  is never relied on per-transaction.
- Rebuilt AUTOINCREMENT tables re-declare `INTEGER PRIMARY KEY AUTOINCREMENT`;
  `sqlite_sequence` keeps new ids above the high-water mark.

**Reconciling with the live sqlx-migrated DB:** the production `flip7.db` already
has the post-004 schema (including `players.rating`) and records applied
migrations in `_sqlx_migrations`. At startup the runner: if `_sqlx_migrations`
exists and is non-empty, **seeds `schema_migrations` as having already applied
001–004** so only `005` runs (re-running `004`'s `ADD COLUMN rating` would crash
with "duplicate column name"); if neither tracking table exists, runs all five on
a fresh DB. A self-check confirms `players.rating` exists before `005`. The
migration parity test (`internal/db/migrate_test.go`) asserts row counts for
`players` / `score_entries` / `game_winners` / `rating_history` are unchanged,
sample `players.rating` values survive, `post_id IS NULL` / `channel_id=''`
post-005, and the new columns are TEXT.

All `players`, `score_entries`, `game_winners`, `rating_history`, and
`players.rating` rows are preserved — that is the valuable history.

### Rollback procedure (WAL-aware)

Because `005` transforms the DB to TEXT columns the **Rust binary cannot read**,
rollback is *"restore the pre-migration backup + redeploy the Rust bot"*, not
"just redeploy Rust". With the Go container **stopped**:

1. **Delete the post-migration `flip7.db-wal` / `flip7.db-shm` sidecars** on the
   `flip7_data` volume. (Critical: leaving the Go-era WAL behind makes SQLite
   replay it over the restored Rust file and corrupt it.)
2. Restore `flip7.db.bak` over `flip7.db`.
3. Redeploy the Rust bot.

The Rust working tree is intentionally retained (until cutover, Step 26) as the
code half of this rollback.

## Smoke tests

Automated gate: `make check`. The end-to-end acceptance checklist to run against a
live Mattermost stack lives in [`SMOKE_TEST.md`](SMOKE_TEST.md).
