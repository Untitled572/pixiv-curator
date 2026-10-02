#!/usr/bin/env python3
"""Check a native executable or running container through real HTTP requests."""

import argparse
import json
import os
from pathlib import Path
import socket
import subprocess
import time
import urllib.error
import urllib.request


def check(base, version, process):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    deadline = time.monotonic() + 30
    while True:
        if process is not None and process.poll() is not None:
            raise RuntimeError("server exited before becoming ready")
        try:
            with opener.open(base + "/healthz", timeout=2) as response:
                assert response.status == 200
                assert json.load(response) == {"status": "ok"}
            break
        except (OSError, urllib.error.URLError):
            if time.monotonic() >= deadline:
                raise
            time.sleep(0.1)
    with opener.open(base + "/version", timeout=2) as response:
        assert response.status == 200
        assert json.load(response) == {"name": "pixiv-curator", "version": version}
    print(f"PASS: healthz and version={version}", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument("--binary", type=Path)
    group.add_argument("--url")
    parser.add_argument("--version", default="dev")
    args = parser.parse_args()
    if args.url:
        check(args.url.rstrip("/"), args.version, None)
        return

    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    env = dict(os.environ, PIXIV_CURATOR_ADDR=f"127.0.0.1:{port}")
    process = subprocess.Popen([str(args.binary.resolve())], env=env)
    try:
        check(f"http://127.0.0.1:{port}", args.version, process)
    finally:
        process.terminate()
        try:
            process.wait(timeout=12)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
            raise
    # TerminateProcess is used on Windows; POSIX receives SIGTERM.
    if os.name != "nt" and process.returncode != 0:
        raise RuntimeError(f"server failed graceful shutdown: {process.returncode}")


if __name__ == "__main__":
    main()
