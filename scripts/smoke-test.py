#!/usr/bin/env python3
"""Check a native executable or running container through real HTTP requests."""

import argparse
import http.cookiejar
import json
import os
from pathlib import Path
import socket
import secrets
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def check(base, version, process, admin_token=None):
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
    with opener.open(base + "/readyz", timeout=2) as response:
        assert response.status == 200
        assert json.load(response) == {"status": "ready"}
    with opener.open(base + "/", timeout=2) as response:
        assert response.status == 200
        assert response.url == base + "/admin/login"
        assert response.headers.get_content_type() == "text/html"
    try:
        opener.open(base + "/admin/api/artists", timeout=2)
        raise AssertionError("management route accepted an unauthenticated request")
    except urllib.error.HTTPError as error:
        assert error.code == 401
    if admin_token is not None:
        browser = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        request = urllib.request.Request(base + "/admin/login", data=json.dumps({"token": admin_token}).encode(), headers={"Content-Type": "application/json"})
        with browser.open(request, timeout=2) as response:
            assert response.status == 200
        with browser.open(base + "/", timeout=2) as response:
            assert response.status == 200
            assert response.url == base + "/admin/"
            assert response.headers.get_content_type() == "text/html"
    print(f"PASS: healthz, readyz, root login entry, management authentication and version={version}", flush=True)


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
    private = tempfile.TemporaryDirectory(prefix="curator-smoke-")
    token = secrets.token_urlsafe(32)
    credential = Path(private.name) / "admin-token"
    credential.write_text(token + "\n", encoding="utf-8")
    credential.chmod(0o600)
    env = dict(os.environ, PIXIV_CURATOR_ADDR=f"127.0.0.1:{port}",
               PIXIV_CURATOR_DATABASE_PATH=str(Path(private.name) / "catalog.db"),
               PIXIV_CURATOR_ADMIN_TOKEN_FILE=str(credential), PIXIV_CURATOR_PIXIV_TOKEN_FILE="")
    process = subprocess.Popen([str(args.binary.resolve())], env=env)
    try:
        check(f"http://127.0.0.1:{port}", args.version, process, token)
    finally:
        process.terminate()
        try:
            process.wait(timeout=12)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
            raise
        private.cleanup()
    # TerminateProcess is used on Windows; POSIX receives SIGTERM.
    if os.name != "nt" and process.returncode != 0:
        raise RuntimeError(f"server failed graceful shutdown: {process.returncode}")


if __name__ == "__main__":
    main()
