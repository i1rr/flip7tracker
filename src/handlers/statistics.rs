use teloxide::prelude::*;
use teloxide::types::{InlineKeyboardButton, InlineKeyboardMarkup, ParseMode};
use sqlx::SqlitePool;
use crate::handlers::{HandlerResult, MyDialogue, State};
use crate::db::queries;
use crate::keyboards;

pub async fn show_stats(bot: &Bot, chat_id: ChatId, pool: &SqlitePool) -> HandlerResult {
    show_stats_page(bot, chat_id, pool, 0).await
}

async fn show_stats_page(bot: &Bot, chat_id: ChatId, pool: &SqlitePool, page: u32) -> HandlerResult {
    let all_stats = queries::get_all_stats(pool).await?;
    if all_stats.is_empty() {
        bot.send_message(chat_id, "No statistics yet. Finish some games first!")
            .reply_markup(InlineKeyboardMarkup::new(vec![vec![
                InlineKeyboardButton::callback("← Back", "stats:back"),
            ]]))
            .await?;
        return Ok(());
    }

    let per_page = 10usize;
    let total_pages = ((all_stats.len() as f32) / per_page as f32).ceil() as u32;
    let total_pages = total_pages.max(1);
    let start = (page as usize) * per_page;
    let page_stats: Vec<_> = all_stats.iter().skip(start).take(per_page).collect();

    let medals = ["🥇", "🥈", "🥉"];
    let header = "📊 Flip 7 — Hall of Fame\n\n";
    let col_header = format!("{:<3} {:<12} {:>3} {:>3} {:>5} {:>5} {:>7}\n", "#", "Player", "G", "W", "Win%", "Avg", "Rating");
    let separator = "━".repeat(col_header.chars().count().saturating_sub(1)) + "\n";

    let mut rows_text = String::new();
    for (i, stats) in page_stats.iter().enumerate() {
        let rank = start + i;
        let prefix = if rank < 3 { medals[rank] } else { "  " };
        let rank_num = format!("{}", rank + 1);
        let win_pct = format!("{:.0}%", stats.win_rate * 100.0);
        let avg = format!("{:.0}", stats.avg_score);
        let rating = format!("{:.0}", stats.rating);
        let name = if stats.player_name.len() > 12 { &stats.player_name[..12] } else { &stats.player_name };
        rows_text.push_str(&format!(
            "{} {:<2} {:<12} {:>3} {:>3} {:>5} {:>5} {:>7}\n",
            prefix, rank_num, name,
            stats.games, stats.wins, win_pct, avg, rating
        ));
    }

    let full_text = format!("{}{}{}{}", header, col_header, separator, rows_text);

    // Player buttons
    let mut btn_rows: Vec<Vec<InlineKeyboardButton>> = vec![];
    for chunk in page_stats.chunks(3) {
        btn_rows.push(chunk.iter().map(|s| {
            InlineKeyboardButton::callback(&s.player_name, format!("stats:player:{}", s.player_id))
        }).collect());
    }

    // Navigation
    let mut nav_row = vec![];
    if page > 0 {
        nav_row.push(InlineKeyboardButton::callback("← Prev", format!("stats:page:{}", page - 1)));
    }
    if page + 1 < total_pages {
        nav_row.push(InlineKeyboardButton::callback("Next →", format!("stats:page:{}", page + 1)));
    }
    if !nav_row.is_empty() { btn_rows.push(nav_row); }
    btn_rows.push(vec![InlineKeyboardButton::callback("← Back", "stats:back")]);

    bot.send_message(chat_id, format!("<pre>{}</pre>", full_text))
        .parse_mode(ParseMode::Html)
        .reply_markup(InlineKeyboardMarkup::new(btn_rows))
        .await?;
    Ok(())
}

pub async fn handle_stats_callback(
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
        ["stats", "player", pid_str] => {
            if let Ok(player_id) = pid_str.parse::<i64>() {
                dialogue.update(State::StatsPlayerDetail { player_id }).await?;
                show_player_detail(&bot, chat_id, &pool, player_id).await?;
            }
        }
        ["stats", "page", n_str] => {
            if let Ok(n) = n_str.parse::<u32>() {
                show_stats_page(&bot, chat_id, &pool, n).await?;
            }
        }
        ["stats", "back"] => {
            dialogue.update(State::MainMenu).await?;
            bot.send_message(chat_id, "Main menu:")
                .reply_markup(keyboards::menu::main_menu_keyboard())
                .await?;
        }
        _ => {}
    }
    Ok(())
}

pub async fn handle_player_detail_callback(
    bot: Bot,
    dialogue: MyDialogue,
    q: CallbackQuery,
    pool: SqlitePool,
    _player_id: i64,
) -> HandlerResult {
    bot.answer_callback_query(&q.id).await?;
    let data = q.data.as_deref().unwrap_or("");
    let chat_id = match q.message.as_ref() {
        Some(m) => m.chat().id,
        None => return Ok(()),
    };
    if data == "stats:back_to_list" {
        dialogue.update(State::StatsView).await?;
        show_stats(&bot, chat_id, &pool).await?;
    }
    Ok(())
}

async fn show_player_detail(bot: &Bot, chat_id: ChatId, pool: &SqlitePool, player_id: i64) -> HandlerResult {
    let stats = queries::get_player_stats(pool, player_id).await?;
    let text = format!(
        "👤 {}\n\nGames played:   {}\nWins:            {}  ({:.0}%)\nLosses:          {}\nHighest score:  {} pts\nAvg per game:   {:.0} pts\nTotal pts ever: {} pts\nRating:         {:.0}",
        stats.player_name, stats.games, stats.wins, stats.win_rate * 100.0,
        stats.losses, stats.highest_score, stats.avg_score, stats.total_points, stats.rating
    );
    let kb = InlineKeyboardMarkup::new(vec![vec![
        InlineKeyboardButton::callback("← Back to Stats", "stats:back_to_list"),
    ]]);
    bot.send_message(chat_id, text)
        .reply_markup(kb)
        .await?;
    Ok(())
}
