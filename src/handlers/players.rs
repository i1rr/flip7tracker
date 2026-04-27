use teloxide::prelude::*;
use teloxide::types::{ChatId, MessageId};
use sqlx::SqlitePool;

use crate::db::queries;
use crate::handlers::{HandlerResult, MyDialogue, State};
use crate::keyboards;

fn q_msg(q: &CallbackQuery) -> Option<(ChatId, MessageId)> {
    q.message.as_ref().and_then(|m| {
        m.regular_message().map(|r| (m.chat().id, r.id))
    })
}

pub async fn show_players_in_msg(
    bot: &Bot,
    chat_id: ChatId,
    msg_id: MessageId,
    pool: &SqlitePool,
    page: u32,
) -> HandlerResult {
    let players = queries::get_all_active_players(pool).await?;
    let text = if players.is_empty() {
        "No players yet.".to_string()
    } else {
        format!("Players ({}):\nTap \u{270f}\u{fe0f} to rename, \u{1f5d1} to delete.", players.len())
    };
    let kb = keyboards::players::players_list_keyboard(&players, page);
    let _ = bot
        .edit_message_text(chat_id, msg_id, text)
        .reply_markup(kb)
        .await;
    Ok(())
}

// ---------------------------------------------------------------------------
// State::PlayersManage callbacks
// ---------------------------------------------------------------------------
pub async fn handle_players_callback(
    bot: Bot,
    dialogue: MyDialogue,
    q: CallbackQuery,
    pool: SqlitePool,
    _page: u32,
) -> HandlerResult {
    bot.answer_callback_query(&q.id).await?;
    let (chat_id, msg_id) = match q_msg(&q) {
        Some(v) => v,
        None => return Ok(()),
    };
    let data = q.data.as_deref().unwrap_or("");
    let parts: Vec<&str> = data.splitn(3, ':').collect();

    match parts.as_slice() {
        ["mgmt", "back"] => {
            let _ = bot
                .edit_message_text(chat_id, msg_id, "Welcome to Flip7! Choose an option:")
                .reply_markup(keyboards::menu::main_menu_keyboard())
                .await;
            dialogue.update(State::MainMenu).await?;
        }

        ["mgmt", "noop"] => {}

        ["mgmt", "page", n_str] => {
            if let Ok(n) = n_str.parse::<u32>() {
                dialogue.update(State::PlayersManage { page: n }).await?;
                show_players_in_msg(&bot, chat_id, msg_id, &pool, n).await?;
            }
        }

        ["mgmt", "rename", id_str] => {
            if let Ok(player_id) = id_str.parse::<i64>() {
                let players = queries::get_all_active_players(&pool).await?;
                let name = players.iter().find(|p| p.id == player_id)
                    .map(|p| p.name.clone())
                    .unwrap_or_default();
                let _ = bot
                    .edit_message_text(chat_id, msg_id, format!("Enter new name for {}:", name))
                    .await;
                dialogue
                    .update(State::PlayersRenaming { player_id, setup_msg_id: msg_id.0 })
                    .await?;
            }
        }

        ["mgmt", "delete", id_str] => {
            if let Ok(player_id) = id_str.parse::<i64>() {
                let players = queries::get_all_active_players(&pool).await?;
                let name = players.iter().find(|p| p.id == player_id)
                    .map(|p| p.name.clone())
                    .unwrap_or_default();
                let kb = keyboards::players::delete_confirm_keyboard(player_id);
                let _ = bot
                    .edit_message_text(
                        chat_id,
                        msg_id,
                        format!("Delete {} and all their game history?\nThis cannot be undone.", name),
                    )
                    .reply_markup(kb)
                    .await;
                dialogue.update(State::PlayersDeleteConfirm { player_id }).await?;
            }
        }

        _ => {}
    }

    Ok(())
}

// ---------------------------------------------------------------------------
// State::PlayersDeleteConfirm callbacks
// ---------------------------------------------------------------------------
pub async fn handle_delete_confirm_callback(
    bot: Bot,
    dialogue: MyDialogue,
    q: CallbackQuery,
    pool: SqlitePool,
    player_id: i64,
) -> HandlerResult {
    bot.answer_callback_query(&q.id).await?;
    let (chat_id, msg_id) = match q_msg(&q) {
        Some(v) => v,
        None => return Ok(()),
    };
    let data = q.data.as_deref().unwrap_or("");
    let parts: Vec<&str> = data.splitn(3, ':').collect();

    match parts.as_slice() {
        ["mgmt", "delete_confirm", id_str] => {
            if let Ok(pid) = id_str.parse::<i64>() {
                if pid == player_id {
                    queries::hard_delete_player(&pool, player_id).await?;
                }
            }
            dialogue.update(State::PlayersManage { page: 0 }).await?;
            show_players_in_msg(&bot, chat_id, msg_id, &pool, 0).await?;
        }

        ["mgmt", "delete_cancel"] => {
            dialogue.update(State::PlayersManage { page: 0 }).await?;
            show_players_in_msg(&bot, chat_id, msg_id, &pool, 0).await?;
        }

        _ => {}
    }

    Ok(())
}

// ---------------------------------------------------------------------------
// State::PlayersRenaming message handler
// ---------------------------------------------------------------------------
pub async fn handle_renaming_message(
    bot: Bot,
    dialogue: MyDialogue,
    msg: Message,
    pool: SqlitePool,
    (player_id, setup_msg_id): (i64, i32),
) -> HandlerResult {
    let chat_id = msg.chat.id;
    let text = match msg.text() {
        Some(t) => t.trim().to_string(),
        None => {
            bot.send_message(chat_id, "Please enter a player name.").await?;
            return Ok(());
        }
    };

    if text.is_empty() || text.len() > 50 || text.chars().any(|c| c.is_control()) {
        bot.send_message(
            chat_id,
            "Invalid name. Use 1\u{2013}50 characters, no control characters.",
        )
        .await?;
        return Ok(());
    }

    let all_players = queries::get_all_active_players(&pool).await?;
    if all_players.iter().any(|p| p.name == text && p.id != player_id) {
        bot.send_message(chat_id, format!("\"{}\" is already taken.", text)).await?;
        return Ok(());
    }

    queries::rename_player(&pool, player_id, &text).await?;
    dialogue.update(State::PlayersManage { page: 0 }).await?;
    show_players_in_msg(&bot, chat_id, MessageId(setup_msg_id), &pool, 0).await?;
    Ok(())
}
