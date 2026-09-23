#!/bin/bash
set -euo pipefail

cd "$(dirname "$0")/.."

export RB_ADMIN_PASSWORD="${RB_ADMIN_PASSWORD:-}"
if [[ -n "$RB_ADMIN_PASSWORD" && ${#RB_ADMIN_PASSWORD} -lt 12 ]]; then
  echo "RB_ADMIN_PASSWORD must contain at least 12 characters for first initialization." >&2
  exit 1
fi
export RB_DEFAULT_INSTANCE_QUOTA="${RB_DEFAULT_INSTANCE_QUOTA:-${RB_MAX_SESSIONS:-5}}"
export RB_DATA_DIR="${RB_DATA_DIR:-./data}"
export RB_HTTP_ADDR="${RB_HTTP_ADDR:-:8080}"

echo "Starting RemoteBrowser Manager in dev mode..."
echo "Existing administrator credentials are preserved. First initialization requires RB_ADMIN_PASSWORD (12+ characters)."

exec go run ./cmd/manager
