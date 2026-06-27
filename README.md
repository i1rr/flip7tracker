# Flip7 Tracker Bot

A bot for tracking scores in the physical card game **Flip7**. One host manages the game: adds players, enters scores after each hand, and the bot maintains a live scoreboard, resolving the winner at a manual End Game (top score ≥ 200).

> **Migration complete: Telegram (Rust) → Mattermost (Go).**
> The active and only implementation is the **Go Mattermost bot** under
> [`mmbot/`](mmbot/). It is at feature parity with the original Rust/teloxide
> Telegram bot and reuses the same `flip7.db` SQLite data (via an additive,
> data-preserving migration). The Rust tree (`src/`, `Cargo.toml`, etc.) has been
> retired from the working tree; its source remains recoverable from git history
> (the pre-cutover commit) as the code half of the documented rollback.
>
> See [`mmbot/CLAUDE.md`](mmbot/CLAUDE.md) for the full Mattermost architecture,
> environment variables, first-deploy provisioning order, Docker-on-bridge
> deployment, slash-command registration, and the WAL-aware migration/rollback
> procedure, and [`mmbot/SMOKE_TEST.md`](mmbot/SMOKE_TEST.md) for the acceptance
> checklist.

## Go / Mattermost bot (`mmbot/`, active)

The Go bot keeps a **live scoreboard post edited in place** in a single owner-only
Mattermost channel, with a thread per game. The Telegram inline-keyboard /
force-reply mechanics are replaced by interactive **buttons** (`POST /action`) and
**dialogs** (`POST /dialog`) carrying **HMAC-signed nav-state**; the five Telegram
commands collapse to one `/flip7` slash command. There are no in-memory sessions —
every interaction re-reads SQLite by `game_id` and re-renders.

```bash
cd mmbot
make check                 # build + vet + test + gofmt gate
go build -o mmbot ./cmd/mmbot
```

Deployment is **Docker Compose on the existing Mattermost bridge** (fixed IP
`172.28.0.5` on `172.28.0.0/16`, inbound port `8068` not published to the host),
reusing the external `flip7_data` volume:

```bash
cd mmbot
cp .env.example .env        # fill in MM connection, owner, HMAC_KEY; chmod 600 .env
# (provision the MM bot/team/channel first — see mmbot/CLAUDE.md)
./scripts/register.sh       # create /flip7 and capture SLASH_TOKEN_FLIP7
# back up flip7.db (WAL-safe) BEFORE the first start — see mmbot/CLAUDE.md
docker compose build && docker compose up -d
```

Configuration env vars, the provisioning order, and the rollback procedure are
documented in [`mmbot/CLAUDE.md`](mmbot/CLAUDE.md).

## Features

- Live scoreboard, edited in-place after every score entry
- Auto-winner detection at 200+ points with tie-breaking (incl. multi-winner ties)
- New game setup with inline player management
- Load & resume unfinished games
- Hall of Fame statistics with Elo rating
- Per-player detail cards with win rate and avg score
- SQLite persistence across restarts
- Docker deployment on the Mattermost bridge

## Database Schema

```
players       (id, name UNIQUE, is_deleted, rating, created_at)
games         (id, channel_id TEXT, post_id TEXT, started_at, finished_at, winner_player_id)
game_players  (game_id, player_id)
score_entries (id, game_id, player_id, points, created_at)
game_winners  (game_id, player_id)
rating_history(...)
```

Scores are computed as `SUM(score_entries.points)` per player per game.
`score_entries` is an append-only audit log; editing removes the last entry.
Soft-deleted players are preserved in historical stats. Ratings use an Elo model
(see [`mmbot/internal/rating`](mmbot/internal/rating/) and
[`mmbot/CLAUDE.md`](mmbot/CLAUDE.md) for the migration/schema details).

## Backup

Add to host crontab for daily SQLite backups (keeps 30 days):

```bash
0 3 * * * docker run --rm \
  -v flip7_data:/data \
  -v /opt/backups:/backup \
  keinos/sqlite3 \
  sqlite3 /data/flip7.db ".backup /backup/flip7_$(date +\%Y\%m\%d).db" \
  && find /opt/backups -name "*.db" -mtime +30 -delete
```

> The Go bot runs SQLite in WAL mode; `.backup` is WAL-safe. See
> [`mmbot/CLAUDE.md`](mmbot/CLAUDE.md) for the WAL-aware pre-migration backup and
> rollback procedure.

## History

The original implementation was a Telegram bot written in Rust (teloxide + SQLx).
It was migrated to the Go/Mattermost bot under [`mmbot/`](mmbot/); the Rust tree
(`src/`, `Cargo.toml`, the old `Dockerfile`/`docker-compose.yml`, `migrations/`,
etc.) was retired from the working tree in the cutover commit and remains
recoverable from git history.
