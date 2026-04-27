use serde::Deserialize;

#[derive(Deserialize, Debug)]
pub struct Config {
    pub bot_token: String,
    #[serde(default = "default_database_url")]
    pub database_url: String,
    #[serde(default)]
    pub allowed_chat_ids: Vec<i64>,
    #[serde(default = "default_log_level")]
    pub log_level: String,
}

fn default_database_url() -> String {
    "sqlite:///data/flip7.db".into()
}

fn default_log_level() -> String {
    "info".into()
}
