#!/usr/bin/env python3
import base64
import hashlib
import os
import signal
import socketserver
import struct
import subprocess
import sys


PORT = int(os.environ.get("AUDIO_SERVICE_PORT", "6081"))
PULSE_SOURCE = os.environ.get("RB_AUDIO_SOURCE", "rb_audio.monitor")
RATE = os.environ.get("RB_AUDIO_RATE", "48000")
CHANNELS = os.environ.get("RB_AUDIO_CHANNELS", "2")
CHUNK_SIZE = 8192


class AudioHandler(socketserver.BaseRequestHandler):
    def handle(self):
        headers = self.read_headers()
        key = headers.get("sec-websocket-key")
        if not key:
            self.request.sendall(b"HTTP/1.1 400 Bad Request\r\n\r\n")
            return

        accept = base64.b64encode(
            hashlib.sha1((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()
        ).decode()
        self.request.sendall(
            (
                "HTTP/1.1 101 Switching Protocols\r\n"
                "Upgrade: websocket\r\n"
                "Connection: Upgrade\r\n"
                f"Sec-WebSocket-Accept: {accept}\r\n"
                "X-Audio-Format: s16le;rate=48000;channels=2\r\n"
                "\r\n"
            ).encode()
        )

        proc = subprocess.Popen(
            [
                "parec",
                "--raw",
                f"--format=s16le",
                f"--rate={RATE}",
                f"--channels={CHANNELS}",
                f"--device={PULSE_SOURCE}",
            ],
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            preexec_fn=os.setsid,
        )

        try:
            while True:
                chunk = proc.stdout.read(CHUNK_SIZE)
                if not chunk:
                    break
                self.write_binary_frame(chunk)
        except (BrokenPipeError, ConnectionResetError, OSError):
            pass
        finally:
            try:
                os.killpg(proc.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            proc.wait(timeout=2)

    def read_headers(self):
        data = b""
        while b"\r\n\r\n" not in data:
            part = self.request.recv(4096)
            if not part:
                break
            data += part
            if len(data) > 16384:
                break

        lines = data.decode("iso-8859-1", errors="replace").split("\r\n")
        headers = {}
        for line in lines[1:]:
            if ":" in line:
                name, value = line.split(":", 1)
                headers[name.strip().lower()] = value.strip()
        return headers

    def write_binary_frame(self, payload):
        size = len(payload)
        if size < 126:
            header = struct.pack("!BB", 0x82, size)
        elif size <= 0xFFFF:
            header = struct.pack("!BBH", 0x82, 126, size)
        else:
            header = struct.pack("!BBQ", 0x82, 127, size)
        self.request.sendall(header + payload)


class ThreadingTCPServer(socketserver.ThreadingMixIn, socketserver.TCPServer):
    allow_reuse_address = True
    daemon_threads = True


if __name__ == "__main__":
    with ThreadingTCPServer(("", PORT), AudioHandler) as server:
        print(f"Audio service listening on :{PORT}", flush=True)
        try:
            server.serve_forever()
        except KeyboardInterrupt:
            sys.exit(0)
