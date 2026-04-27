use teloxide::types::{InlineKeyboardButton, InlineKeyboardMarkup};
use crate::db::models::Player;

pub fn setup_keyboard(added_ids: &[i64], all_players: &[Player]) -> InlineKeyboardMarkup {
    let mut rows: Vec<Vec<InlineKeyboardButton>> = vec![];

    // Buttons for added players (2 per row)
    for chunk in added_ids.chunks(2) {
        let mut row = vec![];
        for &pid in chunk {
            let name = all_players
                .iter()
                .find(|p| p.id == pid)
                .map(|p| p.name.as_str())
                .unwrap_or("?");
            row.push(InlineKeyboardButton::callback(
                format!("{} \u{2715}", name),
                format!("player:remove:{}", pid),
            ));
        }
        rows.push(row);
    }

    // Add player buttons
    rows.push(vec![
        InlineKeyboardButton::callback("+ New Player", "setup:add_new"),
        InlineKeyboardButton::callback("+ Known Player", "setup:known"),
    ]);

    // Back button
    rows.push(vec![InlineKeyboardButton::callback("\u{2190} Back", "setup:back")]);

    // Start button
    if added_ids.len() >= 2 {
        rows.push(vec![InlineKeyboardButton::callback(
            "\u{25b6} Start Game",
            "setup:start",
        )]);
    } else {
        rows.push(vec![InlineKeyboardButton::callback(
            "\u{25b6} Start Game (need 2+ players)",
            "setup:disabled",
        )]);
    }

    InlineKeyboardMarkup::new(rows)
}

pub fn known_players_keyboard(
    players: &[&Player],
    page: u32,
    total_pages: u32,
    added_count: usize,
) -> InlineKeyboardMarkup {
    let mut rows: Vec<Vec<InlineKeyboardButton>> = vec![];
    for p in players {
        rows.push(vec![InlineKeyboardButton::callback(
            p.name.clone(),
            format!("known:add:{}", p.id),
        )]);
    }

    // Navigation row
    let mut nav_row = vec![];
    if page > 0 {
        nav_row.push(InlineKeyboardButton::callback(
            "\u{2190} Prev",
            format!("known:page:{}", page - 1),
        ));
    }
    nav_row.push(InlineKeyboardButton::callback(
        format!("Page {}/{}", page + 1, total_pages),
        "known:noop",
    ));
    if page + 1 < total_pages {
        nav_row.push(InlineKeyboardButton::callback(
            "Next \u{2192}",
            format!("known:page:{}", page + 1),
        ));
    }
    rows.push(nav_row);
    rows.push(vec![InlineKeyboardButton::callback(
        "\u{2190} Back to Setup",
        "known:back",
    )]);
    if added_count >= 2 {
        rows.push(vec![InlineKeyboardButton::callback(
            "\u{25b6} Start Game",
            "known:start",
        )]);
    }

    InlineKeyboardMarkup::new(rows)
}
