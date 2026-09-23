import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest


SESSION = Path(__file__).resolve().parent.parent
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("profile_setup", SESSION / "profile-setup.py")
profile_setup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(profile_setup)


class ProfileSetupTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.profile = Path(self.directory.name) / "profile"
        self.preferences = self.profile / "Default" / "Preferences"
        self.preferences.parent.mkdir(parents=True)

    def test_existing_preferences_and_profile_data_survive_repeated_start(self):
        self.preferences.write_text(json.dumps({"browser": {"custom": "持久设置"},
                                               "download": {"extensions_to_open": "pdf"}}))
        cookies = self.profile / "Default" / "Cookies"
        cookies.write_bytes(b"existing encrypted cookie database")
        for _ in range(2):
            profile_setup.prepare_profile(self.profile, "/home/rbuser/Downloads")
        prefs = json.loads(self.preferences.read_text())
        self.assertEqual(prefs["browser"]["custom"], "持久设置")
        self.assertEqual(prefs["download"]["extensions_to_open"], "pdf")
        self.assertEqual(prefs["download"]["default_directory"], "/home/rbuser/Downloads")
        self.assertEqual(cookies.read_bytes(), b"existing encrypted cookie database")
        self.assertEqual(self.preferences.stat().st_mode & 0o777, 0o600)

    def test_damaged_preferences_are_preserved_and_fail_startup(self):
        for contents in (b"{incomplete", b"[]"):
            self.preferences.write_bytes(contents)
            with self.assertRaises((ValueError, json.JSONDecodeError)):
                profile_setup.prepare_profile(self.profile, "/downloads")
            self.assertEqual(self.preferences.read_bytes(), contents)

    def test_temporary_symlink_cannot_redirect_profile_write(self):
        outside = Path(self.directory.name) / "outside"
        outside.write_text("do not touch")
        Path(str(self.preferences) + ".tmp").symlink_to(outside)
        profile_setup.prepare_profile(self.profile, "/downloads")
        self.assertEqual(outside.read_text(), "do not touch")

    def test_stale_container_singleton_links_unlinked_without_following(self):
        outside = Path(self.directory.name) / "outside"
        outside.write_text("keep")
        for name in ("SingletonLock", "SingletonSocket", "SingletonCookie"):
            (self.profile / name).symlink_to(outside)
        profile_setup.prepare_profile(self.profile, "/downloads")
        self.assertEqual(outside.read_text(), "keep")
        for name in ("SingletonLock", "SingletonSocket", "SingletonCookie"):
            self.assertFalse(os.path.lexists(self.profile / name))

    def test_sigterm_flushes_browser_before_entrypoint_exits(self):
        marker = Path(self.directory.name) / "flushed"
        ready = Path(self.directory.name) / "ready"
        fake_browser = Path(self.directory.name) / "browser.py"
        fake_browser.write_text(
            "import signal,time,sys\nfrom pathlib import Path\n"
            "def stop(*args):\n    time.sleep(0.2)\n    Path(sys.argv[1]).write_text('flushed')\n    raise SystemExit(0)\n"
            "signal.signal(signal.SIGTERM,stop)\nPath(sys.argv[2]).write_text('ready')\n"
            "while True: time.sleep(0.05)\n")
        command = 'source "$1"; "$2" "$3" "$4" "$5" & CHROME_PID=$!; wait "$CHROME_PID" || true; shutdown_browser'
        child = subprocess.Popen(["bash", "-c", command, "test", str(SESSION / "browser-lifecycle.sh"),
                                  sys.executable, str(fake_browser), str(marker), str(ready)])
        try:
            deadline = time.monotonic() + 3
            while not ready.exists() and time.monotonic() < deadline:
                time.sleep(0.01)
            self.assertTrue(ready.exists(), "fake browser failed to start")
            child.send_signal(signal.SIGTERM)
            self.assertEqual(child.wait(timeout=3), 0)
            self.assertEqual(marker.read_text(), "flushed")
        finally:
            if child.poll() is None:
                child.kill()
                child.wait()

    def test_audio_restart_allocates_fresh_runtime_and_verifies_monitor(self):
        binary = Path(self.directory.name) / "bin"
        binary.mkdir()
        pulse = binary / "pulseaudio"
        pulse.write_text("#!/bin/sh\ntrap 'exit 0' TERM\nwhile :; do sleep 0.1; done\n")
        pulse.chmod(0o755)
        pactl = binary / "pactl"
        pactl.write_text("#!/bin/sh\nprintf '%s %s\\n' \"$PULSE_SERVER\" \"$*\" >> \"$TEST_AUDIO_LOG\"\n"
                         "if [ \"$*\" = 'list short sources' ]; then printf '0\\trb_audio.monitor\\n'; fi\n")
        pactl.chmod(0o755)
        log = Path(self.directory.name) / "audio.log"
        marker = Path(self.directory.name) / "runtime"
        env = os.environ.copy()
        env["PATH"] = str(binary) + os.pathsep + env["PATH"]
        env["TEST_AUDIO_LOG"] = str(log)
        command = 'set -e; source "$1"; source "$2"; start_audio "$3" "$4"; shutdown_browser'
        runtimes = []
        for _ in range(2):
            result = subprocess.run(["bash", "-c", command, "test", str(SESSION / "browser-lifecycle.sh"),
                                     str(SESSION / "audio-startup.sh"), self.directory.name, str(marker)],
                                    env=env, timeout=3, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            runtimes.append(marker.read_text().strip())
        self.assertNotEqual(runtimes[0], runtimes[1])
        entries = log.read_text()
        for runtime in runtimes:
            self.assertIn("unix:" + runtime + "/native info", entries)
            self.assertIn("unix:" + runtime + "/native list short sources", entries)
        self.assertIn("load-module module-null-sink sink_name=rb_audio", entries)


if __name__ == "__main__":
    unittest.main()
