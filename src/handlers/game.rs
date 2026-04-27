use teloxide::prelude::*;
use sqlx::SqlitePool;

use crate::handlers::{HandlerResult, MyDialogue};

pub async fn handle_scoreboard_command(
    bot: Bot,
    _dialogue: MyDialogue,
    msg: Message,
    _pool: SqlitePool,
) -> HandlerResult {
    bot.send_message(msg.chat.id, "Not implemented yet: handle_scoreboard_command").await?;
    Ok(())
}

pub async fn handle_enter_points(
    bot: Bot,
    _dialogue: MyDialogue,
    _game_id: i64,
    _player_id: i64,
    msg: Message,
    _pool: SqlitePool,
) -> HandlerResult {
    bot.send_message(msg.chat.id, "Not implemented yet: handle_enter_points").await?;
    Ok(())
}

pub async fn handle_game_callback(
    bot: Bot,
    _dialogue: MyDialogue,
    _game_id: i64,
    q: CallbackQuery,
    _pool: SqlitePool,
) -> HandlerResult {
    bot.answer_callback_query(&q.id).await?;
    let chat_id = match q.message.as_ref() {
        Some(m) => m.chat().id,
        None => return Ok(()),
    };
    bot.send_message(chat_id, "Not implemented yet: handle_game_callback").await?;
    Ok(())
}

pub async fn handle_select_player_callback(
    bot: Bot,
    _dialogue: MyDialogue,
    _game_id: i64,
    q: CallbackQuery,
    _pool: SqlitePool,
) -> HandlerResult {
    bot.answer_callback_query(&q.id).await?;
    let chat_id = match q.message.as_ref() {
        Some(m) => m.chat().id,
        None => return Ok(()),
    };
    bot.send_message(chat_id, "Not implemented yet: handle_select_player_callback").await?;
    Ok(())
}

pub async fn handle_edit_confirm_callback(
    bot: Bot,
    _dialogue: MyDialogue,
    _game_id: i64,
    q: CallbackQuery,
    _pool: SqlitePool,
) -> HandlerResult {
    bot.answer_callback_query(&q.id).await?;
    let chat_id = match q.message.as_ref() {
        Some(m) => m.chat().id,
        None => return Ok(()),
    };
    bot.send_message(chat_id, "Not implemented yet: handle_edit_confirm_callback").await?;
    Ok(())
}
