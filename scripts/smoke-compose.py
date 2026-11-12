#!/usr/bin/env python3
"""Run the real Compose template with private credentials and an empty test DB."""
import argparse
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import tempfile
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    parser.add_argument("--version", default="dev")
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    base = f"http://127.0.0.1:{port}"
    with tempfile.TemporaryDirectory(prefix="curator-compose-") as directory:
        work = Path(directory)
        private = work / "private"
        private.mkdir(mode=0o700)
        (work / "data").mkdir(mode=0o700)
        token = secrets.token_urlsafe(32)
        for name, value in {"admin-token": token + "\n", "oauth.json": json.dumps({"client_id": "offline-test", "client_secret": "offline-test"})}.items():
            path = private / name
            path.write_text(value, encoding="utf-8")
            path.chmod(0o600)
        shutil.copyfile(root / "deploy/compose.yml", work / "compose.yml")
        env = dict(os.environ, PIXIV_CURATOR_IMAGE=args.image,
                   PIXIV_CURATOR_PULL_POLICY="never",
                   PIXIV_CURATOR_UID=str(os.getuid()),
                   PIXIV_CURATOR_GID=str(os.getgid()),
                   PIXIV_CURATOR_ADDR=f"127.0.0.1:{port}",
                   PIXIV_CURATOR_PUBLIC_BASE_URL=base)
        compose = ["docker", "compose", "--project-name", "curator-smoke-" + secrets.token_hex(4)]
        def run(*arguments, **kwargs):
            return subprocess.run([*compose, *arguments], cwd=work, env=env, **kwargs)
        try:
            run("config", "--quiet", check=True)
            run("up", "-d", "--pull", "never", check=True)
            subprocess.run(["python3", str(root / "scripts/smoke-test.py"), "--url", base, "--version", args.version], check=True)
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
            request = urllib.request.Request(base + "/admin/login", data=json.dumps({"token": token}).encode(), headers={"Content-Type": "application/json"})
            with opener.open(request, timeout=5) as response:
                assert response.status == 200
                assert json.load(response)["status"] == "authenticated"
            with opener.open(base + "/admin/api/stats", timeout=5) as response:
                stats = json.load(response)
                assert stats["accounts"] == 0 and stats["image_pages"] == 0
            print("PASS: Compose private directory mount, login and authenticated statistics")
        finally:
            run("logs", "--tail", "20", check=False)
            run("down", "--volumes", check=True)


if __name__ == "__main__":
    main()
