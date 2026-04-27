use teloxide::types::{InlineKeyboardButton, InlineKeyboardMarkup};
use crate::db::models::Player;

pub fn players_list_keyboard(players: &[Player], page: u32) -> InlineKeyboardMarkup {
    const PER_PAGE: usize = 8;
    let total_pages = ((players.len() as f32) / PER_PAGE as f32).ceil() as u32;
    let total_pages = total_pages.max(1);
    let start = (page as usize) * PER_PAGE;
    let page_players: Vec<&Player> = players.iter().skip(start).take(PER_PAGE).collect();

    let mut rows: Vec<Vec<InlineKeyboardButton>> = vec![];
    for p in &page_players {
        rows.push(vec![
            InlineKeyboardButton::callback(
                format!("{} \u{270f}\u{fe0f}", p.name),
                format!("mgmt:rename:{}", p.id),
            ),
            InlineKeyboardButton::callback("\u{1f5d1}", format!("mgmt:delete:{}", p.id)),
        ]);
    }

    if total_pages > 1 {
        let mut nav = vec![];
        if page > 0 {
            nav.push(InlineKeyboardButton::callback("\u{2190} Prev", format!("mgmt:page:{}", page - 1)));
        }
        nav.push(InlineKeyboardButton::callback(
            format!("{}/{}", page + 1, total_pages),
            "mgmt:noop",
        ));
        if page + 1 < total_pages {
            nav.push(InlineKeyboardButton::callback("Next \u{2192}", format!("mgmt:page:{}", page + 1)));
        }
        rows.push(nav);
    }

    rows.push(vec![InlineKeyboardButton::callback("\u{2190} Back", "mgmt:back")]);
    InlineKeyboardMarkup::new(rows)
}

pub fn delete_confirm_keyboard(player_id: i64) -> InlineKeyboardMarkup {
    InlineKeyboardMarkup::new(vec![vec![
        InlineKeyboardButton::callback(
            "Yes, Delete",
            format!("mgmt:delete_confirm:{}", player_id),
        ),
        InlineKeyboardButton::callback("Cancel", "mgmt:delete_cancel"),
    ]])
}
