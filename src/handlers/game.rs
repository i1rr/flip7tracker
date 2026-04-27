use teloxide::prelude::*;
use teloxide::types::{ChatId, MessageId, ParseMode};
use sqlx::SqlitePool;

use crate::db::{models::Game, queries};
use crate::handlers::{HandlerResult, MyDialogue, State};
use crate::keyboards;
use crate::utils::scoreboard;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

fn q_msg(q: &CallbackQuery) -> Option<(ChatId, MessageId)> {
    q.message.as_ref().and_then(|m| {
        m.regular_message().map(|r| (m.chat().id, r.id))
    })
}

/// Delete the tracked scoreboard message and send a fresh one at the bottom.
async fn push_scoreboard(
    bot: &Bot,
    pool: &SqlitePool,
    game: &Game,
    text: &str,
    kb: teloxide::types::InlineKeyboardMarkup,
) {
    let chat_id = ChatId(game.chat_id);
    if let Some(msg_id) = game.message_id {
        let _ = bot.delete_message(chat_id, MessageId(msg_id as i32)).await;
    }
    if let Ok(sent) = bot
        .send_message(chat_id, text)
        .parse_mode(ParseMode::Html)
        .reply_markup(kb)
        .await
    {
        let _ = queries::update_game_message_id(pool, game.id, sent.id.0 as i64).await;
    }
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
    let kb = keyboards::game::game_keyboard(game_id);
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
    winner_name: &str,
    winner_score: i64,
    scores: &std::collections::HashMap<i64, i64>,
    players: &[crate::db::models::Player],
    is_tie: bool,
) -> String {
    let medals = ["🥇", "🥈", "🥉"];
    let mut sorted: Vec<(&crate::db::models::Player, i64)> = players
        .iter()
        .map(|p| (p, *scores.get(&p.id).unwrap_or(&0)))
        .collect();
    sorted.sort_by(|a, b| b.1.cmp(&a.1));

    let mut lines = vec![
        format!("🏆 WINNER: {} with {} pts!", winner_name, winner_score),
        String::new(),
        "Final Scores:".to_string(),
    ];
    for (i, (player, score)) in sorted.iter().enumerate() {
        let medal = if i < 3 { medals[i] } else { "  " };
        lines.push(format!("{} {}   {} pts", medal, player.name, score));
    }
    if is_tie {
        lines.push(String::new());
        lines.push("🤝 Tie — first to enter wins.".to_string());
    }
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
    let kb = keyboards::game::game_keyboard(game.id);
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
    bot.answer_callback_query(&q.id).await?;
    let (chat_id, msg_id) = match q_msg(&q) {
        Some(v) => v,
        None => return Ok(()),
    };
    let data = q.data.as_deref().unwrap_or("");

    // ── Add Score ────────────────────────────────────────────────────────────
    if data == format!("game:add_score:{}", game_id) {
        let players = queries::get_game_players(&pool, game_id).await?;
        let kb = keyboards::game::player_select_keyboard(game_id, &players);
        let _ = bot
            .edit_message_text(chat_id, msg_id, "Select a player to add score for:")
            .reply_markup(kb)
            .await;
        dialogue.update(State::GameAddScoreSelectPlayer { game_id }).await?;
        return Ok(());
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
        let kb = keyboards::game::end_confirm_keyboard(game_id);
        let _ = bot
            .edit_message_text(chat_id, msg_id, "End game without declaring a winner?")
            .reply_markup(kb)
            .await;
        return Ok(());
    }

    // ── End Game: Cancel ─────────────────────────────────────────────────────
    if data == format!("game:end_cancel:{}", game_id) {
        edit_to_scoreboard(&bot, &pool, game_id, chat_id, msg_id).await?;
        return Ok(());
    }

    // ── End Game: Confirm ────────────────────────────────────────────────────
    if data == format!("game:end_confirm:{}", game_id) {
        let game_players = queries::get_game_players(&pool, game_id).await?;
        let last_ids: Vec<i64> = game_players.iter().map(|p| p.id).collect();
        queries::finish_game(&pool, game_id, None).await?;
        let _ = queries::save_last_players(&pool, chat_id.0, &last_ids).await;
        let _ = bot.delete_message(chat_id, msg_id).await;
        dialogue.update(State::MainMenu).await?;
        bot.send_message(chat_id, "Game over — no winner declared.")
            .reply_markup(keyboards::menu::main_menu_keyboard())
            .await?;
        return Ok(());
    }

    // ── Win screen / post-game navigation ────────────────────────────────────
    if data == "game:new" || data == "game:home" {
        let _ = bot.delete_message(chat_id, msg_id).await;
        dialogue.update(State::MainMenu).await?;
        bot.send_message(chat_id, "Welcome to Flip7! Choose an option:")
            .reply_markup(keyboards::menu::main_menu_keyboard())
            .await?;
        return Ok(());
    }

    if data == "game:stats" {
        let _ = bot.delete_message(chat_id, msg_id).await;
        dialogue.update(State::StatsView).await?;
        crate::handlers::statistics::show_stats(&bot, chat_id, &pool).await?;
        return Ok(());
    }

    Ok(())
}

// ---------------------------------------------------------------------------
// State::GameAddScoreSelectPlayer callbacks
// ---------------------------------------------------------------------------
pub async fn handle_select_player_callback(
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

    // ── Cancel ───────────────────────────────────────────────────────────────
    if data == "score:cancel" {
        edit_to_scoreboard(&bot, &pool, game_id, chat_id, msg_id).await?;
        dialogue.update(State::GameActive { game_id }).await?;
        return Ok(());
    }

    // ── Player selected ──────────────────────────────────────────────────────
    let prefix = format!("score:player:{}:", game_id);
    if let Some(rest) = data.strip_prefix(&prefix) {
        if let Ok(player_id) = rest.parse::<i64>() {
            let players = queries::get_game_players(&pool, game_id).await?;
            let player_name = players
                .iter()
                .find(|p| p.id == player_id)
                .map(|p| p.name.clone())
                .unwrap_or_else(|| "Unknown".to_string());
            let _ = bot
                .edit_message_text(
                    chat_id,
                    msg_id,
                    format!("Enter score for {} (0–999):", player_name),
                )
                .await;
            dialogue
                .update(State::GameAddScoreEnterPoints { game_id, player_id })
                .await?;
            return Ok(());
        }
    }

    Ok(())
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

    let scores = queries::get_game_scores(&pool, game_id).await?;
    let players = queries::get_game_players(&pool, game_id).await?;

    // Check for winner (>= 200 pts)
    let winner = players.iter().filter_map(|p| {
        let s = *scores.get(&p.id).unwrap_or(&0);
        if s >= 200 { Some((p.id, s)) } else { None }
    }).max_by_key(|&(_, s)| s);

    if let Some((win_player_id, win_score)) = winner {
        queries::finish_game(&pool, game_id, Some(win_player_id)).await?;
        let last_ids: Vec<i64> = players.iter().map(|p| p.id).collect();
        let _ = queries::save_last_players(&pool, chat_id.0, &last_ids).await;

        let win_name = players
            .iter()
            .find(|p| p.id == win_player_id)
            .map(|p| p.name.clone())
            .unwrap_or_else(|| "Unknown".to_string());

        let tie = players.iter().any(|p| {
            p.id != win_player_id && *scores.get(&p.id).unwrap_or(&0) >= 200
        });

        // Delete the "Enter score for X:" prompt
        let game_vec = queries::get_unfinished_games(&pool, chat_id.0).await;
        if let Ok(gv) = game_vec {
            if let Some(g) = gv.into_iter().find(|g| g.id == game_id) {
                if let Some(mid) = g.message_id {
                    let _ = bot.delete_message(chat_id, MessageId(mid as i32)).await;
                }
            }
        }

        let win_text = build_win_text(&win_name, win_score, &scores, &players, tie);
        dialogue.update(State::MainMenu).await?;
        bot.send_message(chat_id, &win_text)
            .reply_markup(keyboards::game::win_keyboard())
            .await?;
    } else {
        let game_vec = queries::get_unfinished_games(&pool, chat_id.0).await?;
        if let Some(game) = game_vec.into_iter().find(|g| g.id == game_id) {
            let board_text = scoreboard::render_scoreboard(&scores, &players, game_id);
            let kb = keyboards::game::game_keyboard(game_id);
            push_scoreboard(&bot, &pool, &game, &board_text, kb).await;
        }
        dialogue.update(State::GameActive { game_id }).await?;
    }

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
        let kb = keyboards::game::game_keyboard(game_id);
        let _ = bot
            .edit_message_text(chat_id, msg_id, board_text)
            .parse_mode(ParseMode::Html)
            .reply_markup(kb)
            .await;

        // Update message_id in DB (it's the same message, but keep it consistent)
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
