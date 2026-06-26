# flip7bot (mmbot) — Acceptance / Smoke-Test Checklist

This is the Step-25 acceptance checklist. The **automated** items can be verified
on any dev box; the **live-stack** items must be run by the operator against the
real Mattermost deployment (they cannot be checked in CI or a headless env).

---

## 0. Back up the live DB FIRST (point of no return)

The destructive `005` rebuild runs at the **first** container start, so back up
**before** the very first `docker compose up`. The Rust bot runs SQLite in WAL
mode, so a plain `cp flip7.db` can drop committed-but-un-checkpointed games.

With the **Rust container stopped**:

```bash
docker run --rm -v flip7_data:/d keinos/sqlite3 \
  sqlite3 /d/flip7.db "PRAGMA wal_checkpoint(TRUNCATE);"
docker run --rm -v flip7_data:/d alpine cp /d/flip7.db /d/flip7.db.bak
# (or copy all three: flip7.db, flip7.db-wal, flip7.db-shm)
```

- [ ] Rust container stopped, WAL checkpointed, `flip7.db.bak` created on the volume.

---

## 1. Automated checks (verifiable without a live MM stack)

Run from `~/flip7bot/mmbot`:

- [x] `go build ./...` succeeds.
- [x] `go vet ./...` clean.
- [x] `go test ./...` all packages pass.
- [x] `gofmt -l .` prints nothing (i.e. `make check` is fully green).
- [x] `go build -o mmbot ./cmd/mmbot` produces the binary.
- [x] `bash -n scripts/register.sh` clean.
- [x] `docker compose -f docker-compose.yml config` validates (with a `.env` present).

> The boxes above are pre-ticked because they were verified by the implementation
> agent in this repo. Re-run `make check` after any change to confirm.

---

## 2. Live-stack acceptance (operator must run against real Mattermost)

These require a running Mattermost stack, the provisioned bot/team/channel, a
populated `.env`, the registered `/flip7` command, and the deployed container.
**Do not assume these pass until you have actually performed them.**

### Health & networking
- [ ] `/healthz` returns `200 ok` from a container on the MM bridge
      (e.g. `docker exec mattermost wget -qO- http://172.28.0.5:8068/healthz`).
- [ ] `/healthz` (and port 8068) is **NOT** reachable from the LAN / host.

### Slash command & menu
- [ ] `/flip7` posts the main menu (New Game / Load Game / Stats / Players).
- [ ] `/flip7 help` returns an ephemeral help message.
- [ ] `/flip7 scoreboard` re-renders the current scoreboard (if a game is active).

### New game
- [ ] New Game with **known players** selected starts a game and posts a
      scoreboard **thread** with a stored post id.
- [ ] Adding a **new player via dialog** during setup works and the player appears.

### Score entry & live scoreboard
- [ ] Tapping a player opens a dialog; typing points (0–999) updates the
      scoreboard **in place** (`UpdatePost`) and posts a **thread reply**.
- [ ] **Edit last** deletes the last entry and re-renders (the specific
      score-entry id, so a replay is safe). Elo is unaffected until End Game.

### End game
- [ ] End Game with a max total **≥ 200** detects the winner(s), including
      **ties / multi-winner**, and writes `game_winners` + `rating_history`; Elo
      ratings update.
- [ ] End Game with a max total **< 200** **discards** the game (rows deleted, no
      Elo).
- [ ] Double-tapping End Game / discard is a safe no-op (idempotent re-render).

### Load / resume
- [ ] Load Game resumes an unfinished game into a **fresh thread** (new scoreboard
      post; legacy post id was nulled).
- [ ] A **pre-migration** unfinished game is listable and resumable (channel
      backfill claimed it).

### Players
- [ ] Rename a player; the change is reflected.
- [ ] Delete a player (soft delete); historical stats still show them.
- [ ] Player-list **pagination** works.

### Stats
- [ ] Hall of Fame renders with Elo ordering.
- [ ] Per-player detail renders with **preserved historical Elo** (pre-migration
      ratings continue from where they were).

### Security
- [ ] A tampered / forged `context` fails HMAC → benign "expired" ephemeral
      (no crash, no action taken).
- [ ] A non-owner `user_id` is **rejected** on `/slash`, `/action`, `/dialog`.

### Data integrity
- [ ] Pre-migration history is intact (spot-check player ratings, past winners).
- [ ] `make check` is green on the deployed commit.

---

## Rollback (if a live item fails irrecoverably)

With the Go container stopped:
1. Delete the post-migration `flip7.db-wal` / `flip7.db-shm` sidecars on the
   `flip7_data` volume.
2. Restore `flip7.db.bak` over `flip7.db`.
3. Redeploy the Rust bot.
