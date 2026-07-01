#!/usr/bin/env bash
# Add-on entrypoint: translate HA add-on options (/data/options.json) into the
# host app's CLI/env and launch it. Falls back to env vars for plain Docker.
set -euo pipefail

OPTS=/data/options.json
ARGS=()

if [ -f "$OPTS" ] && command -v jq >/dev/null 2>&1; then
  CHANNEL=$(jq -r '.channel // empty' "$OPTS")
  KEY=$(jq -r '.network_key // empty' "$OPTS")
  DECRYPT=$(jq -r '.decrypt // false' "$OPTS")
  # one --port per configured serial port
  while IFS= read -r p; do
    [ -n "$p" ] && ARGS+=(--port "$p")
  done < <(jq -r '.serial_ports[]? // empty' "$OPTS")
  [ -n "${CHANNEL:-}" ] && ARGS+=(--channel "$CHANNEL")
  [ "${DECRYPT}" = "true" ] && [ -n "${KEY:-}" ] && ARGS+=(--key "$KEY")
fi

# Persist the DB on the add-on data volume.
ARGS+=(--db /data/zbsniff.sqlite --http-port "${ZB_HTTP_PORT:-8080}")

exec python -m zbsniff.api.server "${ARGS[@]}"
