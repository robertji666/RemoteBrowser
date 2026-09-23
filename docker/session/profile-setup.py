#!/usr/bin/env python3
"""Prepare the mounted Chromium profile without resetting persistent state."""

import json
import os
import sys
import tempfile
from pathlib import Path


def prepare_profile(profile, downloads):
    profile = Path(profile)
    preferences = profile / "Default" / "Preferences"
    preferences.parent.mkdir(parents=True, exist_ok=True)
    try:
        with preferences.open(encoding="utf-8") as source:
            prefs = json.load(source)
    except FileNotFoundError:
        prefs = {}
    # A malformed or unreadable existing profile needs repair, not a silent
    # replacement. Let startup fail while preserving the original bytes.
    if not isinstance(prefs, dict):
        raise ValueError("Chromium Preferences must be a JSON object")
    prefs["selectfile.last_directory"] = str(downloads)
    download = prefs.get("download")
    if not isinstance(download, dict):
        download = {}
        prefs["download"] = download
    download["default_directory"] = str(downloads)
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=preferences.parent,
                                         prefix=".Preferences-", delete=False) as output:
            temporary = output.name
            json.dump(prefs, output, ensure_ascii=False)
            output.write("\n")
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, preferences)
        temporary = None
    finally:
        if temporary is not None:
            os.unlink(temporary)
    # Only one container is allowed to mount an instance profile. These links
    # refer to processes/sockets in the old container and must not survive a
    # stopped-container replacement (especially with a different hostname).
    for name in ("SingletonLock", "SingletonSocket", "SingletonCookie"):
        candidate = profile / name
        if candidate.is_symlink():
            candidate.unlink()


if __name__ == "__main__":
    prepare_profile(*sys.argv[1:])
