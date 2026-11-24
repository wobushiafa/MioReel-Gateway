#!/usr/bin/env bash
set -euo pipefail

BASE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN="${BASE_DIR}/media-gateway"
CONFIG_FILE="${CONFIG_FILE:-${BASE_DIR}/configs/gateway.json}"
LOG_DIR="${BASE_DIR}/logs"
PID_FILE="${BASE_DIR}/media-gateway.pid"

if [[ -f "${PID_FILE}" ]]; then
  OLD_PID="$(cat "${PID_FILE}")"
  if [[ -n "${OLD_PID}" ]] && kill -0 "${OLD_PID}" 2>/dev/null; then
    echo "media-gateway is already running (pid=${OLD_PID})"
    exit 0
  fi
  rm -f "${PID_FILE}"
fi

mkdir -p "${LOG_DIR}" "${BASE_DIR}/data"

ARGS=()
if [[ -f "${CONFIG_FILE}" ]]; then
  ARGS+=(--config "${CONFIG_FILE}")
fi
if [[ -n "${LISTEN_ADDR:-}" ]]; then
  ARGS+=(--listen "${LISTEN_ADDR}")
fi
if [[ -n "${PORT:-}" ]]; then
  ARGS+=(--port "${PORT}")
fi
if [[ $# -gt 0 ]]; then
  ARGS+=("$@")
fi

nohup "${BIN}" "${ARGS[@]}" >>"${LOG_DIR}/gateway.log" 2>&1 &
NEW_PID=$!
echo "${NEW_PID}" > "${PID_FILE}"
echo "Started media-gateway (pid=${NEW_PID}), logs: ${LOG_DIR}/gateway.log"
