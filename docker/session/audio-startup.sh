#!/bin/bash

# A stopped container keeps its writable filesystem, including PulseAudio's
# old PID and socket. A fresh runtime on every start avoids a stale daemon
# being mistaken for a live one after PID reuse.
start_audio() {
local runtime_base="${1:-/tmp}"
local runtime_marker="${2:-/tmp/remotebrowser-pulse-current}"
export PULSE_RUNTIME_PATH
PULSE_RUNTIME_PATH="$(mktemp -d "$runtime_base/remotebrowser-pulse.XXXXXX")"
export PULSE_SERVER="unix:$PULSE_RUNTIME_PATH/native"
printf '%s\n' "$PULSE_RUNTIME_PATH" > "$runtime_marker"
pulseaudio --daemonize=no --exit-idle-time=-1 --use-pid-file=no --log-target=stderr &
PULSE_PID=$!
audio_ready=false
for _ in {1..50}; do
  if ! kill -0 "$PULSE_PID" 2>/dev/null; then
    echo "PulseAudio exited before becoming ready" >&2
    return 1
  fi
  if pactl info >/dev/null 2>&1; then
    audio_ready=true
    break
  fi
  sleep 0.1
done
if [ "$audio_ready" != true ]; then
  echo "PulseAudio did not become ready" >&2
  return 1
fi
pactl load-module module-null-sink sink_name=rb_audio rate=48000 channels=2 sink_properties=device.description=RemoteBrowser >/dev/null
pactl set-default-sink rb_audio
export PULSE_SINK=rb_audio
pactl list short sources | awk '$2 == "rb_audio.monitor" { found=1 } END { exit !found }'
}
