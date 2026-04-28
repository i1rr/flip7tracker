use teloxide::prelude::*;
use teloxide::types::{ChatId, MessageId, ParseMode};
use sqlx::SqlitePool;

use crate::db::queries;
use crate::handlers::{HandlerResult, MyDialogue, State};
use crate::keyboards;
use crate::utils::rating;
use crate::utils::scoreboard;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

fn q_msg(q: &CallbackQuery) -> Option<(ChatId, MessageId)> {
    q.message.as_ref().and_then(|m| {
        m.regular_message().map(|r| (m.chat().id, r.id))
    })
}

/// Edit an existing message to show the scoreboard (for cancel/back flows).
async fn edit_to_scoreboard(
    bot: &Bot,
    pool: &SqlitePool,
    game_id: i64,
    chat_id: ChatId,
    msg_id: MessageId,
) -> HandlerResult {
    let players = queries::get_game_players(pool, game_id).await?;
    let scores = queries::get_game_scores(pool, game_id).await?;
    let text = scoreboard::render_scoreboard(&scores, &players, game_id);
    let kb = keyboards::game::game_keyboard(game_id, &players);
    let _ = bot
        .edit_message_text(chat_id, msg_id, text)
        .parse_mode(ParseMode::Html)
        .reply_markup(kb)
        .await;
    Ok(())
}

// ---------------------------------------------------------------------------
// Win screen text
// ---------------------------------------------------------------------------
fn build_win_text(
    winner_names: &[String],
    winner_score: i64,
    scores: &std::collections::HashMap<i64, i64>,
    players: &[crate::db::models::Player],
    deltas: &[rating::EloDelta],
) -> String {
    let medals = ["🥇", "🥈", "🥉"];
    let mut sorted: Vec<(&crate::db::models::Player, i64)> = players
        .iter()
        .map(|p| (p, *scores.get(&p.id).unwrap_or(&0)))
        .collect();
    sorted.sort_by(|a, b| b.1.cmp(&a.1));

    let header = if winner_names.len() == 1 {
        format!("🏆 WINNER: {} with {} pts!", winner_names[0], winner_score)
    } else {
        format!(
            "🏆 JOINT WINNERS: {} — tied at {} pts!",
            winner_names.join(", "),
            winner_score
        )
    };

    let mut lines = vec![header, String::new(), "Final Scores:".to_string()];
    for (i, (player, score)) in sorted.iter().enumerate() {
        let medal = if i < 3 { medals[i] } else { "  " };
        let delta_str = deltas
            .iter()
            .find(|d| d.player_id == player.id)
            .map(|d| {
                let sign = if d.delta >= 0.0 { "+" } else { "" };
                format!("   ({}{:.0} → {:.0})", sign, d.delta, d.rating_after)
            })
            .unwrap_or_default();
        lines.push(format!("{} {}   {} pts{}", medal, player.name, score, delta_str));
    }
    lines.push(String::new());
    lines.push("📈 Rating changes shown in parentheses.".to_string());
    lines.join("\n")
}

// ---------------------------------------------------------------------------
// /scoreboard command
// ---------------------------------------------------------------------------
pub async fn handle_scoreboard_command(
    bot: Bot,
    dialogue: MyDialogue,
    msg: Message,
    pool: SqlitePool,
) -> HandlerResult {
    let chat_id = msg.chat.id;
    let games = queries::get_unfinished_games(&pool, chat_id.0).await?;
    if games.is_empty() {
        bot.send_message(chat_id, "No active game.").await?;
        return Ok(());
    }
    let game = &games[0];
    let players = queries::get_game_players(&pool, game.id).await?;
    let scores = queries::get_game_scores(&pool, game.id).await?;
    let text = scoreboard::render_scoreboard(&scores, &players, game.id);
    let kb = keyboards::game::game_keyboard(game.id, &players);
    if let Some(old_id) = game.message_id {
        let _ = bot.delete_message(chat_id, MessageId(old_id as i32)).await;
    }
    let sent = bot
        .send_message(chat_id, &text)
        .parse_mode(ParseMode::Html)
        .reply_markup(kb)
        .await?;
    queries::update_game_message_id(&pool, game.id, sent.id.0 as i64).await?;
    dialogue.update(State::GameActive { game_id: game.id }).await?;
    Ok(())
}

// ---------------------------------------------------------------------------
// State::GameActive callbacks
// ---------------------------------------------------------------------------
pub async fn handle_game_callback(
    bot: Bot,
    dialogue: MyDialogue,
    q: CallbackQuery,
    pool: SqlitePool,
    game_id: i64,
) -> HandlerResult {
    log::info!("game_callback game_id={}: {:?}", game_id, q.data);
    let (chat_id, msg_id) = match q_msg(&q) {
        Some(v) => v,
        None => {
            bot.answer_callback_query(&q.id).await?;
            return Ok(());
        }
    };
    let data = q.data.as_deref().unwrap_or("");

    // ── Player tapped — go straight to score entry ────────────────────────────
    // Keep the full game keyboard visible so a misclick can be corrected by
    // tapping the right player (the same callback re-fires with a new player_id).
    let score_prefix = format!("score:player:{}:", game_id);
    if let Some(rest) = data.strip_prefix(&score_prefix) {
        if let Ok(player_id) = rest.parse::<i64>() {
            bot.answer_callback_query(&q.id).await?;
            let players = queries::get_game_players(&pool, game_id).await?;
            let player_name = players
                .iter()
                .find(|p| p.id == player_id)
                .map(|p| p.name.clone())
                .unwrap_or_else(|| "Unknown".to_string());
            let kb = keyboards::game::game_keyboard(game_id, &players);
            let _ = bot
                .edit_message_text(
                    chat_id,
                    msg_id,
                    format!("Enter score for {} (0–999):", player_name),
                )
                .reply_markup(kb)
                .await;
            dialogue
                .update(State::GameAddScoreEnterPoints { game_id, player_id })
                .await?;
            return Ok(());
        }
    }

    // ── Edit Last ────────────────────────────────────────────────────────────
    if data == format!("game:edit:{}", game_id) {
        let entry_opt: Option<crate::db::models::ScoreEntry> = sqlx::query_as(
            "SELECT id, game_id, player_id, points, created_at \
             FROM score_entries WHERE game_id = ? ORDER BY id DESC LIMIT 1",
        )
        .bind(game_id)
        .fetch_optional(&pool)
        .await?;

        match entry_opt {
            None => {
                bot.answer_callback_query(&q.id)
                    .text("No score entries to edit.")
                    .show_alert(true)
                    .await?;
            }
            Some(entry) => {
                bot.answer_callback_query(&q.id).await?;
                let players = queries::get_game_players(&pool, game_id).await?;
                let player_name = players
                    .iter()
                    .find(|p| p.id == entry.player_id)
                    .map(|p| p.name.as_str())
                    .unwrap_or("Unknown");
                let text = format!(
                    "Last entry: {} — {} pts\nDelete this entry?",
                    player_name, entry.points
                );
                let kb = keyboards::game::edit_confirm_keyboard(game_id);
                let _ = bot
                    .edit_message_text(chat_id, msg_id, text)
                    .reply_markup(kb)
                    .await;
                dialogue.update(State::GameEditConfirm { game_id }).await?;
            }
        }
        return Ok(());
    }

    // ── End Game ─────────────────────────────────────────────────────────────
    if data == format!("game:end:{}", game_id) {
        bot.answer_callback_query(&q.id).await?;
        let players = queries::get_game_players(&pool, game_id).await?;
        let scores = queries::get_game_scores(&pool, game_id).await?;
        let max_score = players
            .iter()
            .map(|p| *scores.get(&p.id).unwrap_or(&0))
            .max()
            .unwrap_or(0);

        let prompt = if max_score >= 200 {
            let winners: Vec<&str> = players
                .iter()
                .filter(|p| *scores.get(&p.id).unwrap_or(&0) == max_score)
                .map(|p| p.name.as_str())
                .collect();
            if winners.len() == 1 {
                format!(
                    "End the game now? {} wins with {} pts.",
                    winners[0], max_score
                )
            } else {
                format!(
                    "End the game now? Joint winners at {} pts: {}.",
                    max_score,
                    winners.join(", ")
                )
            }
        } else {
            "End the game now? No one has reached 200 yet — the game will be discarded and won't be saved to statistics.".to_string()
        };

        let kb = keyboards::game::end_confirm_keyboard(game_id);
        let _ = bot
            .edit_message_text(chat_id, msg_id, prompt)
            .reply_markup(kb)
            .await;
        return Ok(());
    }

    // ── End Game: Cancel ─────────────────────────────────────────────────────
    if data == format!("game:end_cancel:{}", game_id) {
        bot.answer_callback_query(&q.id).await?;
        edit_to_scoreboard(&bot, &pool, game_id, chat_id, msg_id).await?;
        return Ok(());
    }

    // ── End Game: Confirm ────────────────────────────────────────────────────
    if data == format!("game:end_confirm:{}", game_id) {
        bot.answer_callback_query(&q.id).await?;
        let players = queries::get_game_players(&pool, game_id).await?;
        let scores = queries::get_game_scores(&pool, game_id).await?;
        let max_score = players
            .iter()
            .map(|p| *scores.get(&p.id).unwrap_or(&0))
            .max()
            .unwrap_or(0);

        if max_score < 200 {
            // Nobody reached 200 — discard the game so it doesn't pollute stats.
            queries::discard_game(&pool, game_id).await?;
            let _ = bot.delete_message(chat_id, msg_id).await;
            dialogue.update(State::MainMenu).await?;
            bot.send_message(chat_id, "Game discarded — no one reached 200, so it wasn't saved to statistics.")
                .reply_markup(keyboards::menu::main_menu_keyboard())
                .await?;
            return Ok(());
        }

        // One or more winners tied at the highest 200+ score.
        let winners: Vec<&crate::db::models::Player> = players
            .iter()
            .filter(|p| *scores.get(&p.id).unwrap_or(&0) == max_score)
            .collect();
        let winner_ids: Vec<i64> = winners.iter().map(|p| p.id).collect();
        let winner_names: Vec<String> = winners.iter().map(|p| p.name.clone()).collect();

        // Compute Elo updates from final scores + each player's current rating.
        let elo_entries: Vec<rating::EloEntry> = players
            .iter()
            .map(|p| rating::EloEntry {
                player_id: p.id,
                score: *scores.get(&p.id).unwrap_or(&0),
                rating: p.rating,
            })
            .collect();
        let deltas = rating::compute_updates(&elo_entries);

        queries::finish_game_with_winners(&pool, game_id, &winner_ids, &deltas).await?;
        let last_ids: Vec<i64> = players.iter().map(|p| p.id).collect();
        let _ = queries::save_last_players(&pool, chat_id.0, &last_ids).await;

        let _ = bot.delete_message(chat_id, msg_id).await;
        let win_text = build_win_text(&winner_names, max_score, &scores, &players, &deltas);
        dialogue.update(State::MainMenu).await?;
        bot.send_message(chat_id, &win_text)
            .reply_markup(keyboards::game::win_keyboard())
            .await?;
        return Ok(());
    }

    // ── Win screen / post-game navigation ────────────────────────────────────
    if data == "game:new" || data == "game:home" {
        bot.answer_callback_query(&q.id).await?;
        let _ = bot.delete_message(chat_id, msg_id).await;
        dialogue.update(State::MainMenu).await?;
        bot.send_message(chat_id, "Welcome to Flip7! Choose an option:")
            .reply_markup(keyboards::menu::main_menu_keyboard())
            .await?;
        return Ok(());
    }

    if data == "game:stats" {
        bot.answer_callback_query(&q.id).await?;
        let _ = bot.delete_message(chat_id, msg_id).await;
        dialogue.update(State::StatsView).await?;
        crate::handlers::statistics::show_stats(&bot, chat_id, &pool).await?;
        return Ok(());
    }

    bot.answer_callback_query(&q.id).await?;
    Ok(())
}

// ---------------------------------------------------------------------------
// State::GameAddScoreEnterPoints callback handler
// ---------------------------------------------------------------------------
// While the user is mid-entry, the game keyboard stays attached to the prompt
// so they can correct a misclick or jump to Edit Last / End Game without
// having to type a throwaway score first. The callbacks themselves are
// identical to the GameActive ones, so we just forward.
pub async fn handle_score_entry_callback(
    bot: Bot,
    dialogue: MyDialogue,
    q: CallbackQuery,
    pool: SqlitePool,
    (game_id, _player_id): (i64, i64),
) -> HandlerResult {
    handle_game_callback(bot, dialogue, q, pool, game_id).await
}

// ---------------------------------------------------------------------------
// State::GameAddScoreEnterPoints message handler
// ---------------------------------------------------------------------------
pub async fn handle_enter_points(
    bot: Bot,
    dialogue: MyDialogue,
    (game_id, player_id): (i64, i64),
    msg: Message,
    pool: SqlitePool,
) -> HandlerResult {
    let chat_id = msg.chat.id;
    let text = msg.text().unwrap_or("").trim();

    let pts = match text.parse::<i64>() {
        Ok(v) => v,
        Err(_) => {
            bot.send_message(chat_id, "Please enter a valid number (0–999).").await?;
            return Ok(());
        }
    };

    if pts < 0 || pts > 999 {
        bot.send_message(chat_id, "Score must be between 0 and 999.").await?;
        return Ok(());
    }

    queries::add_score_entry(&pool, game_id, player_id, pts).await?;

    // Per Flip 7 rules, the round must finish before the game ends, so we never
    // auto-end here even if a player has crossed 200 — that's the End Game
    // button's job.
    let players = queries::get_game_players(&pool, game_id).await?;
    let player_name = players
        .iter()
        .find(|p| p.id == player_id)
        .map(|p| p.name.as_str())
        .unwrap_or("Unknown");

    let game_vec = queries::get_unfinished_games(&pool, chat_id.0).await?;
    if let Some(game) = game_vec.into_iter().find(|g| g.id == game_id) {
        // Turn the prompt message in place into a permanent record of the
        // entry, then push a fresh scoreboard at the bottom. The record line
        // sits just above the user's typed number, giving a clean trail of
        // who got what without any extra typing or deletions.
        if let Some(prompt_msg_id) = game.message_id {
            let record = format!("✏️ {}: {}", player_name, pts);
            let _ = bot
                .edit_message_text(chat_id, MessageId(prompt_msg_id as i32), record)
                .await;
        }
        let scores = queries::get_game_scores(&pool, game_id).await?;
        let board_text = scoreboard::render_scoreboard(&scores, &players, game_id);
        let kb = keyboards::game::game_keyboard(game_id, &players);
        if let Ok(sent) = bot
            .send_message(chat_id, board_text)
            .parse_mode(ParseMode::Html)
            .reply_markup(kb)
            .await
        {
            let _ = queries::update_game_message_id(&pool, game_id, sent.id.0 as i64).await;
        }
    }
    dialogue.update(State::GameActive { game_id }).await?;
    Ok(())
}

// ---------------------------------------------------------------------------
// State::GameEditConfirm callbacks
// ---------------------------------------------------------------------------
pub async fn handle_edit_confirm_callback(
    bot: Bot,
    dialogue: MyDialogue,
    q: CallbackQuery,
    pool: SqlitePool,
    game_id: i64,
) -> HandlerResult {
    bot.answer_callback_query(&q.id).await?;
    let (chat_id, msg_id) = match q_msg(&q) {
        Some(v) => v,
        None => return Ok(()),
    };
    let data = q.data.as_deref().unwrap_or("");

    // ── Confirm delete ────────────────────────────────────────────────────────
    if data == format!("edit:confirm:{}", game_id) {
        queries::delete_last_score_entry(&pool, game_id).await?;

        let players = queries::get_game_players(&pool, game_id).await?;
        let scores = queries::get_game_scores(&pool, game_id).await?;
        let board_text = scoreboard::render_scoreboard(&scores, &players, game_id);
        let kb = keyboards::game::game_keyboard(game_id, &players);
        let _ = bot
            .edit_message_text(chat_id, msg_id, board_text)
            .parse_mode(ParseMode::Html)
            .reply_markup(kb)
            .await;

        queries::update_game_message_id(&pool, game_id, msg_id.0 as i64).await?;
        dialogue.update(State::GameActive { game_id }).await?;
        return Ok(());
    }

    // ── Cancel ────────────────────────────────────────────────────────────────
    if data == format!("edit:cancel:{}", game_id) {
        edit_to_scoreboard(&bot, &pool, game_id, chat_id, msg_id).await?;
        queries::update_game_message_id(&pool, game_id, msg_id.0 as i64).await?;
        dialogue.update(State::GameActive { game_id }).await?;
        return Ok(());
    }

    Ok(())
}
