#!/usr/bin/env bash
set -euo pipefail

VERSION="${VERSION:-1.0.0}"
ARCH_LIST="${ARCH_LIST:-amd64}"
OUTPUT_DIR="${OUTPUT_DIR:-dist}"
SKIP_TESTS="${SKIP_TESTS:-false}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SERVER_DIR="${REPO_ROOT}/server"
OUT_PATH="${OUTPUT_DIR}"
if [[ "${OUT_PATH}" != /* ]]; then
  OUT_PATH="${REPO_ROOT}/${OUT_PATH}"
fi

if [[ "${ARCH_LIST}" == "all" ]]; then
  ARCH_LIST="amd64 arm64"
fi

if [[ "${SKIP_TESTS}" != "true" ]]; then
  echo "==> Running go vet & go test..."
  (cd "${SERVER_DIR}" && go vet ./... && go test -count=1 ./...)
fi

mkdir -p "${OUT_PATH}"
: > "${OUT_PATH}/SHA256SUMS.txt"

for arch in ${ARCH_LIST}; do
  pkg_name="media-gateway-${VERSION}-linux-${arch}"
  stage_parent="$(mktemp -d)"
  stage_dir="${stage_parent}/${pkg_name}"
  mkdir -p "${stage_dir}/configs" "${stage_dir}/deploy" "${stage_dir}/data"

  echo "==> Building linux/${arch} binary..."
  (
    cd "${SERVER_DIR}"
    CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" go build -trimpath -ldflags "-s -w" -o "${stage_dir}/media-gateway" ./cmd/gateway
  )

  cp "${REPO_ROOT}/configs/gateway.example.json" "${stage_dir}/configs/gateway.example.json"
  cp "${REPO_ROOT}/deploy/systemd/media-gateway.service" "${stage_dir}/deploy/media-gateway.service"
  install -m 0755 "${REPO_ROOT}/deploy/scripts/install.sh" "${stage_dir}/deploy/install.sh"
  install -m 0755 "${REPO_ROOT}/deploy/scripts/start.sh" "${stage_dir}/start.sh"
  install -m 0755 "${REPO_ROOT}/deploy/scripts/stop.sh" "${stage_dir}/stop.sh"
  touch "${stage_dir}/data/.gitkeep"

  tar_name="${pkg_name}.tar.gz"
  tar_path="${OUT_PATH}/${tar_name}"
  echo "==> Packing ${tar_path}..."
  tar -czf "${tar_path}" -C "${stage_parent}" "${pkg_name}"
  rm -rf "${stage_parent}"

  if command -v sha256sum >/dev/null 2>&1; then
    (cd "${OUT_PATH}" && sha256sum "${tar_name}" >> SHA256SUMS.txt)
  elif command -v shasum >/dev/null 2>&1; then
    (cd "${OUT_PATH}" && shasum -a 256 "${tar_name}" >> SHA256SUMS.txt)
  fi
done

echo "==> Done! Artifacts in ${OUT_PATH}:"
cat "${OUT_PATH}/SHA256SUMS.txt"
