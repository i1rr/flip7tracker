use std::sync::Arc;
use env_logger::Env;
use teloxide::prelude::*;
use teloxide::dispatching::dialogue::InMemStorage;

mod config;
mod db;
mod handlers;
mod keyboards;
mod utils;

#[tokio::main(flavor = "multi_thread")]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    dotenvy::dotenv().ok();
    let config = envy::from_env::<config::Config>()?;
    env_logger::Builder::from_env(Env::default().default_filter_or(&config.log_level)).init();
    log::info!("Starting Flip7 bot...");

    let pool = db::create_pool(&config.database_url).await?;
    log::info!("Database pool initialized.");

    sqlx::migrate!("./migrations").run(&pool).await?;
    log::info!("Database migrations applied.");

    let bot = Bot::new(&config.bot_token);

    match bot.get_me().await {
        Ok(me) => log::info!("Bot started: @{}", me.username()),
        Err(e) => {
            log::error!("Failed to validate bot token: {}", e);
            std::process::exit(1);
        }
    }

    let storage = InMemStorage::<handlers::State>::new();
    let config_arc = Arc::new(config);

    let handler = handlers::build_handler();

    let mut sigterm = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
        .expect("Failed to install SIGTERM handler");

    let mut dispatcher = Dispatcher::builder(bot.clone(), handler)
        .dependencies(dptree::deps![pool.clone(), storage, config_arc.clone()])
        .build();

    let shutdown_token = dispatcher.shutdown_token();

    tokio::spawn(async move {
        sigterm.recv().await;
        log::info!("Received SIGTERM, shutting down...");
        shutdown_token.shutdown().unwrap().await;
    });

    log::info!("Dispatching...");
    dispatcher.dispatch().await;

    Ok(())
}
