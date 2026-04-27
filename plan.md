# Flip7 Tracker Bot

A Telegram bot for tracking scores in the physical card game Flip7. One host manages the game: adds players, enters scores after each hand, and the bot maintains the live scoreboard, auto-detecting the winner at 200+ points.

---

## Tech Stack

| Component   | Choice                          | Rationale                                                      |
|-------------|----------------------------------|----------------------------------------------------------------|
| Language    | Rust (stable)                   | Memory safety, performance, small Docker footprint             |
| Telegram    | teloxide 0.13                   | Most mature Rust Telegram library, native FSM Dialogue, async  |
| Async       | tokio                           | Standard async runtime, required by teloxide                   |
| Database    | SQLx 0.8 + SQLite               | Async, runtime SQL execution, built-in migrations              |
| Migrations  | sqlx-migrate (built into SQLx)  | No extra tooling, version-controlled migration files           |
| Config      | dotenvy + serde + envy          | .env loading + typed deserialization into a config struct       |
| Deployment  | Docker (multi-stage build)      | Builder: rust:1.85-alpine; Runtime: alpine:3.20 (~15MB image) |
| Persistence | Docker volume for SQLite file   | Data survives container restarts and re-deploys               |

### Key Cargo dependencies
```toml
[package]
edition = "2021"
rust-version = "1.80"

[dependencies]
teloxide       = { version = "0.13", features = ["macros"] }
tokio          = { version = "1", features = ["full"] }
sqlx           = { version = "0.8", features = ["sqlite", "runtime-tokio", "migrate", "chrono", "bundled"] }
serde          = { version = "1", features = ["derive"] }
dotenvy        = "0.15"
envy           = "0.4"
chrono         = { version = "0.4", features = ["serde"] }
log            = "0.4"
env_logger     = "0.11"
```

> `bundled` feature compiles libsqlite3 from source — required for musl/Alpine Docker builds.
> All SQL uses `sqlx::query_as` (dynamic, runtime verification) — no `query!` macros, no `cargo sqlx prepare` step needed.

---

## Project Structure

```
flip7bot/
├── src/
│   ├── main.rs
│   ├── config.rs
│   ├── db/
│   │   ├── mod.rs
│   │   ├── models.rs
│   │   └── queries.rs
│   ├── handlers/
│   │   ├── mod.rs
│   │   ├── menu.rs
│   │   ├── new_game.rs
│   │   ├── game.rs
│   │   ├── load_game.rs
│   │   └── statistics.rs
│   ├── keyboards/
│   │   ├── mod.rs
│   │   ├── menu.rs
│   │   ├── new_game.rs
│   │   └── game.rs
│   └── utils/
│       ├── mod.rs
│       ├── scoreboard.rs
│       └── rating.rs
├── migrations/
│   └── 001_initial.sql
├── Cargo.toml
├── Dockerfile
├── entrypoint.sh
├── docker-compose.yml
├── .env
├── .env.example
└── plan.md
```

---

## Database Schema

```
players      (id PK, name UNIQUE NOT NULL, is_deleted INTEGER DEFAULT 0, created_at TEXT)
games        (id PK, chat_id, message_id, started_at, finished_at, winner_player_id FK)
game_players (game_id FK, player_id FK, PRIMARY KEY (game_id, player_id))
score_entries(id PK, game_id FK, player_id FK, points INTEGER, created_at)
```

- Cumulative scores computed via `SUM(score_entries.points)` per player per game.
- `score_entries` acts as an audit log; deleting the last entry is the edit mechanism.
- `games.finished_at IS NULL` = active (unfinished) game.
- `players.is_deleted = TRUE` = soft-deleted; preserved in historical stats.

---

## UX Reference

**Main Menu:**
```
[🎮 New Game]
[📂 Load Game]
[📊 Statistics]
```

**New Game Setup:**
```
Players added: Alice, Bob

[Alice ✕]  [Bob ✕]
[+ New Player]  [+ Known Player]
[← Back]
────────────────
[▶ Start Game]   ← enabled only with ≥ 2 players
```

**Live Scoreboard (edited in-place after every score entry):**
```
🎮 Flip 7 — Game #5

🥇 1. Alice      175 pts
🥈 2. Bob        140 pts
🥉 3. Charlie     95 pts
   4. Dave        80 pts

🏁 First to 200 wins!

[+ Add Score]  [✏️ Edit Last]  [🚩 End Game]
```

**Win Screen (auto-triggered at ≥ 200):**
```
🏆 WINNER: Alice with 207 pts!

Final Scores:
🥇 Alice   207
🥈 Bob     140
🥉 Charlie  95
   Dave     80

[📊 View Stats]  [🎮 New Game]
```

**Statistics — Hall of Fame:**
```
📊 Flip 7 — Hall of Fame

#  Player     Games  Wins  Win%  Avg Score  Rating
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
🥇 Alice        12     5   42%     142 pts   1380
🥈 Bob           8     3   38%     118 pts   1240
🥉 Charlie       6     1   17%      95 pts   1090

[Alice]  [Bob]  [Charlie]  [← Back]
```

---

## SECTION 1 — Project Setup

**Step 1.1** `cargo new flip7bot`, create all subdirectories, `.gitignore` (exclude `.env`, `*.db`, `target/`, `.sqlx/`), `.env.example`. Set `edition = "2021"` and `rust-version = "1.80"` in `Cargo.toml`.

**Step 1.2** Set up `Cargo.toml` with all dependencies pinned to exact minor versions (see Tech Stack section). Commit `Cargo.lock` to version control.

**Step 1.3** Implement `config.rs` — a `Config` struct derived with `serde::Deserialize`, populated from env via `envy` after loading `.env` with `dotenvy`:
```rust
#[derive(Deserialize, Debug)]
pub struct Config {
    pub bot_token: String,
    #[serde(default = "default_database_url")]
    pub database_url: String,
    #[serde(default)]
    pub allowed_chat_ids: Vec<i64>,   // empty = allow all (dev mode)
    #[serde(default = "default_log_level")]
    pub log_level: String,
}
fn default_database_url() -> String { "sqlite:///data/flip7.db".into() }
fn default_log_level() -> String { "info".into() }
```
`envy` respects `serde` default functions — missing env vars will use these defaults.
`Vec<i64>` is parsed from a comma-separated env var: `ALLOWED_CHAT_IDS=-100123456,-100789012` (no spaces). Document this format in `.env.example`.

**Step 1.4** Configure logging via `env_logger` initialized from `config.log_level`. Ensure `bot_token` is never printed at any log level (use `{:?}` only on safe types).

**Step 1.5** `main.rs` entry point skeleton:
```rust
#[tokio::main(flavor = "multi_thread")]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    dotenvy::dotenv().ok();
    let config = envy::from_env::<Config>()?;
    env_logger::Builder::from_env(Env::default().default_filter_or(&config.log_level)).init();
    // pool, migrations, bot init, dispatcher...
}
```

---

## SECTION 2 — Database Layer

**Step 2.1** Define Rust model structs in `models.rs` derived with `sqlx::FromRow`:
```rust
pub struct Player { pub id: i64, pub name: String, pub is_deleted: bool, pub created_at: DateTime<Utc> }
pub struct Game { pub id: i64, pub chat_id: i64, pub message_id: Option<i64>, pub started_at: DateTime<Utc>, pub finished_at: Option<DateTime<Utc>>, pub winner_player_id: Option<i64> }
pub struct GamePlayer { pub game_id: i64, pub player_id: i64 }
pub struct ScoreEntry { pub id: i64, pub game_id: i64, pub player_id: i64, pub points: i64, pub created_at: DateTime<Utc> }
// PlayerStats does NOT derive FromRow — constructed manually in queries.rs from aggregation results
pub struct PlayerStats {
    pub player_id: i64, pub player_name: String,
    pub games: i64, pub wins: i64, pub losses: i64,
    pub win_rate: f64, pub avg_score: f64,   // avg_score = total_points / games (per game)
    pub highest_score: i64, pub total_points: i64, pub rating: f64
}
```

**Step 2.2** Set up `SqlitePool` via `sqlx::sqlite::SqlitePoolOptions`:
- Use `SqliteConnectOptions` with `.journal_mode(SqliteJournalMode::Wal)` and `.foreign_keys(true)`
- Pool: min 1, max 5 connections
- Pass `pool.clone()` into dptree deps — `SqlitePool` is internally `Arc`-backed, no extra wrapping needed

**Step 2.3** Create `migrations/001_initial.sql`:
- Use `TEXT` for datetime columns (SQLx chrono maps `DateTime<Utc>` to ISO 8601 text)
- Add `DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))` on all `created_at` columns
- Add indexes: `idx_games_chat_id`, `idx_score_entries_game_id`, `idx_game_players_game_id`
- Call `sqlx::migrate!().run(&pool).await?` in `main.rs` before starting the bot

**Step 2.4** Implement `queries.rs` with async functions (all take `&SqlitePool`):
- `create_or_get_player(pool, name) -> Result<Player>` — INSERT OR IGNORE, then SELECT
- `get_all_active_players(pool) -> Result<Vec<Player>>` — WHERE is_deleted = FALSE
- `soft_delete_player(pool, player_id) -> Result<()>`
- `create_game(pool, chat_id) -> Result<Game>`
- `get_unfinished_games(pool, chat_id) -> Result<Vec<Game>>`
- `finish_game(pool, game_id, winner_id: Option<i64>) -> Result<()>`
- `update_game_message_id(pool, game_id, message_id) -> Result<()>`
- `add_player_to_game(pool, game_id, player_id) -> Result<()>` — INSERT OR IGNORE
- `add_score_entry(pool, game_id, player_id, points) -> Result<ScoreEntry>`
- `delete_last_score_entry(pool, game_id) -> Result<Option<ScoreEntry>>` — SELECT ORDER BY id DESC LIMIT 1, then DELETE by id (use id not created_at to avoid timestamp ties)
- `get_game_scores(pool, game_id) -> Result<HashMap<i64, i64>>` — SUM(points) GROUP BY player_id
- `get_game_players(pool, game_id) -> Result<Vec<Player>>`
- `get_player_stats(pool, player_id) -> Result<PlayerStats>`
- `get_all_stats(pool) -> Result<Vec<PlayerStats>>` — only players with ≥1 finished game

---

## SECTION 3 — Bot Core

**Step 3.1** `main.rs` entry point:
- Load config, init logging, create `SqlitePool`, run migrations
- Call `bot.get_me().await` to validate token; on error log and `std::process::exit(1)`
- Build teloxide `Dispatcher` with `dptree::deps![pool.clone(), storage, Arc::new(config)]` for dependency injection
- Add chat_id filter middleware: reject updates where `chat.id` not in `allowed_chat_ids` (if list non-empty)
- Wire graceful shutdown: `tokio::signal::unix::signal(SignalKind::terminate()).expect("SIGTERM handler")` — on SIGTERM, call `dispatcher.shutdown_token().shutdown().await` so `docker stop` drains in-flight updates cleanly
- Start long-polling:
  ```rust
  Dispatcher::builder(bot, handler)
      .dependencies(dptree::deps![pool.clone(), storage, Arc::new(config)])
      .build()
      .dispatch()
      .await;
  ```

**Step 3.2** Define FSM state enum in `handlers/mod.rs`:
```rust
#[derive(Clone, Default, PartialEq, Debug)]
pub enum State {
    #[default] MainMenu,
    NewGameSetup { players: Vec<i64> },
    NewGameTypingName { players: Vec<i64> },
    NewGameKnownPlayers { players: Vec<i64>, page: u32 },
    GameActive { game_id: i64 },
    GameAddScoreSelectPlayer { game_id: i64 },
    GameAddScoreEnterPoints { game_id: i64, player_id: i64 },
    GameEditConfirm { game_id: i64 },
    LoadGameList,
    StatsView,
    StatsPlayerDetail { player_id: i64 },
}
```
Wire storage: `InMemStorage::<State>::new()` passed to `Dispatcher::builder` via `dptree::deps![]`.
Global command handlers registered outside the dialogue tree: `/start`, `/menu` → reset to `State::default()`, send "Session reset. Use /menu to continue."; `/cancel` → same; `/help` → send help text.

**Step 3.3** Callback data encoding: all `InlineKeyboardButton` callback strings use the format `"action:p1:p2"`. Define constants:
```
"game:load:{game_id}"
"game:end:{game_id}"
"game:edit:{game_id}"
"score:player:{game_id}:{player_id}"
"player:add:{player_id}"
"player:remove:{player_id}"
"known:page:{page}"
"stats:player:{player_id}"
"stats:page:{page}"
```
Parse via `split(':').collect::<Vec<_>>()` with explicit length check before indexing.

**Step 3.4** Error handling pattern: every handler returns `Result<(), Box<dyn Error + Send + Sync>>`. On DB or Telegram error: log with `log::error!`, send "Something went wrong, please try again." to user. The dispatcher's error handler logs unhandled errors without crashing.

**Step 3.5** Callback query convention: always call `bot.answer_callback_query(q.id).await?` as the first statement in every callback handler, before any DB work.

---

## SECTION 4 — New Game Flow

**Step 4.1** "New Game" entry: transition dialogue to `State::NewGameSetup`. Setup player list is stored in the `State` variant itself (add `players: Vec<i64>` field). Render setup keyboard and send setup message.

**Step 4.2** `+ New Player` handler:
- Transition to `State::NewGameTypingName`
- On text input: strip whitespace, validate length (1-50 chars), reject control characters
- Call `create_or_get_player(name)` — if player exists (by name) but `is_deleted=true`, un-delete and reuse
- If player_id already in `State::NewGameSetup.players`: reply "Alice is already added"
- Add to players list, return to `State::NewGameSetup`, re-render keyboard

**Step 4.3** `+ Known Player` handler:
- Filter: exclude player IDs already in `State::NewGameSetup { players }` — a player in the setup list must not appear here
- Show paginated inline list of remaining active players (8 per page)
- Navigation: `[← Prev]  Page 1/3  [Next →]`
- One-click add: add player to setup list, return to setup screen
- `[← Back to Setup]` button

**Step 4.4** Player button in setup (e.g., `[Alice ✕]`): inline confirm "Remove Alice?" with `[Yes, Remove]` and `[Keep]`. On confirm: remove from list, re-render.

**Step 4.5** `Start Game` validation:
- Minimum 2 players required; button disabled (shown as grayed-out text) if fewer
- Check `get_unfinished_games(chat_id)` — if any exist, reply "You have an unfinished game. Load it via Load Game or end it first." and abort
- On press: immediately edit the setup message to show "Starting game..." to prevent double-taps
- Then: `create_game(chat_id)`, `add_player_to_game()` for each, send scoreboard message, save `message_id`, transition to `State::GameActive`

---

## SECTION 5 — Active Game

**Step 5.1** `scoreboard.rs` — `render_scoreboard(scores: &HashMap<i64, i64>, players: &[Player]) -> String`:
- Sort players by total score descending
- Medals for positions 1-3 (🥇🥈🥉), numbers for 4+
- Show all players with score (0 if none entered yet)
- Returns formatted text; keyboard built separately with `InlineKeyboardMarkup`

**Step 5.2** Message edit helper in `game.rs`:
```rust
async fn update_scoreboard_message(bot: &Bot, pool: &SqlitePool, game: &Game, text: String, kb: InlineKeyboardMarkup) {
    if let Some(msg_id) = game.message_id {
        match bot.edit_message_text(ChatId(game.chat_id), MessageId(msg_id as i32), &text)
            .reply_markup(kb.clone()).await {
            Ok(_) => {},
            Err(_) => {
                // Message too old or deleted — send new one
                if let Ok(msg) = bot.send_message(ChatId(game.chat_id), &text).reply_markup(kb).await {
                    let _ = update_game_message_id(pool, game.id, msg.id.0 as i64).await;
                }
            }
        }
    }
}
```

**Step 5.3** `+ Add Score` flow:
- At handler entry: verify `game.finished_at IS NULL` — if game already finished, reply "This game is already over." and return
- Show player selection keyboard (all players in game as buttons) + `[← Cancel]` button
- On player select: ask "Enter score for Alice:" (text input) + reply keyboard with `[Cancel]`
- On "Cancel" at any step: reply "Cancelled." and return to `State::GameActive`
- Validate input: trim whitespace, parse as `i64`, reject if `< 0` or `> 999`
- Execute atomically in a single SQLite transaction: INSERT ScoreEntry, SELECT SUM per player, if any total >= 200 then UPDATE game as finished with winner
- On commit: send brief confirmation "✅ Added 35 pts to Alice", then update scoreboard (or show win screen)

**Step 5.4** Win detection: handled inside the transaction in Step 5.3. Winner = player with highest total (in case of tie, the one who triggered the round). If tie exists, show both scores on win screen with note "Tie — first to enter wins."

**Step 5.5** `✏️ Edit Last` flow:
- Query last `ScoreEntry` for this game. If none: reply "No scores to edit yet", stay in `State::GameActive`
- Show: "Last entry: Alice +35 pts (2 min ago). Delete this entry?"
- Buttons: `[🗑 Delete Entry]` and `[← Cancel]`
- On confirm: `delete_last_score_entry(game_id)`, immediately update scoreboard in-place

**Step 5.6** `🚩 End Game` flow:
- Confirm: "End game without declaring a winner?" `[Yes, End]` `[Cancel]`
- On confirm: `finish_game(game_id, winner_id=None)`, send "Game over — no winner declared. [🏠 Main Menu]", transition to `State::MainMenu`

**Step 5.7** Win screen buttons: `[📊 View Stats]`, `[🎮 New Game]`, `[🏠 Main Menu]`.

**Step 5.8** Add `/scoreboard` command: query `get_unfinished_games(chat_id) ORDER BY started_at DESC LIMIT 1` — use the most recent active game. Send fresh scoreboard message and update `game.message_id`. If no active game: reply "No active game."

**Step 5.9** `/help` response:
```
Flip7 Bot — Commands:
/start or /menu — Main menu
/scoreboard — Re-send scoreboard to bottom of chat
/cancel — Cancel current action
/help — Show this message

The host controls the game. Add players, start, then enter scores after each hand.
```

---

## SECTION 6 — Load Game

**Step 6.1** "Load Game" entry: query `get_unfinished_games(chat_id)`. If empty: "No unfinished games found." with `[← Back]`.

**Step 6.2** Show list — one button per game:
```
[Game #3 · Alice, Bob, Charlie · Started Apr 14]
[Game #1 · Dave, Eve · Started Apr 10]
```

**Step 6.3** On game select: restore by fetching `get_game_scores()` and `get_game_players()`, send new scoreboard message (old `message_id` may be stale), update `game.message_id`, transition to `State::GameActive`.

---

## SECTION 7 — Statistics

**Step 7.1** "Finished game" definition for all stats: `finished_at IS NOT NULL` (includes no-winner games). A game where no winner was declared still counts toward `games` played. Only games with `winner_player_id IS NOT NULL` count toward `wins`.

Rating formula:
```
rating = 1000.0 + (wins * 100.0) + (win_rate_pct * 50.0) + (avg_score_per_game * 0.5)
// win_rate_pct is 0.0–100.0 (e.g. 42.0 for 42%), NOT a fraction
// avg_score_per_game = total_points / games (includes 0-point finished games)
```
Only players appearing in ≥1 finished game (`finished_at IS NOT NULL`) are shown in stats.

**Step 7.2** Hall of Fame table (monospace via `<pre>` tag):
```
📊 Flip 7 — Hall of Fame

#   Player      G   W   Win%  Avg   Rating
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
🥇  Alice      12   5   42%  142   1380
🥈  Bob         8   3   38%  118   1240
🥉  Charlie     6   1   17%   95   1090
```
Buttons below: one per player for detail card. Pagination if >10 players.

**Step 7.3** Player detail card:
```
👤 Alice

Games played:   12
Wins:            5  (42%)
Losses:          7
Highest score:  198 pts
Avg per game:   142 pts
Total pts ever: 1704 pts
Rating:         1380

[← Back to Stats]
```

---

## SECTION 8 — Deployment

**Step 8.1** Write multi-stage `Dockerfile`:
```dockerfile
# --- Build stage ---
FROM rust:1.85-alpine AS builder
RUN apk add --no-cache musl-dev
WORKDIR /app
# Cache dependencies separately from source
COPY Cargo.toml Cargo.lock ./
RUN mkdir src && echo "fn main(){}" > src/main.rs && cargo build --release && rm -rf src
# Build actual source
COPY src ./src
COPY migrations ./migrations
RUN touch src/main.rs && cargo build --release

# --- Runtime stage ---
FROM alpine:3.20 AS runtime
RUN apk add --no-cache ca-certificates procps su-exec
RUN addgroup -S bot && adduser -S bot -G bot
WORKDIR /app
COPY --from=builder /app/target/release/flip7bot ./flip7bot
COPY migrations ./migrations
COPY entrypoint.sh ./entrypoint.sh
RUN chmod +x entrypoint.sh
# DATABASE_URL NOT set here — comes from .env at runtime to avoid layer exposure
# No USER directive — entrypoint.sh handles privilege drop via su-exec
CMD ["./entrypoint.sh"]
```
`entrypoint.sh`:
```bash
#!/bin/sh
set -e
# chown /data as root, then drop to bot user via su-exec (Alpine's alternative to gosu)
chown -R bot:bot /data
exec su-exec bot ./flip7bot
```
> `su-exec` is Alpine's equivalent of gosu (gosu requires glibc). `procps` provides `pgrep` for healthcheck.
> `sqlite` package not needed: SQLx `bundled` feature compiles libsqlite3 statically.

**Step 8.2** Write `docker-compose.yml`:
```yaml
services:
  flip7bot:
    build: .
    restart: on-failure        # not unless-stopped: avoids tight loop on bad token
    env_file: .env
    user: root                 # entrypoint needs root briefly to chown /data, then drops to bot
    volumes:
      - flip7_data:/data
    healthcheck:
      test: ["CMD", "pgrep", "flip7bot"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 10s

volumes:
  flip7_data:
```

**Step 8.3** SQLite backup via host cron (`crontab -e` on the Linux server):
```bash
# Daily backup at 3am, keep 30 days
0 3 * * * docker run --rm \
  -v flip7bot_flip7_data:/data \
  -v /opt/backups:/backup \
  keinos/sqlite3 \
  sqlite3 /data/flip7.db ".backup /backup/flip7_$(date +\%Y\%m\%d).db" \
  && find /opt/backups -name "*.db" -mtime +30 -delete
```
> `keinos/sqlite3` image includes sqlite3. Plain `alpine` does not.

**Step 8.4** `.env` permissions: `chmod 600 .env` before first run.

**Step 8.5** Deployment checklist:
1. Clone repo to `/opt/flip7bot` on the Linux server
2. Copy `.env.example` to `.env`, fill `BOT_TOKEN` and `ALLOWED_CHAT_IDS`
3. `docker compose build`
4. `docker compose up -d`
5. `docker compose logs -f` to verify startup and token validation
6. Migrations run automatically on startup (sqlx-migrate in `main.rs`)

---

## FSM Restart Policy

No FSM persistence. On bot restart, all in-progress conversation states are cleared. Game data (players, scores) is fully preserved in SQLite. Users resume via "Load Game". The `/start` command is always available to return to the main menu from any state.
