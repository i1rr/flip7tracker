use teloxide::types::{InlineKeyboardButton, InlineKeyboardMarkup};

pub fn game_keyboard(_game_id: i64) -> InlineKeyboardMarkup {
    // Stub: will be implemented in Section 5
    InlineKeyboardMarkup::new(vec![
        vec![InlineKeyboardButton::callback("Add Score", "game:add_score")],
        vec![InlineKeyboardButton::callback("End Game", "game:end")],
    ])
}
