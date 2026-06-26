#!/usr/bin/env bash
#
# register.sh — create (or re-create) the /flip7 slash command in Mattermost and
# capture its token into .env.
#
# This talks to the Mattermost server via the local-mode admin socket:
#     docker exec -i mattermost mmctl --local ...
# so no admin password is needed, but it MUST be run on the host that runs the
# Mattermost container. Never run this in CI.
#
# It reads MM_TEAM and INTEGRATION_BASE_URL from .env, registers the command at
#     ${INTEGRATION_BASE_URL%/}/slash/flip7
# with autocomplete enabled, parses the returned token with jq, and writes it
# back to .env as SLASH_TOKEN_FLIP7 (preserving the file's 600 permissions).
#
# Requirements: bash, docker, jq. The `mattermost` container must be running
# with local mode (ServiceSettings.EnableLocalMode) enabled.

set -euo pipefail

# Resolve paths relative to this script so it works from any CWD.
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd -P)"
ENV_FILE="${SCRIPT_DIR}/../.env"
MM_CONTAINER="${MM_CONTAINER:-mattermost}"

die() { printf 'register.sh: %s\n' "$*" >&2; exit 1; }

command -v docker >/dev/null 2>&1 || die "docker not found on PATH"
command -v jq >/dev/null 2>&1 || die "jq not found on PATH"
[ -f "$ENV_FILE" ] || die "missing $ENV_FILE — copy .env.example to .env and fill it in first"

# Source .env in a controlled way: only the values we need.
set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

: "${MM_TEAM:?MM_TEAM must be set in .env}"
: "${INTEGRATION_BASE_URL:?INTEGRATION_BASE_URL must be set in .env}"

# Trim any trailing slash from the base URL, then build the slash endpoint.
BASE_URL="${INTEGRATION_BASE_URL%/}"
SLASH_URL="${BASE_URL}/slash/flip7"

echo "Registering /flip7 -> ${SLASH_URL} (team: ${MM_TEAM}) via ${MM_CONTAINER} ..." >&2

# Create the command. --json so we can extract the token with jq.
# mmctl prints the created command (including its Token) as JSON.
CREATE_JSON="$(docker exec -i "$MM_CONTAINER" mmctl --local command create "$MM_TEAM" \
  --title "flip7" \
  --description "Flip 7 score tracker" \
  --trigger-word "flip7" \
  --url "$SLASH_URL" \
  --post \
  --autocomplete \
  --json)" || die "mmctl command create failed (is local mode enabled and the team correct?)"

TOKEN="$(printf '%s' "$CREATE_JSON" | jq -r '.token // empty')"
[ -n "$TOKEN" ] || die "could not parse a token from mmctl output:
$CREATE_JSON"

# Write/replace SLASH_TOKEN_FLIP7 in .env, preserving permissions (default 600).
TMP_FILE="$(mktemp)"
trap 'rm -f "$TMP_FILE"' EXIT

if grep -q '^SLASH_TOKEN_FLIP7=' "$ENV_FILE"; then
  awk -v tok="$TOKEN" '
    /^SLASH_TOKEN_FLIP7=/ { print "SLASH_TOKEN_FLIP7=" tok; next }
    { print }
  ' "$ENV_FILE" > "$TMP_FILE"
else
  cat "$ENV_FILE" > "$TMP_FILE"
  printf 'SLASH_TOKEN_FLIP7=%s\n' "$TOKEN" >> "$TMP_FILE"
fi

cat "$TMP_FILE" > "$ENV_FILE"
chmod 600 "$ENV_FILE"

echo "Wrote SLASH_TOKEN_FLIP7 to ${ENV_FILE} (chmod 600)." >&2
echo >&2
echo "Reminder: there is no /start command — the bot serves the menu via /flip7." >&2
echo "Restart the flip7bot container so it picks up the new token:" >&2
echo "    docker compose -f ${SCRIPT_DIR}/../docker-compose.yml up -d --force-recreate flip7bot" >&2
