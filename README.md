# Flip7 Tracker Bot

A Telegram bot for tracking scores in the physical card game **Flip7**. One host manages the game: adds players, enters scores after each hand, and the bot maintains a live scoreboard, auto-detecting the winner at 200+ points.

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
