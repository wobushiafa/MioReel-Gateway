#!/usr/bin/env bash
set -euo pipefail

BASE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PID_FILE="${BASE_DIR}/media-gateway.pid"

if [[ ! -f "${PID_FILE}" ]]; then
  echo "media-gateway is not running (no pid file)"
  exit 0
fi

PID="$(cat "${PID_FILE}")"
if [[ -z "${PID}" ]] || ! kill -0 "${PID}" 2>/dev/null; then
  rm -f "${PID_FILE}"
  echo "media-gateway is not running (stale pid file removed)"
  exit 0
fi

kill -TERM "${PID}"
for _ in $(seq 1 15); do
  if ! kill -0 "${PID}" 2>/dev/null; then
    rm -f "${PID_FILE}"
    echo "Stopped media-gateway (pid=${PID})"
    exit 0
  fi
  sleep 1
done

echo "Process ${PID} did not exit cleanly within 15s; sending SIGKILL" >&2
kill -KILL "${PID}" 2>/dev/null || true
rm -f "${PID_FILE}"
