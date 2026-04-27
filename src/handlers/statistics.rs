use teloxide::prelude::*;
use teloxide::types::ChatId;
use sqlx::SqlitePool;

use crate::handlers::{HandlerResult, MyDialogue};

pub async fn handle_stats_callback(
    bot: Bot,
    _dialogue: MyDialogue,
    q: CallbackQuery,
    _pool: SqlitePool,
) -> HandlerResult {
    bot.answer_callback_query(&q.id).await?;
    let chat_id = match q.message.as_ref() {
        Some(m) => m.chat().id,
        None => return Ok(()),
    };
    bot.send_message(chat_id, "Not implemented yet: handle_stats_callback").await?;
    Ok(())
}

pub async fn handle_player_detail_callback(
    bot: Bot,
    _dialogue: MyDialogue,
    _player_id: i64,
    q: CallbackQuery,
    _pool: SqlitePool,
) -> HandlerResult {
    bot.answer_callback_query(&q.id).await?;
    let chat_id = match q.message.as_ref() {
        Some(m) => m.chat().id,
        None => return Ok(()),
    };
    bot.send_message(chat_id, "Not implemented yet: handle_player_detail_callback").await?;
    Ok(())
}

pub async fn show_stats(
    bot: &Bot,
    chat_id: ChatId,
    _pool: &SqlitePool,
) -> HandlerResult {
    bot.send_message(chat_id, "Not implemented yet: show_stats").await?;
    Ok(())
}
