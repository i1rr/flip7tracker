use teloxide::types::{InlineKeyboardButton, InlineKeyboardMarkup};

pub fn main_menu_keyboard() -> InlineKeyboardMarkup {
    InlineKeyboardMarkup::new(vec![
        vec![InlineKeyboardButton::callback("New Game", "menu:new_game")],
        vec![InlineKeyboardButton::callback("Load Game", "menu:load_game")],
        vec![InlineKeyboardButton::callback("Statistics", "menu:stats")],
    ])
}
