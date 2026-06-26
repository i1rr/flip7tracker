# Flip7 Tracker Bot

A bot for tracking scores in the physical card game **Flip7**. One host manages the game: adds players, enters scores after each hand, and the bot maintains a live scoreboard, resolving the winner at a manual End Game (top score ≥ 200).

> **Migration in progress: Telegram (Rust) → Mattermost (Go).**
> The current/active implementation is the **Go Mattermost bot** under
> [`mmbot/`](mmbot/). It is at feature parity with the original Rust/teloxide
> Telegram bot and reuses the same `flip7.db` SQLite data (via an additive,
> data-preserving migration). The Rust tree (`src/`, `Cargo.toml`, etc.) is
> retained for now only as the code half of the documented rollback and will be
> retired in a dedicated cutover commit once the Go bot is validated in
> production.
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

---

## Legacy Telegram bot (Rust, retained for rollback)

The sections below describe the original Telegram/Rust implementation. It is no
longer the deployment target.

## Features

- Live scoreboard, edited in-place after every score entry
- Auto-winner detection at 200+ points with tie-breaking
- New game setup with inline player management
- Load & resume unfinished games
- Hall of Fame statistics with Elo-style rating
- Per-player detail cards with win rate and avg score
- SQLite persistence across restarts
- Docker deployment with Alpine (~15 MB image)

## Tech Stack

| Component  | Choice                        |
|------------|-------------------------------|
| Language   | Rust (stable, 1.80+)          |
| Telegram   | teloxide 0.13                 |
| Async      | tokio                         |
| Database   | SQLx 0.8 + SQLite             |
| Config     | dotenvy + serde + envy        |
| Deployment | Docker multi-stage, Alpine    |

## Quick Start

### Local

```bash
cp .env.example .env
# Edit .env: set BOT_TOKEN and optionally ALLOWED_CHAT_IDS
cargo run
```

### Docker

```bash
cp .env.example .env
# Edit .env
docker compose build
docker compose up -d
docker compose logs -f
```

Migrations run automatically on startup. The SQLite database is stored in a Docker volume (`flip7_data`).

## Configuration

Copy `.env.example` to `.env` and fill in the values:

```env
BOT_TOKEN=123456:your-token-here

# Optional: restrict to specific chat IDs (comma-separated, no spaces)
# Leave empty to allow all chats (dev mode)
ALLOWED_CHAT_IDS=-100123456,-100789012

DATABASE_URL=sqlite:///data/flip7.db
LOG_LEVEL=info
```

## Bot Commands

| Command       | Description                              |
|---------------|------------------------------------------|
| `/start`      | Open main menu                           |
| `/menu`       | Open main menu (resets current session)  |
| `/scoreboard` | Re-send scoreboard to bottom of chat     |
| `/cancel`     | Cancel current action                    |
| `/help`       | Show help text                           |

## Database Schema

```
players       (id, name UNIQUE, is_deleted, created_at)
games         (id, chat_id, message_id, started_at, finished_at, winner_player_id)
game_players  (game_id, player_id)
score_entries (id, game_id, player_id, points, created_at)
```

Scores are computed as `SUM(score_entries.points)` per player per game. `score_entries` is an append-only audit log; editing removes the last entry. Soft-deleted players are preserved in historical stats.

## Rating Formula

```
rating = 1000 + (wins × 100) + (win_rate% × 50) + (avg_score_per_game × 0.5)
```

## Backup

Add to host crontab for daily SQLite backups (keeps 30 days):

```bash
0 3 * * * docker run --rm \
  -v flip7bot_flip7_data:/data \
  -v /opt/backups:/backup \
  keinos/sqlite3 \
  sqlite3 /data/flip7.db ".backup /backup/flip7_$(date +\%Y\%m\%d).db" \
  && find /opt/backups -name "*.db" -mtime +30 -delete
```

## Notes

- FSM state is in-memory only. On bot restart, conversation state is cleared but game data is fully preserved in SQLite. Users resume via **Load Game**.
- The `bundled` SQLx feature compiles libsqlite3 statically, no system sqlite3 needed.
- `su-exec` is used instead of `gosu` for Alpine compatibility.
