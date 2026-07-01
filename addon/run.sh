#!/usr/bin/env bash
# Add-on entrypoint: translate HA add-on options (/data/options.json) into
# zbsniff CLI flags and launch it. Data (DB + config) lives on the add-on volume.
set -euo pipefail

OPTS=/data/options.json
ARGS=()

if [ -f "$OPTS" ]; then
  # one --port per configured serial port (none = auto-detect)
  while IFS= read -r p; do
    [ -n "$p" ] && ARGS+=(--port "$p")
  done < <(jq -r '.serial_ports[]? // empty' "$OPTS")

  CH=$(jq -r '.channel // 0' "$OPTS")
  [ "${CH:-0}" != "0" ] && ARGS+=(--channel "$CH")

  KEY=$(jq -r '.network_key // empty' "$OPTS")
  [ -n "$KEY" ] && ARGS+=(--key "$KEY")

  HH=$(jq -r '.ha_host // empty' "$OPTS")
  [ -n "$HH" ] && ARGS+=(--ha-host "$HH")

  HT=$(jq -r '.ha_token // empty' "$OPTS")
  [ -n "$HT" ] && ARGS+=(--ha-token "$HT")
fi

# Persist DB + config on the add-on data volume; ingress serves on :8080.
ARGS+=(--db /data/zbsniff.sqlite --config /data/zbsniff.yaml --http-port 8080)

echo "starting: zbsniff ${ARGS[*]}"
exec /usr/bin/zbsniff "${ARGS[@]}"
