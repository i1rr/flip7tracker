use std::collections::HashMap;
use crate::db::models::Player;

pub fn render_scoreboard(scores: &HashMap<i64, i64>, players: &[Player], game_id: i64) -> String {
    let medals = ["🥇", "🥈", "🥉"];
    let mut sorted: Vec<(&Player, i64)> = players
        .iter()
        .map(|p| (p, *scores.get(&p.id).unwrap_or(&0)))
        .collect();
    sorted.sort_by(|a, b| b.1.cmp(&a.1));

    let mut lines = vec![format!("🎮 Flip 7 — Game #{}", game_id), String::new()];
    for (i, (player, score)) in sorted.iter().enumerate() {
        let prefix = if i < 3 { medals[i] } else { "  " };
        let rank = if i < 3 {
            format!("{} {}.", prefix, i + 1)
        } else {
            format!("   {}.", i + 1)
        };
        lines.push(format!("{} {:.<15} {} pts", rank, player.name, score));
    }
    lines.push(String::new());
    lines.push("🏁 First to 200 wins!".to_string());
    lines.join("\n")
}
