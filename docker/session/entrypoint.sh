#!/bin/bash
set -e

source /usr/local/bin/browser-lifecycle.sh

# Keep one canonical, case-sensitive directory name.  A previous image
# created a lowercase directory and a `Downloads` symlink, which made GTK's
# chooser display the same files twice.
mkdir -p "${RB_DOWNLOADS_DIR:-/home/rbuser/Downloads}"

# Chromium remembers the last directory used by the native upload chooser in
# the profile. Keep it aligned with the directory exposed by the manager's
# file panel so website uploads open on the files the user has just uploaded.
# Do not replace the profile: merge this one preference into an existing JSON
# file when a session is resumed.
export RB_PROFILE_DIR="${RB_PROFILE_DIR:-$HOME/profile}"
python3 /usr/local/bin/profile-setup.py "$RB_PROFILE_DIR" "${RB_DOWNLOADS_DIR:-/home/rbuser/Downloads}"

mkdir -p "$HOME/.vnc"
cat > "$HOME/.vnc/xstartup" <<'EOF'
#!/bin/sh
unset SESSION_MANAGER
unset DBUS_SESSION_BUS_ADDRESS
exec fluxbox
EOF
chmod +x "$HOME/.vnc/xstartup"

mkdir -p "$HOME/.fluxbox"
cat > "$HOME/.fluxbox/init" <<'EOF'
session.screen0.toolbar.visible: false
session.screen0.toolbar.autoHide: true
session.screen0.fullMaximization: true
session.screen0.maxDisableMove: false
session.screen0.maxDisableResize: false
EOF

SCREEN_WIDTH="${RB_SCREEN_WIDTH:-1280}"
SCREEN_HEIGHT="${RB_SCREEN_HEIGHT:-752}"
SCREEN_DEPTH="${RB_SCREEN_DEPTH:-24}"
CHROME_WINDOW_TOP="${RB_CHROME_WINDOW_TOP:-32}"
CHROME_WINDOW_BOTTOM="${RB_CHROME_WINDOW_BOTTOM:-0}"
CHROME_WINDOW_HEIGHT=$((SCREEN_HEIGHT - CHROME_WINDOW_TOP - CHROME_WINDOW_BOTTOM))
if [ "${CHROME_WINDOW_HEIGHT}" -lt 480 ]; then
  CHROME_WINDOW_HEIGHT="${SCREEN_HEIGHT}"
  CHROME_WINDOW_TOP=0
fi

# Start PulseAudio with a virtual sink. Chromium writes to this sink and the
# audio service streams the monitor source as raw PCM.
source /usr/local/bin/audio-startup.sh
start_audio

# Start TigerVNC's X server. It owns DISPLAY :1 and exposes it on localhost:5901.
vncserver :1 -geometry "${SCREEN_WIDTH}x${SCREEN_HEIGHT}" -depth "${SCREEN_DEPTH}" -localhost yes -SecurityTypes None &
sleep 1
pkill xmessage >/dev/null 2>&1 || true

# Start noVNC websockify
websockify --web /usr/share/novnc --cert none --key none 6080 localhost:5901 &
sleep 1

# Start file service
/usr/local/bin/file-service &

# Start audio service
/usr/local/bin/audio-service &

# Start WebRTC signaling and media service
/usr/local/bin/webrtc-service &

# Start input and clipboard service
/usr/local/bin/input-service &

# Start Chromium
chromium \
  --user-data-dir="${RB_PROFILE_DIR}" \
  --window-position=0,"${CHROME_WINDOW_TOP}" \
  --window-size="${SCREEN_WIDTH},${CHROME_WINDOW_HEIGHT}" \
  --no-sandbox \
  --test-type \
  --disable-dev-shm-usage \
  --disable-background-networking \
  --disable-sync \
  --disable-component-update \
  --disable-features=OptimizationGuideModelDownloading,OptimizationHints,MediaRouter,Translate \
  --disable-session-crashed-bubble \
  --hide-crash-restore-bubble \
  --load-extension=/opt/remotebrowser/download-extension \
  --no-first-run \
  --no-default-browser-check \
  --display=:1 \
  "$@" &
CHROME_PID=$!

# An exited browser must not leave a healthy-looking file-service-only
# container. The persistent Docker restart policy recreates the process.
wait "$CHROME_PID" || true
shutdown_browser
