#!/usr/bin/env bash
set -euo pipefail

APP_DIR="/opt/media-gateway"
CONF_DIR="/etc/media-gateway"
SERVICE_USER="media-gw"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PKG_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

if [[ "${EUID}" -ne 0 ]]; then
  echo "Please run as root: sudo bash $0" >&2
  exit 1
fi

if ! id -u "${SERVICE_USER}" >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin "${SERVICE_USER}"
fi

mkdir -p "${APP_DIR}/bin" "${APP_DIR}/data" "${CONF_DIR}"
install -m 0755 "${PKG_ROOT}/media-gateway" "${APP_DIR}/bin/media-gateway"

if [[ ! -f "${CONF_DIR}/config.json" ]]; then
  install -m 0640 "${PKG_ROOT}/configs/gateway.example.json" "${CONF_DIR}/config.json"
  chown root:"${SERVICE_USER}" "${CONF_DIR}/config.json"
  echo "Initialized ${CONF_DIR}/config.json (please update auth_tokens and listen_addr before starting)"
else
  echo "Keeping existing ${CONF_DIR}/config.json"
fi

chown -R "${SERVICE_USER}:${SERVICE_USER}" "${APP_DIR}/data"

if command -v systemctl >/dev/null 2>&1; then
  install -m 0644 "${PKG_ROOT}/deploy/media-gateway.service" /etc/systemd/system/media-gateway.service
  systemctl daemon-reload
  echo "Installed systemd unit: /etc/systemd/system/media-gateway.service"
  echo "Next steps:"
  echo "  1. Edit ${CONF_DIR}/config.json (set auth_tokens and listen_addr)"
  echo "  2. sudo systemctl enable --now media-gateway"
  echo "  3. sudo journalctl -u media-gateway -f"
else
  echo "Installed binary to ${APP_DIR}/bin/media-gateway (systemd not detected; use start.sh/stop.sh)"
fi
