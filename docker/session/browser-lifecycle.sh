#!/bin/bash

# Sourced by PID 1. Give Chromium the first shutdown window so bookmarks,
# cookies and Preferences are flushed before X/audio/helper services disappear.
CHROME_PID=""
shutdown_browser() {
  trap '' TERM INT
  set +e
  if [ -n "$CHROME_PID" ] && kill -0 "$CHROME_PID" 2>/dev/null; then
    kill -TERM "$CHROME_PID" 2>/dev/null
    for _ in {1..70}; do
      kill -0 "$CHROME_PID" 2>/dev/null || break
      sleep 0.1
    done
    if kill -0 "$CHROME_PID" 2>/dev/null; then
      kill -KILL "$CHROME_PID" 2>/dev/null
    fi
    wait "$CHROME_PID" 2>/dev/null
  fi
  local worker
  for worker in $(jobs -pr); do
    kill -TERM "$worker" 2>/dev/null
  done
  exit 0
}
trap shutdown_browser TERM INT
