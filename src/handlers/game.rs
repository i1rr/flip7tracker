use teloxide::prelude::*;
use teloxide::types::{ChatId, MessageId, ParseMode};
use sqlx::SqlitePool;

use crate::db::{models::Game, queries};
use crate::handlers::{HandlerResult, MyDialogue, State};
use crate::keyboards;
use crate::utils::scoreboard;

// ---------------------------------------------------------------------------
// Helper: edit the pinned scoreboard message in-place, or send a new one.
// ---------------------------------------------------------------------------
async fn update_scoreboard_message(
    bot: &Bot,
    pool: &SqlitePool,
    game: &Game,
    text: &str,
    kb: teloxide::types::InlineKeyboardMarkup,
) {
    if let Some(msg_id) = game.message_id {
        let result = bot
            .edit_message_text(ChatId(game.chat_id), MessageId(msg_id as i32), text)
            .parse_mode(ParseMode::Html)
            .reply_markup(kb.clone())
            .await;
        if result.is_err() {
            if let Ok(sent) = bot
                .send_message(ChatId(game.chat_id), text)
                .parse_mode(ParseMode::Html)
                .reply_markup(kb)
                .await
            {
                let _ =
                    queries::update_game_message_id(pool, game.id, sent.id.0 as i64).await;
            }
        }
    } else {
        if let Ok(sent) = bot
            .send_message(ChatId(game.chat_id), text)
            .parse_mode(ParseMode::Html)
            .reply_markup(kb)
            .await
        {
            let _ = queries::update_game_message_id(pool, game.id, sent.id.0 as i64).await;
        }
    }
}

// ---------------------------------------------------------------------------
// Build the win-screen text.
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
// /scoreboard command handler
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
// State::GameActive callback handler
// ---------------------------------------------------------------------------
pub async fn handle_game_callback(
    bot: Bot,
    dialogue: MyDialogue,
    q: CallbackQuery,
    pool: SqlitePool,
    game_id: i64,
) -> HandlerResult {
    bot.answer_callback_query(&q.id).await?;
    let chat_id = match q.message.as_ref() {
        Some(m) => m.chat().id,
        None => return Ok(()),
    };
    let data = q.data.as_deref().unwrap_or("");

    // "game:add_score:{game_id}"
    if data == format!("game:add_score:{}", game_id) {
        let players = queries::get_game_players(&pool, game_id).await?;
        let kb = keyboards::game::player_select_keyboard(game_id, &players);
        bot.send_message(chat_id, "Select a player to add score for:")
            .reply_markup(kb)
            .await?;
        dialogue
            .update(State::GameAddScoreSelectPlayer { game_id })
            .await?;
        return Ok(());
    }

    // "game:edit:{game_id}"
    if data == format!("game:edit:{}", game_id) {
        // Fetch the last score entry to show it (peek, not delete yet)
        let entry_opt: Option<crate::db::models::ScoreEntry> = sqlx::query_as(
            "SELECT id, game_id, player_id, points, created_at \
             FROM score_entries WHERE game_id = ? ORDER BY id DESC LIMIT 1",
        )
        .bind(game_id)
        .fetch_optional(&pool)
        .await?;

        match entry_opt {
            None => {
                bot.send_message(chat_id, "No score entries to edit.").await?;
            }
            Some(entry) => {
                // Resolve player name
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
                bot.send_message(chat_id, text).reply_markup(kb).await?;
                dialogue.update(State::GameEditConfirm { game_id }).await?;
            }
        }
        return Ok(());
    }

    // "game:end:{game_id}"
    if data == format!("game:end:{}", game_id) {
        let kb = keyboards::game::end_confirm_keyboard(game_id);
        bot.send_message(
            chat_id,
            "End game without declaring a winner?",
        )
        .reply_markup(kb)
        .await?;
        return Ok(());
    }

    // "game:end_confirm:{game_id}"
    if data == format!("game:end_confirm:{}", game_id) {
        queries::finish_game(&pool, game_id, None).await?;
        dialogue.update(State::MainMenu).await?;
        bot.send_message(chat_id, "Game over — no winner declared.")
            .reply_markup(keyboards::menu::main_menu_keyboard())
            .await?;
        return Ok(());
    }

    // "game:end_cancel:{game_id}"
    if data == format!("game:end_cancel:{}", game_id) {
        // Stay in GameActive; callback already answered above.
        return Ok(());
    }

    // "game:new"
    if data == "game:new" {
        dialogue.update(State::MainMenu).await?;
        bot.send_message(chat_id, "Welcome to Flip7! Choose an option:")
            .reply_markup(keyboards::menu::main_menu_keyboard())
            .await?;
        return Ok(());
    }

    // "game:stats"
    if data == "game:stats" {
        dialogue.update(State::StatsView).await?;
        crate::handlers::statistics::show_stats(&bot, chat_id, &pool).await?;
        return Ok(());
    }

    // "game:home"
    if data == "game:home" {
        dialogue.update(State::MainMenu).await?;
        bot.send_message(chat_id, "Welcome to Flip7! Choose an option:")
            .reply_markup(keyboards::menu::main_menu_keyboard())
            .await?;
        return Ok(());
    }

    Ok(())
}

// ---------------------------------------------------------------------------
// State::GameAddScoreSelectPlayer callback handler
// ---------------------------------------------------------------------------
pub async fn handle_select_player_callback(
    bot: Bot,
    dialogue: MyDialogue,
    q: CallbackQuery,
    pool: SqlitePool,
    game_id: i64,
) -> HandlerResult {
    bot.answer_callback_query(&q.id).await?;
    let chat_id = match q.message.as_ref() {
        Some(m) => m.chat().id,
        None => return Ok(()),
    };
    let data = q.data.as_deref().unwrap_or("");

    // "score:cancel"
    if data == "score:cancel" {
        dialogue.update(State::GameActive { game_id }).await?;
        bot.send_message(chat_id, "Cancelled.").await?;
        return Ok(());
    }

    // "score:player:{game_id}:{player_id}"
    let prefix = format!("score:player:{}:", game_id);
    if let Some(rest) = data.strip_prefix(&prefix) {
        if let Ok(player_id) = rest.parse::<i64>() {
            let players = queries::get_game_players(&pool, game_id).await?;
            let player_name = players
                .iter()
                .find(|p| p.id == player_id)
                .map(|p| p.name.clone())
                .unwrap_or_else(|| "Unknown".to_string());
            bot.send_message(chat_id, format!("Enter score for {}:", player_name))
                .await?;
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

    // Parse
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

    // Add score entry
    queries::add_score_entry(&pool, game_id, player_id, pts).await?;

    // Fetch updated scores
    let scores = queries::get_game_scores(&pool, game_id).await?;
    let players = queries::get_game_players(&pool, game_id).await?;

    // Resolve player name for confirmation message
    let player_name = players
        .iter()
        .find(|p| p.id == player_id)
        .map(|p| p.name.clone())
        .unwrap_or_else(|| "Unknown".to_string());

    bot.send_message(
        chat_id,
        format!("✅ Added {} pts to {}", pts, player_name),
    )
    .await?;

    // Check for winner (>= 200 pts)
    let winner = {
        let mut winner_opt: Option<(i64, i64)> = None; // (player_id, score)
        for player in &players {
            let score = *scores.get(&player.id).unwrap_or(&0);
            if score >= 200 {
                match winner_opt {
                    None => winner_opt = Some((player.id, score)),
                    Some((_, prev_score)) => {
                        if score > prev_score {
                            winner_opt = Some((player.id, score));
                        }
                        // Tie: keep first inserted (lowest player_id wins by insertion order)
                    }
                }
            }
        }
        winner_opt
    };

    if let Some((win_player_id, win_score)) = winner {
        // Finish game with winner
        queries::finish_game(&pool, game_id, Some(win_player_id)).await?;

        let win_name = players
            .iter()
            .find(|p| p.id == win_player_id)
            .map(|p| p.name.clone())
            .unwrap_or_else(|| "Unknown".to_string());

        // Detect tie: any other player also >= 200
        let tie = players.iter().any(|p| {
            p.id != win_player_id && *scores.get(&p.id).unwrap_or(&0) >= 200
        });

        let win_text = build_win_text(&win_name, win_score, &scores, &players, tie);
        let kb = keyboards::game::win_keyboard();

        bot.send_message(chat_id, &win_text)
            .parse_mode(ParseMode::Html)
            .reply_markup(kb)
            .await?;

        dialogue.update(State::MainMenu).await?;
    } else {
        // Game continues: update scoreboard in-place
        let game_vec = queries::get_unfinished_games(&pool, chat_id.0).await?;
        let game_opt = game_vec.into_iter().find(|g| g.id == game_id);
        if let Some(game) = game_opt {
            let board_text = scoreboard::render_scoreboard(&scores, &players, game_id);
            let kb = keyboards::game::game_keyboard(game_id);
            update_scoreboard_message(&bot, &pool, &game, &board_text, kb).await;
        }
        dialogue.update(State::GameActive { game_id }).await?;
    }

    Ok(())
}

// ---------------------------------------------------------------------------
// State::GameEditConfirm callback handler
// ---------------------------------------------------------------------------
pub async fn handle_edit_confirm_callback(
    bot: Bot,
    dialogue: MyDialogue,
    q: CallbackQuery,
    pool: SqlitePool,
    game_id: i64,
) -> HandlerResult {
    bot.answer_callback_query(&q.id).await?;
    let chat_id = match q.message.as_ref() {
        Some(m) => m.chat().id,
        None => return Ok(()),
    };
    let data = q.data.as_deref().unwrap_or("");

    // "edit:confirm:{game_id}"
    if data == format!("edit:confirm:{}", game_id) {
        queries::delete_last_score_entry(&pool, game_id).await?;

        // Re-fetch and update scoreboard
        let players = queries::get_game_players(&pool, game_id).await?;
        let scores = queries::get_game_scores(&pool, game_id).await?;
        let game_vec = queries::get_unfinished_games(&pool, chat_id.0).await?;
        let game_opt = game_vec.into_iter().find(|g| g.id == game_id);
        if let Some(game) = game_opt {
            let board_text = scoreboard::render_scoreboard(&scores, &players, game_id);
            let kb = keyboards::game::game_keyboard(game_id);
            update_scoreboard_message(&bot, &pool, &game, &board_text, kb).await;
        }

        bot.send_message(chat_id, "Last entry deleted.").await?;
        dialogue.update(State::GameActive { game_id }).await?;
        return Ok(());
    }

    // "edit:cancel:{game_id}"
    if data == format!("edit:cancel:{}", game_id) {
        dialogue.update(State::GameActive { game_id }).await?;
        return Ok(());
    }

    Ok(())
}
