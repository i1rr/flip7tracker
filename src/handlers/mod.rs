use teloxide::prelude::*;
use teloxide::dispatching::dialogue::{self, InMemStorage};
use teloxide::dispatching::UpdateHandler;
use dptree::case;

pub mod game;
pub mod load_game;
pub mod menu;
pub mod new_game;
pub mod statistics;

pub type MyDialogue = Dialogue<State, InMemStorage<State>>;
pub type HandlerResult = Result<(), Box<dyn std::error::Error + Send + Sync>>;

#[derive(Clone, Default, PartialEq, Debug, serde::Serialize, serde::Deserialize)]
pub enum State {
    #[default]
    MainMenu,
    NewGameSetup { players: Vec<i64> },
    NewGameTypingName { players: Vec<i64> },
    NewGameKnownPlayers { players: Vec<i64>, page: u32 },
    GameActive { game_id: i64 },
    GameAddScoreSelectPlayer { game_id: i64 },
    GameAddScoreEnterPoints { game_id: i64, player_id: i64 },
    GameEditConfirm { game_id: i64 },
    LoadGameList,
    StatsView,
    StatsPlayerDetail { player_id: i64 },
}

#[derive(teloxide::utils::command::BotCommands, Clone)]
#[command(rename_rule = "lowercase", description = "Flip7 Bot commands:")]
pub enum Command {
    #[command(description = "Start the bot / Main menu")]
    Start,
    #[command(description = "Main menu")]
    Menu,
    #[command(description = "Cancel current action")]
    Cancel,
    #[command(description = "Show help")]
    Help,
    #[command(description = "Re-send scoreboard")]
    Scoreboard,
}

pub fn build_handler() -> UpdateHandler<Box<dyn std::error::Error + Send + Sync + 'static>> {
    let command_handler = teloxide::filter_command::<Command, _>()
        .branch(case![Command::Start].endpoint(menu::handle_start))
        .branch(case![Command::Menu].endpoint(menu::handle_start))
        .branch(case![Command::Cancel].endpoint(menu::handle_cancel))
        .branch(case![Command::Help].endpoint(menu::handle_help))
        .branch(case![Command::Scoreboard].endpoint(game::handle_scoreboard_command));

    let message_handler = Update::filter_message()
        .branch(command_handler)
        .branch(case![State::NewGameTypingName { players }].endpoint(new_game::handle_typing_name))
        .branch(
            case![State::GameAddScoreEnterPoints { game_id, player_id }]
                .endpoint(game::handle_enter_points),
        )
        .branch(dptree::endpoint(menu::handle_unknown_message));

    let callback_handler = Update::filter_callback_query()
        .branch(case![State::MainMenu].endpoint(menu::handle_main_menu_callback))
        .branch(case![State::NewGameSetup { players }].endpoint(new_game::handle_setup_callback))
        .branch(
            case![State::NewGameKnownPlayers { players, page }]
                .endpoint(new_game::handle_known_players_callback),
        )
        .branch(case![State::GameActive { game_id }].endpoint(game::handle_game_callback))
        .branch(
            case![State::GameAddScoreSelectPlayer { game_id }]
                .endpoint(game::handle_select_player_callback),
        )
        .branch(
            case![State::GameEditConfirm { game_id }]
                .endpoint(game::handle_edit_confirm_callback),
        )
        .branch(case![State::LoadGameList].endpoint(load_game::handle_load_callback))
        .branch(case![State::StatsView].endpoint(statistics::handle_stats_callback))
        .branch(
            case![State::StatsPlayerDetail { player_id }]
                .endpoint(statistics::handle_player_detail_callback),
        );

    dialogue::enter::<Update, InMemStorage<State>, State, _>()
        .branch(message_handler)
        .branch(callback_handler)
}
