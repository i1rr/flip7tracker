use teloxide::prelude::*;
use sqlx::SqlitePool;

use crate::handlers::{HandlerResult, MyDialogue, State};
use crate::keyboards;

pub async fn handle_start(bot: Bot, dialogue: MyDialogue, msg: Message) -> HandlerResult {
    dialogue.update(State::MainMenu).await?;
    bot.send_message(msg.chat.id, "Welcome to Flip7! Choose an option:")
        .reply_markup(keyboards::menu::main_menu_keyboard())
        .await?;
    Ok(())
}

pub async fn handle_cancel(bot: Bot, dialogue: MyDialogue, msg: Message) -> HandlerResult {
    dialogue.update(State::MainMenu).await?;
    bot.send_message(msg.chat.id, "Session reset. Use /menu to continue.")
        .reply_markup(keyboards::menu::main_menu_keyboard())
        .await?;
    Ok(())
}

pub async fn handle_help(bot: Bot, msg: Message) -> HandlerResult {
    let help_text = "Flip7 Bot - Commands:\n\
        /start or /menu - Main menu\n\
        /scoreboard - Re-send scoreboard to bottom of chat\n\
        /cancel - Cancel current action\n\
        /help - Show this message\n\n\
        The host controls the game. Add players, start, then enter scores after each hand.";
    bot.send_message(msg.chat.id, help_text).await?;
    Ok(())
}

pub async fn handle_unknown_message(_bot: Bot, _msg: Message) -> HandlerResult {
    // Silently ignore unknown messages in non-text-entry states
    Ok(())
}

pub async fn handle_main_menu_callback(
    bot: Bot,
    dialogue: MyDialogue,
    q: CallbackQuery,
    pool: SqlitePool,
) -> HandlerResult {
    bot.answer_callback_query(&q.id).await?;
    let data = q.data.as_deref().unwrap_or("");
    let chat_id = match q.message.as_ref() {
        Some(m) => m.chat().id,
        None => return Ok(()),
    };
    match data {
        "menu:new_game" => {
            dialogue.update(State::NewGameSetup { players: vec![] }).await?;
            bot.send_message(chat_id, "New game setup:\nPlayers added: (none)")
                .reply_markup(keyboards::new_game::setup_keyboard(&[], &[]))
                .await?;
        }
        "menu:load_game" => {
            dialogue.update(State::LoadGameList).await?;
            bot.send_message(chat_id, "Loading games...").await?;
            crate::handlers::load_game::show_load_game_list(&bot, chat_id, &pool).await?;
        }
        "menu:stats" => {
            dialogue.update(State::StatsView).await?;
            crate::handlers::statistics::show_stats(&bot, chat_id, &pool).await?;
        }
        _ => {}
    }
    Ok(())
}
