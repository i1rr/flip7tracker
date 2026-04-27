use teloxide::prelude::*;
use teloxide::types::{InlineKeyboardButton, InlineKeyboardMarkup, ParseMode};
use sqlx::SqlitePool;
use crate::handlers::{HandlerResult, MyDialogue, State};
use crate::db::queries;
use crate::utils::scoreboard;
use crate::keyboards;

pub async fn show_load_game_list(bot: &Bot, chat_id: ChatId, pool: &SqlitePool) -> HandlerResult {
    let games = queries::get_unfinished_games(pool, chat_id.0).await?;
    if games.is_empty() {
        let kb = InlineKeyboardMarkup::new(vec![vec![
            InlineKeyboardButton::callback("← Back", "load:back"),
        ]]);
        bot.send_message(chat_id, "No unfinished games found.")
            .reply_markup(kb)
            .await?;
        return Ok(());
    }

    let mut rows: Vec<Vec<InlineKeyboardButton>> = vec![];
    for game in &games {
        let players = queries::get_game_players(pool, game.id).await?;
        let names: Vec<&str> = players.iter().map(|p| p.name.as_str()).collect();
        let date_str = game.started_at.format("%b %-d").to_string();
        let label = format!("Game #{} · {} · Started {}", game.id, names.join(", "), date_str);
        rows.push(vec![InlineKeyboardButton::callback(label, format!("game:load:{}", game.id))]);
    }
    rows.push(vec![InlineKeyboardButton::callback("← Back", "load:back")]);

    bot.send_message(chat_id, "Select a game to load:")
        .reply_markup(InlineKeyboardMarkup::new(rows))
        .await?;
    Ok(())
}

pub async fn handle_load_callback(
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
    let parts: Vec<&str> = data.split(':').collect();
    match parts.as_slice() {
        ["game", "load", gid_str] => {
            if let Ok(game_id) = gid_str.parse::<i64>() {
                let players = queries::get_game_players(&pool, game_id).await?;
                let scores = queries::get_game_scores(&pool, game_id).await?;
                let text = scoreboard::render_scoreboard(&scores, &players, game_id);
                let kb = keyboards::game::game_keyboard(game_id);
                let sent = bot.send_message(chat_id, &text)
                    .parse_mode(ParseMode::Html)
                    .reply_markup(kb)
                    .await?;
                queries::update_game_message_id(&pool, game_id, sent.id.0 as i64).await?;
                dialogue.update(State::GameActive { game_id }).await?;
            }
        }
        ["load", "back"] | ["menu", "back"] => {
            dialogue.update(State::MainMenu).await?;
            bot.send_message(chat_id, "Main menu:")
                .reply_markup(keyboards::menu::main_menu_keyboard())
                .await?;
        }
        _ => {}
    }
    Ok(())
}
