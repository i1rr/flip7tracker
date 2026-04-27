use teloxide::prelude::*;
use teloxide::types::ParseMode;
use sqlx::SqlitePool;

use crate::db::models::Player;
use crate::db::queries;
use crate::handlers::{HandlerResult, MyDialogue, State};
use crate::keyboards;
use crate::utils::scoreboard;

// ── helpers ──────────────────────────────────────────────────────────────────

fn setup_text(players: &[i64], all_players: &[Player]) -> String {
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

async fn send_setup(
    bot: &Bot,
    chat_id: ChatId,
    players: &[i64],
    all_players: &[Player],
) -> HandlerResult {
    bot.send_message(chat_id, setup_text(players, all_players))
        .reply_markup(keyboards::new_game::setup_keyboard(players, all_players))
        .await?;
    Ok(())
}

async fn show_known_players_page(
    bot: &Bot,
    chat_id: ChatId,
    added_ids: &[i64],
    all_players: &[Player],
    page: u32,
) -> HandlerResult {
    let available: Vec<&Player> = all_players
        .iter()
        .filter(|p| !added_ids.contains(&p.id))
        .collect();
    let per_page: usize = 8;
    let total_pages = ((available.len() as f32) / per_page as f32).ceil() as u32;
    let total_pages = total_pages.max(1);
    let start = (page as usize) * per_page;
    let page_players: Vec<&Player> = available.into_iter().skip(start).take(per_page).collect();

    let kb = keyboards::new_game::known_players_keyboard(&page_players, page, total_pages);
    bot.send_message(
        chat_id,
        format!("Select a player (page {}/{}):", page + 1, total_pages),
    )
    .reply_markup(kb)
    .await?;
    Ok(())
}

// ── Step 4.2: typing name ─────────────────────────────────────────────────────

pub async fn handle_typing_name(
    bot: Bot,
    dialogue: MyDialogue,
    msg: Message,
    pool: SqlitePool,
    players: Vec<i64>,
) -> HandlerResult {
    let text = match msg.text() {
        Some(t) => t.trim().to_string(),
        None => {
            bot.send_message(msg.chat.id, "Please enter a player name.")
                .await?;
            return Ok(());
        }
    };

    if text.is_empty() || text.len() > 50 || text.chars().any(|c| c.is_control()) {
        bot.send_message(
            msg.chat.id,
            "Invalid name. Use 1-50 characters, no control characters.",
        )
        .await?;
        return Ok(());
    }

    let player = queries::create_or_get_player(&pool, &text).await?;

    if players.contains(&player.id) {
        bot.send_message(msg.chat.id, format!("{} is already added.", text))
            .await?;
        return Ok(());
    }

    let mut updated = players.clone();
    updated.push(player.id);

    dialogue
        .update(State::NewGameSetup {
            players: updated.clone(),
        })
        .await?;

    let all_players = queries::get_all_active_players(&pool).await?;
    send_setup(&bot, msg.chat.id, &updated, &all_players).await?;
    Ok(())
}

// ── Step 4.3/4.4/4.5: setup callback ─────────────────────────────────────────

pub async fn handle_setup_callback(
    bot: Bot,
    dialogue: MyDialogue,
    q: CallbackQuery,
    pool: SqlitePool,
    players: Vec<i64>,
) -> HandlerResult {
    bot.answer_callback_query(&q.id).await?;

    let data = q.data.as_deref().unwrap_or("");
    let chat_id = match q.message.as_ref() {
        Some(m) => m.chat().id,
        None => return Ok(()),
    };

    let parts: Vec<&str> = data.splitn(3, ':').collect();

    match parts.as_slice() {
        // ── add new player by typing ──────────────────────────────────────────
        ["setup", "add_new"] => {
            dialogue
                .update(State::NewGameTypingName {
                    players: players.clone(),
                })
                .await?;
            bot.send_message(chat_id, "Enter the new player's name:")
                .await?;
        }

        // ── add from known list ───────────────────────────────────────────────
        ["setup", "known"] => {
            let all_players = queries::get_all_active_players(&pool).await?;
            dialogue
                .update(State::NewGameKnownPlayers {
                    players: players.clone(),
                    page: 0,
                })
                .await?;
            show_known_players_page(&bot, chat_id, &players, &all_players, 0).await?;
        }

        // ── start game ────────────────────────────────────────────────────────
        ["setup", "start"] => {
            if players.len() < 2 {
                bot.send_message(chat_id, "Need at least 2 players to start.")
                    .await?;
                return Ok(());
            }

            let unfinished = queries::get_unfinished_games(&pool, chat_id.0).await?;
            if !unfinished.is_empty() {
                bot.send_message(
                    chat_id,
                    "You have an unfinished game. Load it via Load Game or end it first.",
                )
                .await?;
                return Ok(());
            }

            // Edit the setup message to "Starting game..." if we can get its id
            if let Some(msg) = q.message.as_ref() {
                if let Some(regular) = msg.regular_message() {
                    let _ = bot
                        .edit_message_text(chat_id, regular.id, "Starting game...")
                        .await;
                }
            }

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
            let kb = keyboards::game::game_keyboard(game.id);
            let sent = bot
                .send_message(chat_id, &scoreboard_text)
                .parse_mode(ParseMode::Html)
                .reply_markup(kb)
                .await?;

            queries::update_game_message_id(&pool, game.id, sent.id.0 as i64).await?;
            dialogue
                .update(State::GameActive { game_id: game.id })
                .await?;
        }

        // ── back to main menu ─────────────────────────────────────────────────
        ["setup", "back"] => {
            dialogue.update(State::MainMenu).await?;
            bot.send_message(chat_id, "Main menu:")
                .reply_markup(keyboards::menu::main_menu_keyboard())
                .await?;
        }

        // ── disabled start (not enough players) ───────────────────────────────
        ["setup", "disabled"] => {
            // Already answered above; nothing else to do
        }

        // ── remove player (show confirm) ──────────────────────────────────────
        ["player", "remove", player_id_str] => {
            if let Ok(player_id) = player_id_str.parse::<i64>() {
                let all_players = queries::get_all_active_players(&pool).await?;
                let name = all_players
                    .iter()
                    .find(|p| p.id == player_id)
                    .map(|p| p.name.clone())
                    .unwrap_or_default();
                let kb = keyboards::new_game::confirm_remove_keyboard(player_id);
                bot.send_message(chat_id, format!("Remove {}?", name))
                    .reply_markup(kb)
                    .await?;
            }
        }

        // ── confirm remove ────────────────────────────────────────────────────
        ["player", "confirm_remove", player_id_str] => {
            if let Ok(player_id) = player_id_str.parse::<i64>() {
                let updated: Vec<i64> =
                    players.into_iter().filter(|&id| id != player_id).collect();
                dialogue
                    .update(State::NewGameSetup {
                        players: updated.clone(),
                    })
                    .await?;
                let all_players = queries::get_all_active_players(&pool).await?;
                send_setup(&bot, chat_id, &updated, &all_players).await?;
            }
        }

        // ── keep (cancel remove) ──────────────────────────────────────────────
        ["player", "keep"] => {
            let all_players = queries::get_all_active_players(&pool).await?;
            send_setup(&bot, chat_id, &players, &all_players).await?;
        }

        _ => {}
    }

    Ok(())
}

// ── Step 4.3: known players callback ─────────────────────────────────────────

pub async fn handle_known_players_callback(
    bot: Bot,
    dialogue: MyDialogue,
    q: CallbackQuery,
    pool: SqlitePool,
    players: Vec<i64>,
    page: u32,
) -> HandlerResult {
    bot.answer_callback_query(&q.id).await?;

    let data = q.data.as_deref().unwrap_or("");
    let chat_id = match q.message.as_ref() {
        Some(m) => m.chat().id,
        None => return Ok(()),
    };

    let parts: Vec<&str> = data.splitn(3, ':').collect();

    match parts.as_slice() {
        ["known", "page", n_str] => {
            if let Ok(n) = n_str.parse::<u32>() {
                let all_players = queries::get_all_active_players(&pool).await?;
                dialogue
                    .update(State::NewGameKnownPlayers {
                        players: players.clone(),
                        page: n,
                    })
                    .await?;
                show_known_players_page(&bot, chat_id, &players, &all_players, n).await?;
            }
        }

        ["known", "add", pid_str] => {
            if let Ok(player_id) = pid_str.parse::<i64>() {
                let mut updated = players.clone();
                if !updated.contains(&player_id) {
                    updated.push(player_id);
                }
                dialogue
                    .update(State::NewGameSetup {
                        players: updated.clone(),
                    })
                    .await?;
                let all_players = queries::get_all_active_players(&pool).await?;
                send_setup(&bot, chat_id, &updated, &all_players).await?;
            }
        }

        ["known", "back"] => {
            dialogue
                .update(State::NewGameSetup {
                    players: players.clone(),
                })
                .await?;
            let all_players = queries::get_all_active_players(&pool).await?;
            send_setup(&bot, chat_id, &players, &all_players).await?;
        }

        ["known", "noop"] => {
            // page indicator button pressed; nothing to do
        }

        _ => {}
    }

    Ok(())
}
