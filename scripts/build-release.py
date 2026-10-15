#!/usr/bin/env python3
"""Build the four supported CGO-free release archives and their checksums."""

import argparse
import hashlib
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[1]
TARGETS = (("linux", "amd64"), ("linux", "arm64"), ("windows", "amd64"), ("windows", "arm64"))
VERSION_PATTERN = r"(?:dev|v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?)"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", default="dev")
    parser.add_argument("--output", type=Path, default=ROOT / "dist")
    args = parser.parse_args()
    if not re.fullmatch(VERSION_PATTERN, args.version):
        parser.error("version must be dev or vMAJOR.MINOR.PATCH[-prerelease]")

    args.output.mkdir(parents=True, exist_ok=True)
    archives = []
    for goos, goarch in TARGETS:
        basename = f"pixiv-curator_{args.version}_{goos}_{goarch}"
        with tempfile.TemporaryDirectory(prefix="pixiv-curator-") as temp:
            binary = Path(temp) / ("pixiv-curator.exe" if goos == "windows" else "pixiv-curator")
            env = dict(os.environ, CGO_ENABLED="0", GOOS=goos, GOARCH=goarch)
            subprocess.run([
                "go", "build", "-mod=readonly", "-buildvcs=false", "-trimpath",
                "-ldflags", f"-s -w -X main.version={args.version}",
                "-o", str(binary), "./cmd/api",
            ], cwd=ROOT, env=env, check=True)
            files = [(binary, binary.name), (ROOT / "README.md", "README.md"),
                     (ROOT / "LICENSE", "LICENSE"),
                     (ROOT / ".env.example", ".env.example"),
                     (ROOT / "docs/releasing.md", "docs/releasing.md"),
                     (ROOT / "docs/running.md", "docs/running.md"),
                     (ROOT / "docs/pixiv-login.md", "docs/pixiv-login.md"),
                     (ROOT / "docs/third-party.md", "docs/third-party.md"),
                     (ROOT / "scripts/pixiv-login.py", "scripts/pixiv-login.py"),
                     (ROOT / "scripts/pixiv-callback.py", "scripts/pixiv-callback.py"),
                     (ROOT / "scripts/requirements-login.txt", "scripts/requirements-login.txt")]
            if goos == "windows":
                archive = args.output / f"{basename}.zip"
                with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as bundle:
                    for source, name in files:
                        bundle.write(source, arcname=f"{basename}/{name}")
            else:
                archive = args.output / f"{basename}.tar.gz"
                with tarfile.open(archive, "w:gz") as bundle:
                    for source, name in files:
                        bundle.add(source, arcname=f"{basename}/{name}", recursive=False)
            archives.append(archive)
            print(f"Built {archive}", flush=True)

    lines = []
    for path in archives:
        with path.open("rb") as stream:
            digest = hashlib.file_digest(stream, "sha256").hexdigest()
        lines.append(f"{digest}  {path.name}\n")
    (args.output / "SHA256SUMS").write_text("".join(lines), encoding="utf-8")
    print("Built all four targets and SHA256SUMS", flush=True)


if __name__ == "__main__":
    main()
