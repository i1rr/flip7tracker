use teloxide::prelude::*;
use teloxide::types::{MessageId, ParseMode};
use sqlx::SqlitePool;

use crate::db::models::Player;
use crate::db::queries;
use crate::handlers::{HandlerResult, MyDialogue, State};
use crate::keyboards;
use crate::utils::scoreboard;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

fn q_msg(q: &CallbackQuery) -> Option<(teloxide::types::ChatId, MessageId)> {
    q.message.as_ref().and_then(|m| {
        m.regular_message().map(|r| (m.chat().id, r.id))
    })
}

pub fn setup_text(players: &[i64], all_players: &[Player]) -> String {
    let names: Vec<&str> = players
        .iter()
        .filter_map(|id| all_players.iter().find(|p| p.id == *id))
        .map(|p| p.name.as_str())
        .collect();
    if names.is_empty() {
        "New game setup:\nPlayers added: (none)".to_string()
    } else {
        format!("New game setup:\nPlayers added: {}", names.join(", "))
    }
}

/// Edit a specific message to the setup screen.
async fn edit_to_setup(
    bot: &Bot,
    chat_id: teloxide::types::ChatId,
    msg_id: MessageId,
    players: &[i64],
    all_players: &[Player],
) -> HandlerResult {
    let _ = bot
        .edit_message_text(chat_id, msg_id, setup_text(players, all_players))
        .reply_markup(keyboards::new_game::setup_keyboard(players, all_players))
        .await;
    Ok(())
}

// ---------------------------------------------------------------------------
// Typing name handler (State::NewGameTypingName)
// ---------------------------------------------------------------------------
pub async fn handle_typing_name(
    bot: Bot,
    dialogue: MyDialogue,
    msg: Message,
    pool: SqlitePool,
    (players, setup_msg_id): (Vec<i64>, i32),
) -> HandlerResult {
    // Remove the user's typed name — the setup message above will be edited
    // in place to reflect the new roster, so the raw input shouldn't linger.
    let _ = bot.delete_message(msg.chat.id, msg.id).await;

    let text = match msg.text() {
        Some(t) => t.trim().to_string(),
        None => {
            bot.send_message(msg.chat.id, "Please enter a player name.").await?;
            return Ok(());
        }
    };

    if text.is_empty() || text.len() > 50 || text.chars().any(|c| c.is_control()) {
        bot.send_message(
            msg.chat.id,
            "Invalid name. Use 1–50 characters, no control characters.",
        )
        .await?;
        return Ok(());
    }

    let player = queries::create_or_get_player(&pool, &text).await?;

    if players.contains(&player.id) {
        bot.send_message(msg.chat.id, format!("{} is already added.", text)).await?;
        return Ok(());
    }

    let mut updated = players.clone();
    updated.push(player.id);

    let all_players = queries::get_all_active_players(&pool).await?;
    // Edit the original setup message back to show the updated setup
    edit_to_setup(
        &bot,
        msg.chat.id,
        MessageId(setup_msg_id),
        &updated,
        &all_players,
    )
    .await?;

    dialogue
        .update(State::NewGameSetup { players: updated })
        .await?;
    Ok(())
}

// ---------------------------------------------------------------------------
// Setup callbacks (State::NewGameSetup)
// ---------------------------------------------------------------------------
pub async fn handle_setup_callback(
    bot: Bot,
    dialogue: MyDialogue,
    q: CallbackQuery,
    pool: SqlitePool,
    players: Vec<i64>,
) -> HandlerResult {
    log::info!("setup_callback players={:?}: {:?}", players, q.data);

    let (chat_id, msg_id) = match q_msg(&q) {
        Some(v) => v,
        None => {
            bot.answer_callback_query(&q.id).await?;
            return Ok(());
        }
    };

    let data = q.data.as_deref().unwrap_or("");
    let parts: Vec<&str> = data.splitn(3, ':').collect();

    match parts.as_slice() {
        // ── Add new player by typing ──────────────────────────────────────────
        ["setup", "add_new"] => {
            bot.answer_callback_query(&q.id).await?;
            let _ = bot
                .edit_message_text(chat_id, msg_id, "Enter the new player's name:")
                .await;
            dialogue
                .update(State::NewGameTypingName {
                    players: players.clone(),
                    setup_msg_id: msg_id.0,
                })
                .await?;
        }

        // ── Add from known list ───────────────────────────────────────────────
        ["setup", "known"] => {
            let all_players = queries::get_all_active_players(&pool).await?;
            let available: Vec<&Player> = all_players
                .iter()
                .filter(|p| !players.contains(&p.id))
                .collect();
            if available.is_empty() {
                bot.answer_callback_query(&q.id)
                    .text("No other players to add.")
                    .show_alert(false)
                    .await?;
                return Ok(());
            }
            bot.answer_callback_query(&q.id).await?;
            show_known_players_in_msg(
                &bot, chat_id, msg_id, &players, &all_players, 0,
            )
            .await?;
            dialogue
                .update(State::NewGameKnownPlayers { players: players.clone(), page: 0 })
                .await?;
        }

        // ── Start game ────────────────────────────────────────────────────────
        ["setup", "start"] => {
            if players.len() < 2 {
                bot.answer_callback_query(&q.id)
                    .text("Need at least 2 players to start.")
                    .show_alert(true)
                    .await?;
                return Ok(());
            }

            let unfinished = queries::get_unfinished_games(&pool, chat_id.0).await?;
            if !unfinished.is_empty() {
                bot.answer_callback_query(&q.id)
                    .text("You have an unfinished game. Load it or end it first.")
                    .show_alert(true)
                    .await?;
                return Ok(());
            }

            bot.answer_callback_query(&q.id).await?;

            // Delete the setup message
            let _ = bot.delete_message(chat_id, msg_id).await;

            let game = queries::create_game(&pool, chat_id.0).await?;
            for &pid in &players {
                queries::add_player_to_game(&pool, game.id, pid).await?;
            }

            let all_players = queries::get_all_active_players(&pool).await?;
            let game_players: Vec<Player> = all_players
                .into_iter()
                .filter(|p| players.contains(&p.id))
                .collect();

            let scores = std::collections::HashMap::new();
            let scoreboard_text =
                scoreboard::render_scoreboard(&scores, &game_players, game.id);
            let kb = keyboards::game::game_keyboard(game.id, &game_players);
            let sent = bot
                .send_message(chat_id, &scoreboard_text)
                .parse_mode(ParseMode::Html)
                .reply_markup(kb)
                .await?;

            queries::update_game_message_id(&pool, game.id, sent.id.0 as i64).await?;
            dialogue.update(State::GameActive { game_id: game.id }).await?;
        }

        // ── Back to main menu ─────────────────────────────────────────────────
        ["setup", "back"] => {
            bot.answer_callback_query(&q.id).await?;
            let _ = bot
                .edit_message_text(chat_id, msg_id, "Welcome to Flip7! Choose an option:")
                .reply_markup(keyboards::menu::main_menu_keyboard())
                .await;
            dialogue.update(State::MainMenu).await?;
        }

        // ── Disabled start (< 2 players) ──────────────────────────────────────
        ["setup", "disabled"] => {
            bot.answer_callback_query(&q.id)
                .text("Add at least 2 players first.")
                .show_alert(false)
                .await?;
        }

        // ── Remove player (instant, no confirmation) ──────────────────────────
        ["player", "remove", player_id_str] => {
            bot.answer_callback_query(&q.id).await?;
            if let Ok(player_id) = player_id_str.parse::<i64>() {
                let updated: Vec<i64> =
                    players.into_iter().filter(|&id| id != player_id).collect();
                dialogue
                    .update(State::NewGameSetup { players: updated.clone() })
                    .await?;
                let all_players = queries::get_all_active_players(&pool).await?;
                edit_to_setup(&bot, chat_id, msg_id, &updated, &all_players).await?;
            }
        }

        _ => {
            bot.answer_callback_query(&q.id).await?;
        }
    }

    Ok(())
}

// ---------------------------------------------------------------------------
// Known players callbacks (State::NewGameKnownPlayers)
// ---------------------------------------------------------------------------

fn show_known_players_in_msg<'a>(
    bot: &'a Bot,
    chat_id: teloxide::types::ChatId,
    msg_id: MessageId,
    added_ids: &'a [i64],
    all_players: &'a [Player],
    page: u32,
) -> std::pin::Pin<Box<dyn std::future::Future<Output = HandlerResult> + Send + 'a>> {
    Box::pin(async move {
        let available: Vec<&Player> = all_players
            .iter()
            .filter(|p| !added_ids.contains(&p.id))
            .collect();
        let per_page: usize = 8;
        let total_pages = ((available.len() as f32) / per_page as f32).ceil() as u32;
        let total_pages = total_pages.max(1);
        let page = page.min(total_pages - 1);
        let start = (page as usize) * per_page;
        let page_players: Vec<&Player> =
            available.into_iter().skip(start).take(per_page).collect();

        let added_count = added_ids.len();
        let header = if added_count == 0 {
            format!("Select a player (page {}/{}):", page + 1, total_pages)
        } else {
            format!(
                "Select a player — {} added so far (page {}/{}):",
                added_count,
                page + 1,
                total_pages
            )
        };
        let kb = keyboards::new_game::known_players_keyboard(
            &page_players,
            page,
            total_pages,
            added_count,
        );
        let _ = bot
            .edit_message_text(chat_id, msg_id, header)
            .reply_markup(kb)
            .await;
        Ok(())
    })
}

pub async fn handle_known_players_callback(
    bot: Bot,
    dialogue: MyDialogue,
    q: CallbackQuery,
    pool: SqlitePool,
    (players, page): (Vec<i64>, u32),
) -> HandlerResult {
    bot.answer_callback_query(&q.id).await?;

    let (chat_id, msg_id) = match q_msg(&q) {
        Some(v) => v,
        None => return Ok(()),
    };

    let data = q.data.as_deref().unwrap_or("");
    let parts: Vec<&str> = data.splitn(3, ':').collect();

    match parts.as_slice() {
        ["known", "page", n_str] => {
            if let Ok(n) = n_str.parse::<u32>() {
                let all_players = queries::get_all_active_players(&pool).await?;
                show_known_players_in_msg(&bot, chat_id, msg_id, &players, &all_players, n).await?;
                dialogue
                    .update(State::NewGameKnownPlayers { players, page: n })
                    .await?;
            }
        }

        ["known", "add", pid_str] => {
            if let Ok(player_id) = pid_str.parse::<i64>() {
                let mut updated = players.clone();
                if !updated.contains(&player_id) {
                    updated.push(player_id);
                }
                let all_players = queries::get_all_active_players(&pool).await?;
                let remaining = all_players.iter().filter(|p| !updated.contains(&p.id)).count();
                if remaining == 0 {
                    // All known players added — return to setup
                    dialogue
                        .update(State::NewGameSetup { players: updated.clone() })
                        .await?;
                    edit_to_setup(&bot, chat_id, msg_id, &updated, &all_players).await?;
                } else {
                    // Stay on known players screen so user can keep adding
                    dialogue
                        .update(State::NewGameKnownPlayers { players: updated.clone(), page })
                        .await?;
                    show_known_players_in_msg(
                        &bot, chat_id, msg_id, &updated, &all_players, page,
                    )
                    .await?;
                }
            }
        }

        ["known", "start"] => {
            if players.len() < 2 {
                bot.answer_callback_query(&q.id)
                    .text("Need at least 2 players to start.")
                    .show_alert(true)
                    .await?;
                return Ok(());
            }

            let unfinished = queries::get_unfinished_games(&pool, chat_id.0).await?;
            if !unfinished.is_empty() {
                bot.answer_callback_query(&q.id)
                    .text("You have an unfinished game. Load it or end it first.")
                    .show_alert(true)
                    .await?;
                return Ok(());
            }

            let _ = bot.delete_message(chat_id, msg_id).await;

            let game = queries::create_game(&pool, chat_id.0).await?;
            for &pid in &players {
                queries::add_player_to_game(&pool, game.id, pid).await?;
            }

            let all_players = queries::get_all_active_players(&pool).await?;
            let game_players: Vec<Player> = all_players
                .into_iter()
                .filter(|p| players.contains(&p.id))
                .collect();

            let scores = std::collections::HashMap::new();
            let scoreboard_text =
                scoreboard::render_scoreboard(&scores, &game_players, game.id);
            let kb = keyboards::game::game_keyboard(game.id, &game_players);
            let sent = bot
                .send_message(chat_id, &scoreboard_text)
                .parse_mode(ParseMode::Html)
                .reply_markup(kb)
                .await?;

            queries::update_game_message_id(&pool, game.id, sent.id.0 as i64).await?;
            dialogue.update(State::GameActive { game_id: game.id }).await?;
        }

        ["known", "back"] => {
            dialogue
                .update(State::NewGameSetup { players: players.clone() })
                .await?;
            let all_players = queries::get_all_active_players(&pool).await?;
            edit_to_setup(&bot, chat_id, msg_id, &players, &all_players).await?;
        }

        ["known", "noop"] => {}

        _ => {}
    }

    Ok(())
}
