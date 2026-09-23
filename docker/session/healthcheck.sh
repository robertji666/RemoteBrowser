#!/bin/bash
set -euo pipefail

curl -fsS http://localhost:8081/healthz >/dev/null
runtime="$(< /tmp/remotebrowser-pulse-current)"
case "$runtime" in
  /tmp/remotebrowser-pulse.*) ;;
  *) exit 1 ;;
esac
export PULSE_RUNTIME_PATH="$runtime"
export PULSE_SERVER="unix:$runtime/native"
pactl list short sources | awk '$2 == "rb_audio.monitor" { found=1 } END { exit !found }'
