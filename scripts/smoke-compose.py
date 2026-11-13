#!/usr/bin/env python3
"""Run the real Compose template with private credentials and a populated legacy test DB."""
import argparse
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import sqlite3
import subprocess
import tempfile
import urllib.request


LEGACY_PAGE_COUNT = 12000


def seed_legacy_database(root, database):
    # Large UPDATEs spill SQLite statement journals even with a writable /data.
    # Keep the fixture offline: no accounts, no tasks, no enabled collection.
    with sqlite3.connect(database) as db:
        db.execute("PRAGMA foreign_keys=ON")
        db.execute("CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)")
        migrations = sorted((root / "internal/store/sqlite/migrations").glob("*.sql"))
        for version, migration in enumerate(migrations[:7], 1):
            db.executescript(migration.read_text(encoding="utf-8"))
            db.execute("INSERT INTO schema_migrations VALUES(?,0)", (version,))
        db.execute("INSERT INTO artists(id,created_at,updated_at) VALUES(1,0,0)")
        db.executemany("INSERT INTO artworks(id,artist_id,page_count,created_at,updated_at) VALUES(?,1,2,0,0)",
                       ((i,) for i in range(1, LEGACY_PAGE_COUNT + 1)))
        url = "https://i.pximg.net/img-original/img/2026/01/01/00/00/00/"
        db.executemany("INSERT INTO image_pages(illust_id,page_index,original_url,preview_url,width,height,media_state,created_at,updated_at) VALUES(?,1,?,?,1920,1080,'indexed',0,0)",
                       ((i, url + str(i) + "_p1.jpg", url + str(i) + "_p1.jpg") for i in range(1, LEGACY_PAGE_COUNT + 1)))
    database.chmod(0o600)


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
        seed_legacy_database(root, work / "data/curator.db")
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
            with opener.open(base + "/", timeout=5) as response:
                assert response.status == 200
                assert response.url == base + "/admin/"
                assert response.headers.get_content_type() == "text/html"
            with opener.open(base + "/admin/api/stats", timeout=5) as response:
                stats = json.load(response)
                assert stats["accounts"] == 0 and stats["image_pages"] == LEGACY_PAGE_COUNT
            with sqlite3.connect(work / "data/curator.db") as db:
                assert db.execute("SELECT max(version) FROM schema_migrations").fetchone()[0] == 13
                assert db.execute("SELECT count(*) FROM image_pages WHERE width<>0 OR height<>0").fetchone()[0] == 0
                assert not db.execute("PRAGMA foreign_key_check").fetchall()
            print("PASS: Compose legacy migration, writable temporary directory, private mount, login and statistics")
        finally:
            run("logs", "--tail", "20", check=False)
            run("down", "--volumes", check=True)


if __name__ == "__main__":
    main()
