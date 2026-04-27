#!/bin/sh
set -e
mkdir -p /data
chown -R bot:bot /data
exec su-exec bot ./flip7bot
