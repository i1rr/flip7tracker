use teloxide::types::{InlineKeyboardButton, InlineKeyboardMarkup};
use crate::db::models::Player;

pub fn game_keyboard(game_id: i64) -> InlineKeyboardMarkup {
    InlineKeyboardMarkup::new(vec![
        vec![
            InlineKeyboardButton::callback("+ Add Score", format!("game:add_score:{}", game_id)),
            InlineKeyboardButton::callback("✏️ Edit Last", format!("game:edit:{}", game_id)),
        ],
        vec![InlineKeyboardButton::callback("🚩 End Game", format!("game:end:{}", game_id))],
    ])
}

pub fn win_keyboard() -> InlineKeyboardMarkup {
    InlineKeyboardMarkup::new(vec![
        vec![
            InlineKeyboardButton::callback("📊 View Stats", "game:stats"),
            InlineKeyboardButton::callback("🎮 New Game", "game:new"),
        ],
        vec![InlineKeyboardButton::callback("🏠 Main Menu", "game:home")],
    ])
}

pub fn player_select_keyboard(game_id: i64, players: &[Player]) -> InlineKeyboardMarkup {
    let mut rows: Vec<Vec<InlineKeyboardButton>> = players
        .iter()
        .map(|p| {
            vec![InlineKeyboardButton::callback(
                &p.name,
                format!("score:player:{}:{}", game_id, p.id),
            )]
        })
        .collect();
    rows.push(vec![InlineKeyboardButton::callback("← Cancel", "score:cancel")]);
    InlineKeyboardMarkup::new(rows)
}

pub fn edit_confirm_keyboard(game_id: i64) -> InlineKeyboardMarkup {
    InlineKeyboardMarkup::new(vec![vec![
        InlineKeyboardButton::callback("🗑 Delete Entry", format!("edit:confirm:{}", game_id)),
        InlineKeyboardButton::callback("← Cancel", format!("edit:cancel:{}", game_id)),
    ]])
}

pub fn end_confirm_keyboard(game_id: i64) -> InlineKeyboardMarkup {
    InlineKeyboardMarkup::new(vec![vec![
        InlineKeyboardButton::callback("Yes, End", format!("game:end_confirm:{}", game_id)),
        InlineKeyboardButton::callback("Cancel", format!("game:end_cancel:{}", game_id)),
    ]])
}
